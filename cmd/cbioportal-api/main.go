// cbioportal-api — a cBioPortal-compatible REST API served from the ClickHouse
// transform views. JSON field shapes match the Java backend so the existing
// frontend works unchanged.
//
// Server settings are flags with environment-variable fallbacks (see -h).
// Portal properties (skin.*, priority_studies, quick_search.enabled, …) come
// from the file named by --portal-properties (an application.properties or
// a legacy portal.properties) and from --key=value arguments, which take
// precedence — the same arguments the Java portal is started with. The
// login settings have go names too (--login, --login-google-client-id, …),
// translated into the Java portal's properties.
//
//	cbioportal-api [flags]          serve
//	cbioportal-api serve [flags]    serve
//	cbioportal-api config [flags]   print the effective settings
//	cbioportal-api check [flags]    check the settings and the services they name
//	cbioportal-api version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/cbioportal/cbioportal-go/internal/api"
	"github.com/cbioportal/cbioportal-go/internal/buildinfo"
	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

const name = "cbioportal-api"

func main() {
	// Go's soft memory limit at 80% of the container's, unless GOMEMLIMIT is set.
	memlimit.Set(memlimit.WithRatio(0.8), memlimit.WithLogger(slog.Default()))
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

// run executes one command line and returns its exit status (cli.Exit*).
func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	s := &settings{getenv: getenv}
	root := newRootCmd(s)
	args, s.portalArgs = splitPortalArgs(root, args)
	return cli.Execute(root, args, stdout, stderr, getenv)
}

// newRootCmd returns the command tree. Every command takes the server's
// settings, so `config` and `check` run with the server's own command line.
func newRootCmd(s *settings) *cobra.Command {
	serve := func(cmd *cobra.Command, _ []string) error {
		ld, err := load(s)
		if err != nil {
			return err
		}
		return runServer(s, ld)
	}
	root := &cobra.Command{
		Use:   name,
		Short: "serve the cBioPortal REST API",
		Long: `cbioportal-api serves the cBioPortal REST API, and optionally the web
frontend, from the ClickHouse tables built by cbioportal-import. Without a
command it serves.

Each flag falls back to its environment variable, then to the built-in
default: flag > environment > default. The login flags are go's names for
the Java portal's Spring properties, which work too (docs/configuration.md
lists both).

Any other --key=value argument is a portal property (skin.title,
priority_studies, quick_search.enabled, ...), exactly as given to the Java
portal; it overrides the --portal-properties file. Keys the server does
not use (e.g. spring.datasource.*) are ignored and listed at startup.

Environment only (secrets are not flags: flags show in the process list):
` + envOnlyHelp() + `
Exit status: 0 success, 1 failure, 2 invalid command line or settings.`,
		Example: `  cbioportal-api --db cbioportal --frontend-dir frontend/dist
  cbioportal-api --port=8080 --skin.title="My portal" --quick_search.enabled=true
  cbioportal-api --portal-properties=./application.properties
  cbioportal-api --login=optional --login-google-client-id=<id>   (secret in $CBIOPORTAL_LOGIN_GOOGLE_CLIENT_SECRET)
  cbioportal-api check --json --db cbioportal
  PORT=8080 CBIOPORTAL_DB=cbioportal cbioportal-api`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return cli.UnknownCommand(cmd, args[0])
			}
			return nil
		},
		RunE: serve,
	}
	s.register(root.PersistentFlags())
	root.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "serve the API (the default)",
		Args:  cli.ArgsBetween(0, 0),
		RunE:  serve,
	})
	root.AddCommand(newConfigCmd(s), newCheckCmd(s))
	cli.AddVersion(root, name)
	return root
}

// envOnlyHelp lists the environment-only settings for the help.
func envOnlyHelp() string {
	var b strings.Builder
	for _, e := range envOnly() {
		fmt.Fprintf(&b, "  %s\n      %s\n", e[0], e[1])
	}
	return b.String()
}

// splitPortalArgs separates the portal property arguments from the rest.
// An argument --key=value whose key is not a flag is a portal property, so
// the Java portal's argument list can be passed unchanged; a near miss of a
// flag (--prot=8080) that is not a property the server knows stays a flag,
// so that it is reported as a typo. A flag that takes a value keeps the
// argument after it.
func splitPortalArgs(root *cobra.Command, args []string) (rest, portal []string) {
	flags := pflag.NewFlagSet(name, pflag.ContinueOnError)
	flags.AddFlagSet(root.PersistentFlags())
	var names []string
	flags.VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	ownFlag := map[string]bool{"h": true, "help": true, "version": true, "json": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(rest, args[i:]...), portal
		}
		key, ok := strings.CutPrefix(strings.TrimPrefix(a, "-"), "-")
		if !ok && !strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			continue
		}
		if !ok {
			key = strings.TrimPrefix(a, "-")
		}
		k, _, hasValue := strings.Cut(key, "=")
		f := flags.Lookup(k)
		switch {
		case f != nil:
			rest = append(rest, a)
			if !hasValue && f.NoOptDefVal == "" && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
		case ownFlag[k] || !hasValue || k == "":
			rest = append(rest, a)
		case cli.Suggest(k, names) != "" && !strings.Contains(k, ".") && !config.KnownProperty(k):
			rest = append(rest, a)
		default:
			portal = append(portal, "--"+key)
		}
	}
	return rest, portal
}

// loaded is the configuration every command starts from: the portal
// properties, with the go settings translated into them.
type loaded struct {
	props map[string]string
	// propSources says where each portal property came from: the
	// properties file, an argument, or a go setting.
	propSources map[string]string
}

// load reads the portal properties and applies the login settings to them.
// A problem with either is a mistake in the settings.
func load(s *settings) (*loaded, error) {
	file := s.portalProperties
	props, err := config.LoadPortalProperties(file, s.portalArgs)
	if err != nil {
		return nil, cli.Usagef("portal properties: %w", err)
	}
	sources := map[string]string{}
	for k := range props {
		sources[k] = "file " + file
	}
	for _, a := range s.portalArgs {
		if k, _, ok := cutProperty(a); ok {
			sources[k] = "argument"
		}
	}
	from, err := config.ApplySettings(props, s.loginValues())
	if err != nil {
		return nil, cli.Usage(err)
	}
	for k, src := range from {
		sources[k] = "--" + src
	}
	return &loaded{props: props, propSources: sources}, nil
}

// cutProperty splits a --key=value argument.
func cutProperty(a string) (key, value string, ok bool) {
	kv, ok := strings.CutPrefix(a, "--")
	if !ok {
		return "", "", false
	}
	key, value, ok = strings.Cut(kv, "=")
	return key, value, ok && key != ""
}

// newState builds the API state the settings describe, with its login and
// sessions configured.
func newState(s *settings, ld *loaded) (*api.State, error) {
	ch.SetLogComment(s.logComment)
	client := ch.New(s.clickhouseURL, s.db)
	client.SetDatabaseAliases(s.rawDB, s.refDB)
	state := api.NewState(client, s.api)
	state.SetBulkLimits(s.bulkLimits())
	state.Props = ld.props
	state.PropsReplaceDefaults = s.portalProperties != ""
	if err := state.ConfigureAuth(); err != nil {
		return nil, err
	}
	if err := state.ConfigureSessions(); err != nil {
		return nil, err
	}
	return state, nil
}

// runServer serves until SIGTERM or an interrupt, then lets in-flight
// requests finish.
func runServer(s *settings, ld *loaded) error {
	state, err := newState(s, ld)
	if err != nil {
		return cli.Usage(err)
	}
	handler := api.NewRouter(state)

	addr := fmt.Sprintf("0.0.0.0:%d", s.port)
	log.Printf("%s listening on %s (clickhouse %s, db %s, raw-db %s, ref-db %s)",
		buildinfo.String(name), addr, s.clickhouseURL, s.db, s.rawDB, s.refDB)
	log.Print(config.PropertiesSummary(ld.props, 8))
	switch {
	case s.api.FrontendURL != "" && s.api.FrontendDir == "":
		log.Printf("frontend: the dev server at %s", s.api.FrontendURL)
	case strings.TrimSpace(ld.props["frontend.url"]) != "":
		log.Printf("frontend: loaded by the browser from %s", strings.TrimSpace(ld.props["frontend.url"]))
	case s.api.FrontendDir != "":
		log.Printf("frontend: the build in %s", s.api.FrontendDir)
	default:
		log.Print("frontend: none; the portal pages load it from this server's /reactapp/, which is empty " +
			"without --frontend-dir (set --frontend-dir, --frontend-url or --frontend-assets-url)")
	}
	if m := s.metricsAddr; m != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", api.MetricsHandler())
		go func() {
			log.Printf("metrics on %s/metrics", m)
			log.Print(http.ListenAndServe(m, mux))
		}()
	}
	// SIGTERM (a rollout, a scale-down) lets in-flight requests finish and
	// exits 0, within Kubernetes' default 30 s grace period.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	srv := &http.Server{Addr: addr, Handler: handler}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		log.Print("shutting down: draining in-flight requests")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-drained
	return nil
}
