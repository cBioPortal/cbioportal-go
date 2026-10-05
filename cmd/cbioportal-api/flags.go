package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
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
	}},
	{"Frontend", []option{
		{name: "frontend-dir", arg: "dir", env: "FRONTEND_DIR", usage: "serve this frontend build for every non-/api/ path (single origin)", export: true},
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
	values      map[string]string // resolved value of every option, by flag name
	portalArgs  []string          // --key=value portal property arguments, in order
	showVersion bool
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
	cfg := &settings{values: map[string]string{}}
	fs.BoolVar(&cfg.showVersion, "version", false, "print the version and exit")

	flagArgs, portalArgs, err := splitArgs(args, fs)
	if err != nil {
		return nil, err
	}
	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q (portal properties are written --key=value)", fs.Arg(0))
	}
	for name, p := range strs {
		cfg.values[name] = *p
	}
	for name, p := range bools {
		cfg.values[name] = fmt.Sprint(*p)
	}
	cfg.portalArgs = portalArgs
	return cfg, nil
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
			portalArgs = append(portalArgs, a)
			continue
		}
		return nil, nil, fmt.Errorf("unknown flag %s (portal properties are written --key=value)", a)
	}
	return flagArgs, portalArgs, nil
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

const usageHeader = `Usage: cbioportal-api [flags] [--<portal.property>=<value> ...]

cbioportal-api serves the cBioPortal REST API, and optionally the web
frontend, from the ClickHouse tables built by cbioportal-import.

Each flag falls back to its environment variable, then to the built-in
default: flag > environment > default. Flags may be written -name or --name,
with the value after '=' or as the next argument.
`

const usageFooter = `Examples:
  cbioportal-api -db cbioportal -frontend-dir frontend/dist
  cbioportal-api --port=8080 --skin.title="My portal" --quick_search.enabled=true
  cbioportal-api --portal-properties=./application.properties
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
