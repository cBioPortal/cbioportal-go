// Package mirror rebuilds go's databases from a zero-copy clone of a Java
// cBioPortal production database, so go serves production's data while the
// migration from Java runs (go.cbioportal.org, and the `prod` data of PR
// previews).
//
// A run, by default:
//
//  1. reads the live production color and its last import from
//     <MGMT_DB>.update_status; it stops without changes while an import is
//     running, or when the served color was built from that import already
//     (so it can run hourly and follow each production switch);
//  2. picks the go color that is not being served (ACTIVE_DB is the served
//     database; anything else bootstraps into blue);
//  3. empties that color's databases and CLONEs every production table but
//     the auth and credential tables into <PREFIX>_<color>_ref;
//  4. builds the portal database <PREFIX>_<color> from the clone with the
//     from-prod transform;
//  5. records the production snapshot it was built from in
//     <PREFIX>_<color>_ref._go_mirror_source and writes <WORKDIR>/state.env
//     (CBIOPORTAL_DB, CBIOPORTAL_RAW_DB, CBIOPORTAL_REF_DB) for the step that
//     switches the API over. When nothing is rebuilt, state.env names the
//     served databases, so that step changes nothing.
//
// SRC_DB clones that database instead of the live production color (no
// update_status is read); TARGET_DB builds into TARGET_DB, its _raw and its
// _ref unconditionally, instead of the inactive color.
package mirror

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

// Config is a mirror run's settings.
type Config struct {
	URL            string
	Prefix         string // the blue/green databases: <Prefix>_blue, <Prefix>_green
	ActiveDB       string // the database served now
	SrcDB          string // fixed source, instead of the live production color
	TargetDB       string // fixed target, instead of the inactive color
	Workdir        string // state.env goes here
	MgmtDB         string // holds update_status
	SrcPrefix      string // production's color databases: <SrcPrefix>blue, <SrcPrefix>green
	PortalDatabase string // the update_status row
}

// ConfigFromEnv reads the Config from the environment the CronJob sets.
func ConfigFromEnv() Config {
	return Config{
		URL:            os.Getenv("CLICKHOUSE_URL"),
		Prefix:         envOr("PREFIX", "dev_cbioportal_public_go"),
		ActiveDB:       os.Getenv("ACTIVE_DB"),
		SrcDB:          os.Getenv("SRC_DB"),
		TargetDB:       os.Getenv("TARGET_DB"),
		Workdir:        envOr("WORKDIR", "/work"),
		MgmtDB:         envOr("MGMT_DB", "publicdb_update_management_database"),
		SrcPrefix:      envOr("SRC_PREFIX", "cbioportal_public_"),
		PortalDatabase: envOr("PORTAL_DATABASE", "public"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Source is the production snapshot a run would clone.
type Source struct {
	DB       string
	Status   string // update_process_status; "complete" when idle
	Snapshot string // identifies the import: "<color> <completed at>", or the fixed SrcDB
}

// Plan is what a run does.
type Plan struct {
	Build            bool   // false: keep the served databases
	Reason           string // why
	DB, RawDB, RefDB string // the databases to build (or, when not building, those served)
}

// Decide plans a run from the source and the snapshot the served databases
// were built from (builtFrom, "" when unknown).
func Decide(cfg Config, src Source, builtFrom string) Plan {
	if cfg.TargetDB != "" {
		return Plan{Build: true, Reason: "building into " + cfg.TargetDB, DB: cfg.TargetDB, RawDB: cfg.TargetDB + "_raw", RefDB: cfg.TargetDB + "_ref"}
	}
	blue, green := cfg.Prefix+"_blue", cfg.Prefix+"_green"
	color := blue
	switch cfg.ActiveDB {
	case blue, green:
		keep := Plan{DB: cfg.ActiveDB, RawDB: cfg.ActiveDB + "_raw", RefDB: cfg.ActiveDB + "_ref"}
		if src.Status != "complete" {
			keep.Reason = fmt.Sprintf("production import in progress (status=%s)", src.Status)
			return keep
		}
		if builtFrom == src.Snapshot {
			keep.Reason = "already built from " + src.Snapshot
			return keep
		}
		if cfg.ActiveDB == blue {
			color = green
		}
		p := Plan{Build: true, DB: color, RawDB: color + "_raw", RefDB: color + "_ref"}
		p.Reason = fmt.Sprintf("served databases built from '%s'; rebuilding from %s into %s", orUnknown(builtFrom), src.Snapshot, color)
		return p
	}
	return Plan{Build: true, DB: color, RawDB: color + "_raw", RefDB: color + "_ref",
		Reason: fmt.Sprintf("active='%s': bootstrapping into %s", cfg.ActiveDB, color)}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// The mirror is served publicly, so it carries no auth or credential data:
// an explicit list for today's schema plus substrings for future tables.
var (
	excludedTables     = []string{"authorities", "data_access_tokens", "users"}
	excludedSubstrings = []string{"_backup", "token", "oauth", "session", "login", "password", "credential", "secret"}
)

// Cloned reports whether a production table is cloned.
func Cloned(table string) bool {
	if slices.Contains(excludedTables, table) {
		return false
	}
	lower := strings.ToLower(table)
	for _, s := range excludedSubstrings {
		if strings.Contains(lower, s) {
			return false
		}
	}
	return true
}

// Execer runs a statement; *clickhouse.Client is one.
type Execer interface {
	ExecuteServerwide(sql string) (string, error)
}

// retrying runs statements again after transient failures (DNS,
// connections, timeouts); every statement of a run is safe to repeat.
type retrying struct {
	c     Execer
	tries int
	delay time.Duration
	logf  func(string, ...any)
}

func (r retrying) query(sql string) (string, error) {
	var err error
	for i := 0; i < r.tries; i++ {
		if i > 0 {
			r.logf("retrying after: %v", err)
			time.Sleep(r.delay)
		}
		var out string
		if out, err = r.c.ExecuteServerwide(sql); err == nil {
			return strings.TrimRight(out, "\n"), nil
		}
	}
	return "", err
}

// Runner is a mirror run.
type Runner struct {
	Cfg Config
	C   Execer
	// Build makes the portal database from the clone; nil runs the
	// from-prod transform.
	Build func(db, rawDB, refDB string) error
	Logf  func(format string, args ...any)
	// RetryDelay is the wait before repeating a failed statement.
	RetryDelay time.Duration
}

// Run performs one mirror run and returns its plan.
func (r *Runner) Run(ctx context.Context) (Plan, error) {
	q := retrying{c: r.C, tries: 6, delay: r.RetryDelay, logf: r.Logf}

	// 1) Production color and the import it serves.
	var src Source
	if r.Cfg.SrcDB != "" {
		src = Source{DB: r.Cfg.SrcDB, Status: "complete", Snapshot: r.Cfg.SrcDB}
		r.Logf("source %s", r.Cfg.SrcDB)
	} else {
		out, err := q.query(fmt.Sprintf("SELECT current_database_in_production, update_process_status, toString(time_of_last_update_process_completion) FROM `%s`.update_status WHERE portal_database = '%s' FORMAT TSV",
			r.Cfg.MgmtDB, ch.SQLLit(r.Cfg.PortalDatabase)))
		if err != nil {
			return Plan{}, err
		}
		f := strings.Split(out, "\t")
		if len(f) != 3 || (f[0] != "blue" && f[0] != "green") {
			return Plan{}, fmt.Errorf("unexpected production status %q", out)
		}
		src = Source{DB: r.Cfg.SrcPrefix + f[0], Status: f[1], Snapshot: f[0] + " " + f[2]}
		r.Logf("production color=%s, status=%s, last import completed %s", f[0], f[1], f[2])
	}

	builtFrom := ""
	if r.Cfg.TargetDB == "" && (r.Cfg.ActiveDB == r.Cfg.Prefix+"_blue" || r.Cfg.ActiveDB == r.Cfg.Prefix+"_green") && src.Status == "complete" {
		n, err := q.query(fmt.Sprintf("SELECT count() FROM system.tables WHERE database = '%s_ref' AND name = '_go_mirror_source' FORMAT TSV", ch.SQLLit(r.Cfg.ActiveDB)))
		if err != nil {
			return Plan{}, err
		}
		if n == "1" {
			if builtFrom, err = q.query(fmt.Sprintf("SELECT any(snapshot) FROM `%s_ref`._go_mirror_source FORMAT TSV", r.Cfg.ActiveDB)); err != nil {
				return Plan{}, err
			}
		}
	}
	plan := Decide(r.Cfg, src, builtFrom)
	r.Logf("%s", plan.Reason)
	if !plan.Build {
		return plan, r.writeState(plan)
	}

	// 3) Empty the target databases (tables and views; the databases stay,
	//    since the job's user may not create or drop databases), then clone.
	for _, d := range []string{plan.DB, plan.RawDB, plan.RefDB} {
		for _, kind := range []string{"VIEW", "TABLE"} {
			op := "="
			if kind == "TABLE" {
				op = "!="
			}
			names, err := q.query(fmt.Sprintf("SELECT name FROM system.tables WHERE database = '%s' AND engine %s 'View' FORMAT TSV", ch.SQLLit(d), op))
			if err != nil {
				return plan, err
			}
			for _, name := range lines(names) {
				stmt := fmt.Sprintf("DROP %s IF EXISTS `%s`.`%s`", kind, d, name)
				if kind == "TABLE" {
					stmt += " SYNC"
				}
				if _, err := q.query(stmt); err != nil {
					return plan, err
				}
			}
		}
	}
	names, err := q.query(fmt.Sprintf("SELECT name FROM system.tables WHERE database = '%s' AND engine LIKE '%%MergeTree' ORDER BY name FORMAT TSV", ch.SQLLit(src.DB)))
	if err != nil {
		return plan, err
	}
	var tables []string
	for _, t := range lines(names) {
		if Cloned(t) {
			tables = append(tables, t)
		}
	}
	r.Logf("cloning %d tables from %s into %s", len(tables), src.DB, plan.RefDB)
	for i, t := range tables {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		if err := r.clone(q, src.DB, plan.RefDB, t); err != nil {
			return plan, err
		}
		r.Logf("  [%d/%d] %s", i+1, len(tables), t)
	}

	// 4) Portal database from the production schema.
	r.Logf("building %s from %s", plan.DB, plan.RefDB)
	build := r.Build
	if build == nil {
		build = func(db, rawDB, refDB string) error {
			_, err := transform.ApplyFromProd(ch.New(r.Cfg.URL, refDB), refDB, rawDB, db)
			return err
		}
	}
	if err := build(plan.DB, plan.RawDB, plan.RefDB); err != nil {
		return plan, err
	}
	samples, err := q.query(fmt.Sprintf("SELECT count() FROM `%s`.sample_derived FORMAT TSV", plan.DB))
	if err != nil {
		return plan, err
	}
	if samples == "0" || samples == "" {
		return plan, fmt.Errorf("%s has no samples", plan.DB)
	}
	r.Logf("sanity check: %s samples in %s", samples, plan.DB)

	// 5) Record the snapshot, then hand over to the step that switches the
	//    API to the new databases.
	if _, err := q.query(fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s`._go_mirror_source (snapshot String, built_at DateTime DEFAULT now()) ENGINE = MergeTree ORDER BY tuple()", plan.RefDB)); err != nil {
		return plan, err
	}
	if _, err := q.query(fmt.Sprintf("INSERT INTO `%s`._go_mirror_source (snapshot) VALUES ('%s')", plan.RefDB, ch.SQLLit(src.Snapshot))); err != nil {
		return plan, err
	}
	if err := r.writeState(plan); err != nil {
		return plan, err
	}
	r.Logf("done: CBIOPORTAL_DB=%s", plan.DB)
	return plan, nil
}

// clone makes dst.table a zero-copy clone of src.table. CLONE AS creates
// the table and then attaches the source's parts, so a failed attempt can
// leave an empty table behind: each attempt starts by dropping it. The
// statement runs with the source as the session database, where some
// ClickHouse versions look for the parts whatever the qualifier says.
func (r *Runner) clone(q retrying, src, dst, table string) error {
	c := ch.New(r.Cfg.URL, src)
	var err error
	for i := 0; i < q.tries; i++ {
		if i > 0 {
			r.Logf("retrying after: %v", err)
			time.Sleep(q.delay)
		}
		if _, err = c.Execute(fmt.Sprintf("DROP TABLE IF EXISTS `%s`.`%s` SYNC", dst, table)); err != nil {
			continue
		}
		if _, err = c.Execute(fmt.Sprintf("CREATE TABLE `%s`.`%s` CLONE AS `%s`.`%s`", dst, table, src, table)); err == nil {
			return nil
		}
	}
	return err
}

func lines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// writeState writes <Workdir>/state.env with the databases to serve.
func (r *Runner) writeState(p Plan) error {
	if err := os.MkdirAll(r.Cfg.Workdir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.Cfg.Workdir, "state.env"),
		[]byte(fmt.Sprintf("CBIOPORTAL_DB=%s\nCBIOPORTAL_RAW_DB=%s\nCBIOPORTAL_REF_DB=%s\n", p.DB, p.RawDB, p.RefDB)), 0o644)
}

// Main is `cbioportal-rewrite-test java-v7-to-go mirror`.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mirror", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: cbioportal-rewrite-test java-v7-to-go mirror

Rebuilds go's databases from a zero-copy clone of Java cBioPortal's
production database (see docs/deployment.md, "Mirroring production").
Settings come from the environment:

  CLICKHOUSE_URL, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD
  PREFIX           go's blue/green databases (dev_cbioportal_public_go)
  ACTIVE_DB        the database served now
  WORKDIR          state.env goes here (/work)
  MGMT_DB          holds update_status (publicdb_update_management_database)
  SRC_PREFIX       production's color databases (cbioportal_public_)
  PORTAL_DATABASE  the update_status row (public)
  SRC_DB           clone this database instead of the live production color
  TARGET_DB        build into TARGET_DB, its _raw and its _ref instead of
                   the inactive color
`)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	cfg := ConfigFromEnv()
	if cfg.URL == "" {
		fmt.Fprintln(stderr, "mirror: CLICKHOUSE_URL is not set")
		return 2
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(stdout, "[mirror %s] %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
	}
	r := &Runner{Cfg: cfg, C: ch.New(cfg.URL, "default"), Logf: logf, RetryDelay: 10 * time.Second}
	if _, err := r.Run(context.Background()); err != nil {
		logf("FAIL: %v", err)
		return 1
	}
	return 0
}
