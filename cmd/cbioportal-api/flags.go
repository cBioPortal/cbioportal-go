package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/cli"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

// option is one server setting: a flag whose default comes from an
// environment variable, else from a built-in value.
type option struct {
	name   string // flag name
	arg    string // value placeholder in -h output
	env    string // environment variable it falls back to
	def    string // built-in default
	usage  string
	isBool bool
	// export marks settings that internal/api reads from the environment;
	// main exports their resolved value back into env before starting it.
	export bool
	// setting marks go's names for portal properties (config.Settings),
	// translated into those properties at startup.
	setting bool
}

type section struct {
	title string
	opts  []option
}

var sections = []section{
	{"Server", []option{
		{name: "port", arg: "port", env: "PORT", def: "8080", usage: "listen port; the server binds all interfaces (0.0.0.0)"},
	}},
	{"ClickHouse", []option{
		{name: "clickhouse-url", arg: "url", env: "CLICKHOUSE_URL", def: "http://localhost:18123", usage: "ClickHouse HTTP endpoint, e.g. https://<host>:8443 for ClickHouse Cloud"},
		{name: "clickhouse-log-comment", arg: "text", env: "CLICKHOUSE_LOG_COMMENT", usage: "tag every ClickHouse query with this log_comment (system.query_log), e.g. the deployment's name", export: true},
		{name: "db", arg: "name", env: "CBIOPORTAL_DB", def: "cbioportal", usage: "database built by `cbioportal-import transform`"},
		{name: "raw-db", arg: "name", env: "CBIOPORTAL_RAW_DB", def: "cbioportal_raw", usage: "staging database written by `cbioportal-import ingest`"},
		{name: "ref-db", arg: "name", env: "CBIOPORTAL_REF_DB", def: "cbioportal_ref", usage: "reference database written by `cbioportal-import load-reference`"},
		{name: "clickhouse-bulk-max-memory", arg: "size", env: "CLICKHOUSE_BULK_MAX_MEMORY", def: "10GiB", usage: "memory one query of a bulk endpoint (whole profiles of data, enrichments, export) may use; 0 for no limit"},
		{name: "clickhouse-bulk-max-time", arg: "duration", env: "CLICKHOUSE_BULK_MAX_TIME", def: "10m", usage: "time one query of a bulk endpoint may run, streaming its result included; 0 for no limit"},
		{name: "clickhouse-bulk-sort-spill", arg: "size", env: "CLICKHOUSE_BULK_SORT_SPILL", def: "1GiB", usage: "memory a sort of a bulk endpoint may use before it sorts on disk; 0 keeps the server's setting"},
	}},
	{"Frontend", []option{
		{name: "frontend-dir", arg: "dir", env: "FRONTEND_DIR", usage: "serve this frontend build for every non-/api/ path (single origin)", export: true},
		{name: "static-dir", arg: "dir", env: "CBIOPORTAL_STATIC_DIR", usage: "serve the files in this folder at their path, before the built-in ones, e.g.\n    \tstatic/images/logo.png at /images/logo.png (a portal's logo or custom tab scripts)", export: true},
		{name: "frontend-url", arg: "url", env: "FRONTEND_URL", usage: "reverse-proxy every non-/api/ path to this frontend dev server instead", export: true},
		{name: "oncokb-url", arg: "url", env: "ONCOKB_URL", def: "https://public.api.oncokb.org/api/v1", usage: "OncoKB API behind the frontend's /proxy/ endpoint", export: true},
		{name: "site-banner", arg: "text", env: "SITE_BANNER", usage: "notice bar at the top of every portal page, e.g. for a preview deployment", export: true},
		{name: "site-banner-link", arg: "url", env: "SITE_BANNER_LINK", usage: "link for the site banner", export: true},
	}},
	{"Sessions and auth", []option{
		{name: "session-store", arg: "file", env: "SESSION_STORE_FILE", usage: "persist sessions (virtual studies, groups, custom data) to this JSON file; unset keeps them in memory (unused with the session.service.url property)", export: true},
		{name: "auth-enabled", env: "AUTH_ENABLED", def: "false", usage: "require HS256 bearer tokens signed with $AUTH_JWT_SECRET", isBool: true, export: true},
		{name: "auth-default-user", arg: "user", env: "AUTH_DEFAULT_USER", usage: "single-user development login for requests without a token", export: true},
		{name: "metrics-addr", arg: "addr", env: "METRICS_ADDR", usage: "serve Prometheus metrics at /metrics on this address, e.g. :9090 (apart from the API)"},
		{name: "access-log", arg: "bool", env: "ACCESS_LOG", usage: "write a JSON access log line per request to stdout", export: true},
	}},
	{"Portal properties", []option{
		{name: "portal-properties", arg: "file", env: "PORTAL_PROPERTIES", usage: "application.properties or legacy portal.properties file (same key=value syntax)"},
	}},
}

func init() {
	// go's names for the login and login-session portal properties, before
	// the portal properties section; their secrets are environment only.
	var settings []section
	for _, ss := range config.Settings {
		sec := section{title: ss.Title}
		for _, st := range ss.Settings {
			if st.Secret {
				envOnly = append(envOnly, [2]string{st.Env, st.Usage})
				continue
			}
			sec.opts = append(sec.opts, option{name: st.Name, arg: st.Arg, env: st.Env, usage: st.Usage, setting: true})
		}
		settings = append(settings, sec)
	}
	last := len(sections) - 1
	sections = append(append(sections[:last:last], settings...), sections[last])
}

// envOnly are settings deliberately without a flag: secrets, which a flag
// would expose to every user through the process list (ps), and the profiler
// address, read before flags are parsed.
var envOnly = [][2]string{
	{"CLICKHOUSE_USER, CLICKHOUSE_PASSWORD", "ClickHouse credentials (unset: the default user)"},
	{"ONCOKB_TOKEN", "OncoKB API token sent by the /proxy/ endpoint"},
	{"AUTH_JWT_SECRET", "HS256 secret that bearer tokens are verified with"},
	{"PUBLISHER_API_KEY", "key required by /api/public_virtual_studies publish/unpublish (or the\n    \tsession.endpoint.publisher-api-key property)"},
	{"SESSION_SERVICE_URL, SESSION_SERVICE_USER, SESSION_SERVICE_PASSWORD", "cBioPortal session service and its basic authentication, when the\n    \tsession.service.url/user/password properties are unset"},
	{"CBIOPORTAL_PPROF_ADDR", "serve net/http/pprof on this address, e.g. 127.0.0.1:6060"},
	{"any, through ${NAME}", "portal property values may name environment variables, e.g.\n    \t--spring.data.redis.password='${REDIS_SESSION_PASSWORD}'"},
}

func allOptions() []option {
	var opts []option
	for _, s := range sections {
		opts = append(opts, s.opts...)
	}
	return opts
}

// settings is the parsed command line.
type settings struct {
	values map[string]string // resolved value of every option, by flag name
	// sources says where each value came from: "flag", "env $NAME" or
	// "default"; an unset option has none.
	sources     map[string]string
	portalArgs  []string // --key=value portal property arguments, in order
	showVersion bool
}

// goSettings returns the values of go's names for portal properties
// (config.Settings), the secret ones from the environment.
func (c *settings) goSettings() map[string]string {
	out := map[string]string{}
	for _, st := range config.AllSettings() {
		out[st.Name] = c.values[st.Name]
	}
	return out
}

// parseArgs resolves every option (flag > environment > built-in default)
// and separates the portal property arguments: any --key=value whose key is
// not a server flag is a portal property, so the Java portal's argument
// list can be passed unchanged. -h/-help returns flag.ErrHelp.
func parseArgs(args []string, getenv func(string) string) (*settings, error) {
	fs := flag.NewFlagSet("cbioportal-api", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	strs := map[string]*string{}
	bools := map[string]*bool{}
	for _, o := range allOptions() {
		def := o.def
		if v := getenv(o.env); v != "" {
			def = v
		}
		if o.isBool {
			bools[o.name] = fs.Bool(o.name, strings.EqualFold(def, "true"), o.usage)
		} else {
			strs[o.name] = fs.String(o.name, def, o.usage)
		}
	}
	cfg := &settings{values: map[string]string{}, sources: map[string]string{}}
	fs.BoolVar(&cfg.showVersion, "version", false, "print the version and exit")

	flagArgs, portalArgs, err := splitArgs(args, fs)
	if err != nil {
		return nil, err
	}
	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		if slices.Contains(commandNames, fs.Arg(0)) {
			return nil, fmt.Errorf("the command %q must come first: cbioportal-api %s [flags]", fs.Arg(0), fs.Arg(0))
		}
		return nil, fmt.Errorf("unexpected argument %q (portal properties are written --key=value)", fs.Arg(0))
	}
	for name, p := range strs {
		cfg.values[name] = *p
	}
	for name, p := range bools {
		cfg.values[name] = fmt.Sprint(*p)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for _, o := range allOptions() {
		switch {
		case set[o.name]:
			cfg.sources[o.name] = "flag"
		case getenv(o.env) != "":
			cfg.sources[o.name] = "env $" + o.env
		case o.def != "":
			cfg.sources[o.name] = "default"
		}
	}
	for _, st := range config.AllSettings() {
		if v := getenv(st.Env); st.Secret && v != "" {
			cfg.values[st.Name], cfg.sources[st.Name] = v, "env $"+st.Env
		}
	}
	cfg.portalArgs = portalArgs
	return cfg, nil
}

// flagNames lists every flag, for suggestions.
func flagNames() []string {
	names := []string{"version", "help"}
	for _, o := range allOptions() {
		names = append(names, o.name)
	}
	return names
}

// splitArgs separates server flags (with their values) from portal property
// arguments. A flag is a server flag if fs defines it; it then consumes the
// next argument as its value unless it is boolean or written name=value.
func splitArgs(args []string, fs *flag.FlagSet) (flagArgs, portalArgs []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || a == "-" || !strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, args[i:]...)
			break
		}
		body := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		name, _, hasValue := strings.Cut(body, "=")
		if name == "h" || name == "help" {
			return nil, nil, flag.ErrHelp
		}
		if f := fs.Lookup(name); f != nil {
			flagArgs = append(flagArgs, a)
			if !hasValue && !isBoolFlag(f) && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		if strings.HasPrefix(a, "--") && hasValue && name != "" {
			// A near miss of a flag (--prot=8080) is a typo, not a portal
			// property: no property this server knows is that close to one.
			if s := cli.Suggest(name, flagNames()); s != "" && !strings.Contains(name, ".") && !config.KnownProperty(name) {
				return nil, nil, fmt.Errorf("unknown flag --%s%s", name, cli.DidYouMean("--"+s))
			}
			portalArgs = append(portalArgs, a)
			continue
		}
		if s := cli.Suggest(name, flagNames()); s != "" {
			return nil, nil, fmt.Errorf("unknown flag %s%s", a, cli.DidYouMean("--"+s))
		}
		return nil, nil, fmt.Errorf("unknown flag %s (portal properties are written --key=value)", a)
	}
	return flagArgs, portalArgs, nil
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

const usageHeader = `Usage: cbioportal-api [command] [flags] [--<portal.property>=<value> ...]

cbioportal-api serves the cBioPortal REST API, and optionally the web
frontend, from the ClickHouse tables built by cbioportal-import.

Commands (the command comes first; without one the server serves):
  serve     serve the API (the default)
  config    print the effective settings and where each comes from;
            secrets are shown as (redacted). --json for JSON
  check     check the settings and reach ClickHouse, the sign-in providers
            and the login session store; exit status 1 when a check fails.
            --json for JSON
  version   print the version (--json for JSON)
  help      print this help

Each flag falls back to its environment variable, then to the built-in
default: flag > environment > default. Flags may be written -name or --name,
with the value after '=' or as the next argument. The login flags are go's
names for the Java portal's Spring properties, which work too
(docs/configuration.md lists both).

Exit status: 0 success, 1 failure, 2 invalid command line or settings.
`

const usageFooter = `Examples:
  cbioportal-api -db cbioportal -frontend-dir frontend/dist
  cbioportal-api --port=8080 --skin.title="My portal" --quick_search.enabled=true
  cbioportal-api --portal-properties=./application.properties
  cbioportal-api --login=optional --login-google-client-id=<id>   (secret in $CBIOPORTAL_LOGIN_GOOGLE_CLIENT_SECRET)
  cbioportal-api check --json --db cbioportal
  PORT=8080 CBIOPORTAL_DB=cbioportal cbioportal-api
`

func printUsage(w io.Writer) {
	fmt.Fprint(w, usageHeader+"\n")
	line := func(head, body string) {
		fmt.Fprintf(w, "  %s\n    \t%s\n", head, body)
	}
	for _, s := range sections {
		fmt.Fprintf(w, "%s:\n", s.title)
		for _, o := range s.opts {
			head := "-" + o.name
			if o.arg != "" {
				head += " " + o.arg
			}
			src := "$" + o.env
			if o.def != "" {
				src += ", default " + o.def
			}
			line(head, o.usage+" ("+src+")")
		}
		switch s.title {
		case "Server":
			line("-version", "print the version and exit")
		case "Portal properties":
			line("--<key>=<value>",
				"any other --key=value argument is a portal property (skin.title,\n    \tpriority_studies, quick_search.enabled, ...), exactly as given to the\n    \tJava portal; it overrides the file. Keys the server does not use (e.g.\n    \tspring.datasource.*) are ignored and listed at startup.")
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "Environment only (secrets are not flags: flags show in the process list, ps):")
	for _, e := range envOnly {
		line(e[0], e[1])
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, usageFooter)
}

// bulkLimits reads the bulk endpoints' query limits from the settings.
func bulkLimits(v map[string]string) (core.BulkLimits, error) {
	var l core.BulkLimits
	var err error
	if l.MaxMemory, err = parseSize(v["clickhouse-bulk-max-memory"]); err != nil {
		return l, fmt.Errorf("-clickhouse-bulk-max-memory: %w", err)
	}
	if l.SortSpill, err = parseSize(v["clickhouse-bulk-sort-spill"]); err != nil {
		return l, fmt.Errorf("-clickhouse-bulk-sort-spill: %w", err)
	}
	if l.MaxTime, err = parseSeconds(v["clickhouse-bulk-max-time"]); err != nil {
		return l, fmt.Errorf("-clickhouse-bulk-max-time: %w", err)
	}
	return l, nil
}

// parseSize reads a byte count: a number of bytes, or one with a KiB, MiB,
// GiB or TiB suffix (K, M, G, T, KB, MB, GB and TB mean the same).
func parseSize(s string) (int64, error) {
	t := strings.TrimSpace(s)
	num := strings.TrimRightFunc(t, func(r rune) bool { return r < '0' || r > '9' })
	unit := strings.ToUpper(strings.TrimSpace(t[len(num):]))
	shift, ok := map[string]uint{"": 0, "B": 0, "K": 10, "KB": 10, "KIB": 10, "M": 20, "MB": 20, "MIB": 20,
		"G": 30, "GB": 30, "GIB": 30, "T": 40, "TB": 40, "TIB": 40}[unit]
	n, err := strconv.ParseInt(num, 10, 64)
	if !ok || err != nil || n < 0 || n > math.MaxInt64>>shift {
		return 0, fmt.Errorf("invalid size %q (e.g. 10GiB, 512MiB, or bytes)", s)
	}
	return n << shift, nil
}

// parseSeconds reads a duration: a Go duration (10m, 90s) or a number of
// seconds.
func parseSeconds(s string) (time.Duration, error) {
	t := strings.TrimSpace(s)
	if n, err := strconv.ParseInt(t, 10, 64); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(t)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid duration %q (e.g. 10m, or seconds)", s)
	}
	return d, nil
}
