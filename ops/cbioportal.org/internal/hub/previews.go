package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/studyimport"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/github"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/job"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/preview"
)

// DefaultHubBase is hub.cbioportal.org's address (HUB_BASE).
const DefaultHubBase = "https://hub.cbioportal.org"

// HubLockTable is the import lock in a raw database of the hub or of a
// preview: one import of it at a time.
const HubLockTable = "_hub_import_lock"

// HubDatabases are hub.cbioportal.org's databases: HUB_DB (default
// dev_cbioportal_hub_go), HUB_RAW_DB and HUB_REF_DB (default HUB_DB plus
// _raw and _ref).
func HubDatabases() preview.Databases {
	db := cli.EnvOr("HUB_DB", "dev_cbioportal_hub_go")
	return preview.Databases{DB: db, Raw: cli.EnvOr("HUB_RAW_DB", db+"_raw"), Ref: cli.EnvOr("HUB_REF_DB", db+"_ref")}
}

// The modes of a pull request's preview, set with a `hub-preview:` line in
// its description.
const (
	// ModePRStudies: the reference data and the studies the pull request
	// changes (the default).
	ModePRStudies = "pr-studies"
	// ModePRStudiesPlusHub: a copy of every study on hub.cbioportal.org,
	// with the pull request's studies imported on top.
	ModePRStudiesPlusHub = "pr-studies-plus-hub"
)

// PreviewRequest is what a pull request's description asks of its preview.
type PreviewRequest struct {
	Mode string
	// Only, when set, limits the import to these of the pull request's
	// studies (datahub paths such as public/brca_tcga).
	Only []string
}

// ParsePreviewRequest reads the `hub-preview:` line of a pull request
// description and the list of studies right below it:
//
//	hub-preview: pr-studies
//	- public/brca_tcga
//
// Without the line the mode is ModePRStudies. Text inside HTML comments
// is ignored, so a commented example in a template does nothing. An
// unknown mode is an error, and the mode is then ModePRStudies.
func ParsePreviewRequest(body string) (PreviewRequest, error) {
	st := PreviewRequest{Mode: ModePRStudies}
	lines := strings.Split(preview.StripComments(strings.ReplaceAll(body, "\r", "")), "\n")
	for i, line := range lines {
		v, ok := strings.CutPrefix(line, "hub-preview:")
		if !ok {
			continue
		}
		for _, item := range lines[i+1:] {
			if len(item) < 2 || item[0] != '-' || (item[1] != ' ' && item[1] != '\t') {
				break
			}
			if item = strings.Trim(strings.TrimSpace(item[1:]), "`/"); item != "" {
				st.Only = append(st.Only, item)
			}
		}
		if i := strings.IndexByte(v, '#'); i >= 0 {
			v = v[:i]
		}
		switch v = strings.TrimSpace(v); v {
		case "", ModePRStudies:
		case ModePRStudiesPlusHub:
			st.Mode = ModePRStudiesPlusHub
		default:
			return st, fmt.Errorf("unknown `hub-preview: %s` (%s or %s)", v, ModePRStudies, ModePRStudiesPlusHub)
		}
		return st, nil
	}
	return st, nil
}

// PreviewEnvironment is the GitHub Deployment environment of pull request
// n's preview.
func PreviewEnvironment(n int) string { return fmt.Sprintf("datahub-pr-%d-preview", n) }

// prepareDatabases makes sure the preview's databases and import log
// exist, and returns the mode of the preview's last complete import ("":
// none). A preview built in another mode is dropped first, so it is built
// again from empty.
func (r *run) prepareDatabases(mode string) (string, error) {
	last, err := r.ensureDatabases()
	if err != nil || last == "" || last == mode {
		return last, err
	}
	job.Logf("the preview was built with hub-preview: %s; starting again from empty", last)
	for _, d := range []string{r.DB, r.Raw, r.Ref} {
		if _, err := r.ch.ExecuteServerwide(fmt.Sprintf("DROP DATABASE IF EXISTS `%s` SYNC", d)); err != nil {
			return "", err
		}
	}
	return r.ensureDatabases()
}

func (r *run) ensureDatabases() (string, error) {
	for _, d := range []string{r.DB, r.Raw, r.Ref} {
		if err := r.ch.EnsureDatabase(d); err != nil {
			return "", fmt.Errorf("cannot reach ClickHouse: %w", err)
		}
	}
	if _, err := r.ch.ExecuteServerwide("CREATE TABLE IF NOT EXISTS " + r.importLog() +
		" (pr UInt32, sha String, mode String, conclusion String, finished DateTime) ENGINE = MergeTree ORDER BY (pr, sha)"); err != nil {
		return "", fmt.Errorf("cannot reach ClickHouse: %w", err)
	}
	return job.Query(r.ch, fmt.Sprintf("SELECT argMax(mode, finished) FROM %s FORMAT TSV", r.importLog()))
}

// copyHub copies hub.cbioportal.org's staged studies, reference data and
// built portal into the preview, while holding the hub's import lock so
// the copy is of one state of the hub. The preview's own import then
// rebuilds only the pull request's studies on top of the copy.
func (r *run) copyHub(ctx context.Context) error {
	lock := &studyimport.Lock{C: r.ch, DB: r.Hub.Raw, Table: r.HubLockTable, Owner: r.Owner + "-copy",
		Wait: r.LockWait, Stale: r.LockStale, Poll: r.LockPoll, Heartbeat: r.LockHeartbeat, Logf: job.Logf}
	if err := lock.Acquire(ctx); err != nil {
		return fmt.Errorf("taking the hub's import lock: %w", err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			job.Logf("%v", err)
		}
	}()
	return CopyDatabases(r.ch, r.Hub, preview.Databases{DB: r.DB, Raw: r.Raw, Ref: r.Ref})
}

// CopyDatabases copies the tables and views of from's raw, reference and
// portal databases into to's, replacing tables of the same name. ClickHouse
// attaches a table's data to the copy without rewriting it (ATTACH
// PARTITION ALL FROM): all of datahub (about 130 million rows) takes about
// 20 seconds on ClickHouse Cloud.
// The hub's own bookkeeping (the import lock, log and master sync state)
// and the staged tables of an unfinished build are not copied. The views
// are created again over to's databases. The copied portal keeps the
// fingerprint of the hub's build, so an import into it rebuilds only the
// studies it changes.
func CopyDatabases(c *ch.Client, from, to preview.Databases) error {
	rename := strings.NewReplacer(from.Raw+".", to.Raw+".", from.Ref+".", to.Ref+".", from.DB+".", to.DB+".")
	for _, pair := range [][2]string{{from.Raw, to.Raw}, {from.Ref, to.Ref}, {from.DB, to.DB}} {
		src, dst := pair[0], pair[1]
		text, err := c.Execute(fmt.Sprintf("SELECT name, engine, create_table_query FROM system.tables "+
			"WHERE database = '%s' AND NOT startsWith(name, '_hub_') AND NOT startsWith(name, '__next_') AND name != '_import_lock' "+
			"ORDER BY name FORMAT JSONEachRow", ch.SQLLit(src)))
		if err != nil {
			return err
		}
		var views []string
		n := 0
		for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
			if line == "" {
				continue
			}
			var t struct {
				Name   string `json:"name"`
				Engine string `json:"engine"`
				Create string `json:"create_table_query"`
			}
			if err := json.Unmarshal([]byte(line), &t); err != nil {
				return err
			}
			if t.Engine == "View" {
				views = append(views, "CREATE OR REPLACE VIEW"+strings.TrimPrefix(rename.Replace(t.Create), "CREATE VIEW"))
				continue
			}
			for _, q := range []string{
				fmt.Sprintf("DROP TABLE IF EXISTS `%s`.`%s` SYNC", dst, t.Name),
				fmt.Sprintf("CREATE TABLE `%s`.`%s` AS `%s`.`%s`", dst, t.Name, src, t.Name),
				fmt.Sprintf("ALTER TABLE `%s`.`%s` ATTACH PARTITION ALL FROM `%s`.`%s`", dst, t.Name, src, t.Name),
			} {
				if _, err := c.Execute(q); err != nil {
					return fmt.Errorf("copying %s.%s: %w", src, t.Name, err)
				}
			}
			n++
		}
		if err := createViews(c, views); err != nil {
			return err
		}
		job.Logf("copied %d tables and %d views of %s into %s", n, len(views), src, dst)
	}
	return nil
}

// createViews creates views that may read each other: a view whose
// source does not exist yet is tried again after the others.
func createViews(c *ch.Client, views []string) error {
	for len(views) > 0 {
		var left []string
		var lastErr error
		for _, q := range views {
			if _, err := c.Execute(q); err != nil {
				left, lastErr = append(left, q), err
			}
		}
		if len(left) == len(views) {
			return fmt.Errorf("creating views: %w", lastErr)
		}
		views = left
	}
	return nil
}

// PreviewSettings are the settings of removing previews: HUB_PR_DB_PREFIX
// and, for gc, the label a pull request's preview needs.
type PreviewSettings struct {
	Prefix string
	Label  string
	DryRun bool
}

// PreviewSettingsFromEnv reads the PreviewSettings.
func PreviewSettingsFromEnv() *PreviewSettings {
	return &PreviewSettings{Prefix: cli.EnvOr("HUB_PR_DB_PREFIX", DefaultPRPrefix), Label: cli.EnvOr("PREVIEW_LABEL", "preview")}
}

// Teardown drops the databases of datahub pull request n's preview and
// marks its Deployments inactive; with st.DryRun it only logs what it
// would do.
func Teardown(ctx context.Context, st *PreviewSettings, gh *github.Client, n int) error {
	dbs := preview.DatabasesFor(st.Prefix, n).All()
	if st.DryRun {
		for _, d := range dbs {
			job.Logf("would drop %s", d)
		}
		job.Logf("would mark the deployments of datahub#%d inactive", n)
		return nil
	}
	c := ch.New(job.ClickHouseURL(), "default")
	var errs []error
	for _, d := range dbs {
		if _, err := c.ExecuteServerwide(fmt.Sprintf("DROP DATABASE IF EXISTS `%s` SYNC", d)); err != nil {
			errs = append(errs, err)
			continue
		}
		job.Logf("dropped %s", d)
	}
	if err := gh.DeactivateEnvironment(ctx, DatahubRepo, PreviewEnvironment(n)); err != nil {
		job.Logf("WARN: could not mark the deployments of datahub#%d inactive: %v", n, err)
	}
	return errors.Join(errs...)
}

// GC tears down the preview of every datahub pull request that is closed
// or no longer has the preview label. A pull request that cannot be read
// is left alone.
func GC(ctx context.Context, st *PreviewSettings, gh *github.Client) error {
	c := ch.New(job.ClickHouseURL(), "default")
	names, err := job.Query(c, fmt.Sprintf("SELECT name FROM system.databases WHERE startsWith(name, '%s') FORMAT TSV", ch.SQLLit(st.Prefix)))
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range preview.PRNumbers(st.Prefix, strings.Split(names, "\n")) {
		pr, err := gh.PR(ctx, DatahubRepo, n)
		state, labelled := "", false
		if err == nil {
			state, labelled = pr.State, pr.HasLabel(st.Label)
		}
		d := preview.DecideGC(state, labelled, err, st.Label)
		job.Logf("datahub#%d: %s", n, d.Reason)
		if d.Remove {
			if err := Teardown(ctx, st, gh, n); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// owner names a lock holder: what, the host and the process.
func owner(what string) string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%s-%d", what, host, os.Getpid())
}
