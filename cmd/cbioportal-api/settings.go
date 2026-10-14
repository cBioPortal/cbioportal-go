package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/cbioportal/cbioportal-go/internal/api"
	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/cli"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

// settings are cbioportal-api's settings. Each is a flag that falls back to
// an environment variable, then to its default; register declares them all
// with their help. The login settings are go's names for the Java portal's
// login properties (config.Settings), so they are kept by name.
type settings struct {
	port        int
	metricsAddr string

	clickhouseURL string
	logComment    string
	db            string
	rawDB         string
	refDB         string
	bulkMaxMemory byteSize
	bulkMaxTime   seconds
	bulkSortSpill byteSize

	// api holds the frontend, session and bearer-token settings, which the
	// API itself uses.
	api api.Options

	portalProperties string
	// portalArgs are the --key=value portal property arguments, in order.
	portalArgs []string

	// login is the value of each non-secret login setting, by name.
	login map[string]*string
	// getenv reads the environment: the secret login settings come from it.
	getenv func(string) string
	// flags are the parsed flags, for each value's source.
	flags *pflag.FlagSet
}

// register declares the settings as flags of fs, grouped as the help
// shows them.
func (s *settings) register(fs *pflag.FlagSet) {
	s.flags = fs
	str := func(p *string, name, def, env, usage string) {
		fs.StringVar(p, name, def, usage)
		cli.Env(fs, name, env)
	}

	fs.IntVar(&s.port, "port", 8080, "listen port; the server binds all interfaces (0.0.0.0)")
	cli.Env(fs, "port", "PORT")
	str(&s.metricsAddr, "metrics-addr", "", "METRICS_ADDR", "serve Prometheus metrics at /metrics on this `address`, e.g. :9090 (apart from the API)")
	cli.Group(fs, "Server", "port", "metrics-addr")

	s.bulkMaxMemory = byteSize(core.DefaultBulkLimits.MaxMemory)
	s.bulkMaxTime = seconds(core.DefaultBulkLimits.MaxTime)
	s.bulkSortSpill = byteSize(core.DefaultBulkLimits.SortSpill)
	str(&s.clickhouseURL, "clickhouse-url", "http://localhost:18123", "CLICKHOUSE_URL", "ClickHouse HTTP `endpoint`, e.g. https://<host>:8443 for ClickHouse Cloud")
	str(&s.logComment, "clickhouse-log-comment", "", "CLICKHOUSE_LOG_COMMENT", "tag every ClickHouse query with this `text` (log_comment in system.query_log), e.g. the deployment's name")
	str(&s.db, "db", "cbioportal", "CBIOPORTAL_DB", "`database` built by cbioportal-import transform")
	str(&s.rawDB, "raw-db", "cbioportal_raw", "CBIOPORTAL_RAW_DB", "staging `database` written by cbioportal-import ingest")
	str(&s.refDB, "ref-db", "cbioportal_ref", "CBIOPORTAL_REF_DB", "reference `database` written by cbioportal-import load-reference")
	fs.Var(&s.bulkMaxMemory, "clickhouse-bulk-max-memory", "memory one query of a bulk endpoint (whole profiles of data, enrichments, export) may use; 0 for no limit")
	cli.Env(fs, "clickhouse-bulk-max-memory", "CLICKHOUSE_BULK_MAX_MEMORY")
	fs.Var(&s.bulkMaxTime, "clickhouse-bulk-max-time", "time one query of a bulk endpoint may run, streaming its result included; 0 for no limit")
	cli.Env(fs, "clickhouse-bulk-max-time", "CLICKHOUSE_BULK_MAX_TIME")
	fs.Var(&s.bulkSortSpill, "clickhouse-bulk-sort-spill", "memory a sort of a bulk endpoint may use before it sorts on disk; 0 keeps the server's setting")
	cli.Env(fs, "clickhouse-bulk-sort-spill", "CLICKHOUSE_BULK_SORT_SPILL")
	cli.Group(fs, "ClickHouse", "clickhouse-url", "clickhouse-log-comment", "db", "raw-db", "ref-db",
		"clickhouse-bulk-max-memory", "clickhouse-bulk-max-time", "clickhouse-bulk-sort-spill")

	str(&s.api.FrontendDir, "frontend-dir", "", "FRONTEND_DIR", "serve this frontend build `directory` for every non-/api/ path (single origin)")
	str(&s.api.StaticDir, "static-dir", "", "CBIOPORTAL_STATIC_DIR", "serve the files in this `directory` at their path, before the built-in ones, e.g. static/images/logo.png at /images/logo.png (a portal's logo or custom tab scripts)")
	str(&s.api.FrontendURL, "frontend-url", "", "FRONTEND_URL", "reverse-proxy every non-/api/ path to this frontend dev server `url` instead")
	str(&s.api.OncoKBURL, "oncokb-url", "https://public.api.oncokb.org/api/v1", "ONCOKB_URL", "OncoKB API `url` behind the frontend's /proxy/ endpoint")
	str(&s.api.SiteBanner, "site-banner", "", "SITE_BANNER", "notice bar `text` at the top of every portal page, e.g. for a preview deployment")
	str(&s.api.SiteBannerLink, "site-banner-link", "", "SITE_BANNER_LINK", "link `url` for the site banner")
	cli.Group(fs, "Frontend", "frontend-dir", "static-dir", "frontend-url", "oncokb-url", "site-banner", "site-banner-link")

	str(&s.api.SessionStoreFile, "session-store", "", "SESSION_STORE_FILE", "persist sessions (virtual studies, groups, custom data) to this JSON `file`; unset keeps them in memory (unused with the session.service.url property)")
	fs.BoolVar(&s.api.BearerAuth, "auth-enabled", false, "require HS256 bearer tokens signed with $AUTH_JWT_SECRET")
	cli.Env(fs, "auth-enabled", "AUTH_ENABLED")
	str(&s.api.BearerDefaultUser, "auth-default-user", "", "AUTH_DEFAULT_USER", "single-user development login: the `user` of requests without a token")
	fs.BoolVar(&s.api.AccessLog, "access-log", false, "write a JSON access log line per request to stdout")
	cli.Env(fs, "access-log", "ACCESS_LOG")
	cli.Group(fs, "Sessions and auth", "session-store", "auth-enabled", "auth-default-user", "access-log")

	s.login = map[string]*string{}
	for _, ss := range config.Settings {
		var names []string
		for _, st := range ss.Settings {
			if st.Secret {
				continue
			}
			s.login[st.Name] = fs.String(st.Name, "", strings.Join(strings.Fields(st.Usage), " "))
			cli.Env(fs, st.Name, st.Env)
			names = append(names, st.Name)
		}
		cli.Group(fs, ss.Title, names...)
	}

	str(&s.portalProperties, "portal-properties", "", "PORTAL_PROPERTIES", "application.properties or legacy portal.properties `file` (same key=value syntax)")
	cli.Group(fs, "Portal properties", "portal-properties")
}

// bulkLimits are the bulk endpoints' query limits.
func (s *settings) bulkLimits() core.BulkLimits {
	return core.BulkLimits{
		MaxMemory: int64(s.bulkMaxMemory),
		MaxTime:   time.Duration(s.bulkMaxTime),
		SortSpill: int64(s.bulkSortSpill),
	}
}

// loginValues returns the value of every login setting by name, the secret
// ones from the environment.
func (s *settings) loginValues() map[string]string {
	out := map[string]string{}
	for _, st := range config.AllSettings() {
		if st.Secret {
			out[st.Name] = s.getenv(st.Env)
		} else if p := s.login[st.Name]; p != nil {
			out[st.Name] = *p
		}
	}
	return out
}

// envOnly are settings deliberately without a flag: secrets, which a flag
// would show to every user through the process list (ps), and the profiler
// address, read before the flags are parsed.
func envOnly() [][2]string {
	out := [][2]string{
		{"CLICKHOUSE_USER, CLICKHOUSE_PASSWORD", "ClickHouse credentials (unset: the default user)"},
		{"ONCOKB_TOKEN", "OncoKB API token sent by the /proxy/ endpoint"},
		{"AUTH_JWT_SECRET", "HS256 secret that bearer tokens are verified with"},
		{"PUBLISHER_API_KEY", "key required by /api/public_virtual_studies publish/unpublish (or the session.endpoint.publisher-api-key property)"},
		{"SESSION_SERVICE_URL, SESSION_SERVICE_USER, SESSION_SERVICE_PASSWORD", "cBioPortal session service and its basic authentication, when the session.service.url/user/password properties are unset"},
		{"CBIOPORTAL_PPROF_ADDR", "serve net/http/pprof on this address, e.g. 127.0.0.1:6060"},
	}
	for _, st := range config.AllSettings() {
		if st.Secret {
			out = append(out, [2]string{st.Env, st.Usage})
		}
	}
	return append(out, [2]string{"any, through ${NAME}", "portal property values may name environment variables, e.g. --spring.data.redis.password='${REDIS_SESSION_PASSWORD}'"})
}

// byteSize is a flag value for a number of bytes: plain, or with a KiB,
// MiB, GiB or TiB suffix (K, M, G, T, KB, MB, GB and TB mean the same).
type byteSize int64

func (b *byteSize) Set(s string) error {
	n, err := parseSize(s)
	if err != nil {
		return err
	}
	*b = byteSize(n)
	return nil
}

func (b *byteSize) String() string {
	n := int64(*b)
	for _, u := range []struct {
		suffix string
		shift  uint
	}{{"TiB", 40}, {"GiB", 30}, {"MiB", 20}, {"KiB", 10}} {
		if n != 0 && n%(1<<u.shift) == 0 {
			return strconv.FormatInt(n>>u.shift, 10) + u.suffix
		}
	}
	return strconv.FormatInt(n, 10)
}

func (b *byteSize) Type() string { return "size" }

// parseSize reads a byteSize.
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

// seconds is a flag value for a duration: a Go duration (10m, 90s) or a
// number of seconds.
type seconds time.Duration

func (d *seconds) Set(s string) error {
	t := strings.TrimSpace(s)
	if n, err := strconv.ParseInt(t, 10, 64); err == nil && n >= 0 {
		*d = seconds(time.Duration(n) * time.Second)
		return nil
	}
	v, err := time.ParseDuration(t)
	if err != nil || v < 0 {
		return fmt.Errorf("invalid duration %q (e.g. 10m, or seconds)", s)
	}
	*d = seconds(v)
	return nil
}

func (d *seconds) String() string {
	s := time.Duration(*d).String()
	if s == "0s" {
		return "0"
	}
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func (d *seconds) Type() string { return "duration" }
