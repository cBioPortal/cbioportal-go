// cbioportal-api — a cBioPortal-compatible REST API served from the ClickHouse
// transform views. JSON field shapes match the Java backend so the existing
// frontend works unchanged.
//
// Server settings are flags with environment-variable fallbacks (see -h).
// Portal properties (skin.*, priority_studies, quick_search.enabled, …) come
// from the file named by -portal-properties (an application.properties or a
// legacy portal.properties) and from --key=value arguments, which take
// precedence — the same arguments the Java portal is started with. The
// login settings have go names too (--login, --login-google-client-id, …),
// translated into the Java portal's properties.
//
//	cbioportal-api [flags]          serve
//	cbioportal-api config [flags]   print the effective settings
//	cbioportal-api check [flags]    check the settings and the services they name
//	cbioportal-api version
package main

import (
	"log/slog"

	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/KimMachineGun/automemlimit/memlimit"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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

// commandNames are the subcommands; without one, the server serves.
var commandNames = []string{"serve", "config", "check", "version", "help"}

// run executes one command line and returns its exit status (cli.Exit*).
func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	cmd := "serve"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd = args[0]
		known := false
		for _, c := range commandNames {
			known = known || c == cmd
		}
		if !known {
			fmt.Fprintf(stderr, "%s: unknown command %q%s\nRun '%s -h' for usage.\n",
				name, cmd, cli.DidYouMean(cli.Suggest(cmd, commandNames)), name)
			return cli.ExitUsage
		}
		args = args[1:]
	}
	jsonOut := false
	if cmd != "serve" {
		jsonOut, args = cli.JSONFlag(args)
	}
	if cmd == "help" {
		printUsage(stdout)
		return cli.ExitOK
	}
	cfg, err := parseArgs(args, getenv)
	if errors.Is(err, flag.ErrHelp) {
		printUsage(stdout)
		return cli.ExitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\nRun '%s -h' for usage.\n", name, err, name)
		return cli.ExitUsage
	}
	if cfg.showVersion || cmd == "version" {
		if jsonOut {
			cli.WriteJSON(stdout, buildinfo.Get(name))
		} else {
			fmt.Fprintln(stdout, buildinfo.String(name))
		}
		return cli.ExitOK
	}
	if _, err := bulkLimits(cfg.values); err != nil {
		fmt.Fprintf(stderr, "%s: %v\nRun '%s -h' for usage.\n", name, err, name)
		return cli.ExitUsage
	}
	ld, err := load(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitUsage
	}
	switch cmd {
	case "config":
		return printConfig(stdout, cfg, ld, jsonOut)
	case "check":
		return runCheck(stdout, stderr, cfg, ld, jsonOut)
	}
	return serve(stderr, cfg, ld)
}

// loaded is the configuration every command starts from: the portal
// properties, with the go settings translated into them.
type loaded struct {
	props map[string]string
	// propSources says where each portal property came from: the
	// properties file, an argument, or a go setting.
	propSources map[string]string
}

func load(cfg *settings) (*loaded, error) {
	file := cfg.values["portal-properties"]
	props, err := config.LoadPortalProperties(file, cfg.portalArgs)
	if err != nil {
		return nil, fmt.Errorf("portal properties: %w", err)
	}
	sources := map[string]string{}
	for k := range props {
		sources[k] = "file " + file
	}
	for _, a := range cfg.portalArgs {
		if k, _, ok := cutProperty(a); ok {
			sources[k] = "argument"
		}
	}
	from, err := config.ApplySettings(props, cfg.goSettings())
	if err != nil {
		return nil, err
	}
	for k, s := range from {
		sources[k] = "--" + s
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
func newState(cfg *settings, ld *loaded) (*api.State, error) {
	// internal/api reads these settings from the environment.
	for _, o := range allOptions() {
		if o.export {
			os.Setenv(o.env, cfg.values[o.name])
		}
	}
	v := cfg.values
	client := ch.New(v["clickhouse-url"], v["db"])
	client.SetDatabaseAliases(v["raw-db"], v["ref-db"])
	limits, err := bulkLimits(v)
	if err != nil {
		return nil, err
	}
	state := api.NewState(client)
	state.SetBulkLimits(limits)
	state.Props = ld.props
	state.PropsReplaceDefaults = v["portal-properties"] != ""
	if err := state.ConfigureAuth(); err != nil {
		return nil, err
	}
	if err := state.ConfigureSessions(); err != nil {
		return nil, err
	}
	return state, nil
}

func serve(stderr io.Writer, cfg *settings, ld *loaded) int {
	state, err := newState(cfg, ld)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitUsage
	}
	handler := api.NewRouter(state)

	v := cfg.values
	addr := "0.0.0.0:" + v["port"]
	log.Printf("%s listening on %s (clickhouse %s, db %s, raw-db %s, ref-db %s)",
		buildinfo.String(name), addr, v["clickhouse-url"], v["db"], v["raw-db"], v["ref-db"])
	log.Print(config.PropertiesSummary(ld.props, 8))
	switch {
	case v["frontend-url"] != "":
		log.Printf("frontend: the dev server at %s", v["frontend-url"])
	case strings.TrimSpace(ld.props["frontend.url"]) != "":
		log.Printf("frontend: loaded by the browser from %s", strings.TrimSpace(ld.props["frontend.url"]))
	case v["frontend-dir"] != "":
		log.Printf("frontend: the build in %s", v["frontend-dir"])
	default:
		log.Print("frontend: none; the portal pages load it from this server's /reactapp/, which is empty " +
			"without --frontend-dir (set --frontend-dir, --frontend-url or --frontend-assets-url)")
	}
	if m := v["metrics-addr"]; m != "" {
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
		log.Print(err)
		return cli.ExitFailure
	}
	<-drained
	return cli.ExitOK
}
