// Package hub imports the studies of cBioPortal/datahub pull requests into
// hub.cbioportal.org and reports the result on each pull request.
package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/datahub"
	"github.com/cbioportal/cbioportal-go/internal/studyimport"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/github"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/job"
)

// DatahubRepo is the GitHub repository of datahub.
const DatahubRepo = "cBioPortal/datahub"

// Settings of an import, from the environment the Job manifest sets.
type Settings struct {
	PR            int
	HeadSHA       string // the commit this Job is for
	DB, Raw, Ref  string // HUB_DB, HUB_RAW_DB, HUB_REF_DB
	Base          string // HUB_BASE, the hub's public URL
	Workdir       string
	LogURL        string
	LockWait      time.Duration
	LockStale     time.Duration
	LockPoll      time.Duration
	LockHeartbeat time.Duration
	Owner         string // the lock holder's name (default: pr-<N>-<sha>-<host>-<pid>)
	DatahubGit    string
}

// SettingsFromEnv reads the Settings.
func SettingsFromEnv() (*Settings, error) {
	v := os.Getenv("PR_NUMBER")
	pr, err := strconv.Atoi(v)
	if v == "" || err != nil || pr <= 0 || strings.TrimLeft(v, "0123456789") != "" {
		return nil, fmt.Errorf("PR_NUMBER must be a number, not %q", v)
	}
	s := &Settings{
		PR:         pr,
		HeadSHA:    os.Getenv("HEAD_SHA"),
		DB:         job.Env("HUB_DB", "dev_cbioportal_hub_go"),
		Base:       job.Env("HUB_BASE", "https://hub.cbioportal.org"),
		Workdir:    job.Env("WORKDIR", "/tmp/hub-import"),
		DatahubGit: job.Env("DATAHUB_GIT", datahub.DefaultGit),
	}
	s.Raw = job.Env("HUB_RAW_DB", s.DB+"_raw")
	s.Ref = job.Env("HUB_REF_DB", s.DB+"_ref")
	s.LogURL = job.Env("LOG_URL", s.Base)
	if s.LockWait, err = job.EnvSeconds("LOCK_WAIT", 2*time.Hour); err != nil {
		return nil, err
	}
	if s.LockStale, err = job.EnvSeconds("LOCK_STALE", 10*time.Minute); err != nil {
		return nil, err
	}
	if s.LockPoll, err = job.EnvSeconds("LOCK_POLL", 30*time.Second); err != nil {
		return nil, err
	}
	return s, nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// ErrFailed is returned by ImportPR when the import failed (after
// reporting it).
var ErrFailed = errors.New("hub import failed")

type run struct {
	*Settings
	ctx       context.Context
	gh        *github.Client
	ch        *ch.Client
	status    *github.Status
	lock      *studyimport.Lock
	summary   job.Summary
	ok, fails int
	studies   int
}

func (r *run) importLog() string { return fmt.Sprintf("`%s`.`_hub_import_log`", r.Raw) }

// ImportPR imports the studies a datahub pull request changes into the hub
// (see the package README for the steps).
func ImportPR(ctx context.Context, st *Settings, gh *github.Client) error {
	r := &run{Settings: st, ctx: ctx, gh: gh, ch: ch.New(job.ClickHouseURL(), "default")}
	if err := os.MkdirAll(r.Workdir, 0o755); err != nil {
		return err
	}

	pr, err := gh.PR(ctx, DatahubRepo, r.PR)
	if err != nil {
		return fmt.Errorf("cannot read PR #%d: %w", r.PR, err)
	}
	if r.HeadSHA == "" {
		r.HeadSHA = pr.Head.SHA
	}
	job.Logf("PR #%d head %s, Job for %s", r.PR, pr.Head.SHA, r.HeadSHA)
	if r.Owner == "" {
		host, _ := os.Hostname()
		r.Owner = fmt.Sprintf("pr-%d-%s-%s-%d", r.PR, short(r.HeadSHA), host, os.Getpid())
	}

	files, err := gh.PRFiles(ctx, DatahubRepo, r.PR)
	if err != nil {
		return fmt.Errorf("cannot list the PR's files: %w", err)
	}
	studies, panelsChanged := Plan(files, pr.Body)
	r.studies = len(studies)
	if len(studies) == 0 && !panelsChanged {
		job.Logf("no studies to import")
		return nil
	}
	job.Logf("studies: %s", strings.Join(studies, " "))

	// Argo CD recreates a finished Job once its TTL removes it; a commit
	// that was imported already is not imported again.
	if err := r.ch.EnsureDatabase(r.Raw); err != nil {
		return fmt.Errorf("cannot reach ClickHouse: %w", err)
	}
	if _, err := r.ch.ExecuteServerwide("CREATE TABLE IF NOT EXISTS " + r.importLog() +
		" (pr UInt32, sha String, conclusion String, finished DateTime) ENGINE = MergeTree ORDER BY (pr, sha)"); err != nil {
		return fmt.Errorf("cannot reach ClickHouse: %w", err)
	}
	doneAt, err := job.Query(r.ch, fmt.Sprintf("SELECT max(finished) FROM %s WHERE pr = %d AND sha = '%s' HAVING count() > 0 FORMAT TSV",
		r.importLog(), r.PR, ch.SQLLit(r.HeadSHA)))
	if err != nil {
		return err
	}
	if doneAt != "" {
		job.Logf("commit %s was imported at %s; nothing to do", r.HeadSHA, doneAt)
		return nil
	}

	r.status = &github.Status{C: gh, Repo: DatahubRepo, SHA: r.HeadSHA, CheckName: "Hub preview import",
		Environment: "hub-preview", Description: "Preview on hub.cbioportal.org", LogURL: r.LogURL, Logf: job.Logf}
	r.status.Start(ctx, "Importing studies...", fmt.Sprintf("[Logs](%s) · [hub.cbioportal.org](%s)", r.LogURL, r.Base), r.Base+"/")

	// The checkout; LFS files are downloaded per study, just before its
	// ingest, and the study is deleted after.
	repo := filepath.Join(r.Workdir, "datahub")
	fallback := pr.HeadCloneURL()
	if fallback == "" {
		fallback = r.DatahubGit
	}
	co := &datahub.Checkout{Dir: repo, Git: r.DatahubGit, FallbackLFSURL: fallback + "/info/lfs", Logf: job.Logf}
	fetched, err := co.Fetch(ctx, fmt.Sprintf("pull/%d/head", r.PR), studies)
	if err != nil {
		job.Logf("%v", err)
		r.summary.Add("- :x: could not fetch the PR head")
		return r.finish("failure", "Could not fetch the PR")
	}
	if fetched != r.HeadSHA {
		r.summary.Add("- superseded by %s", fetched)
		return r.finish("skipped", "Superseded by a newer commit")
	}
	if err := co.CheckoutFetched(ctx); err != nil {
		job.Logf("%v", err)
		return r.finish("failure", "Could not check out the PR")
	}
	defer os.RemoveAll(repo)

	// One import at a time across Jobs.
	r.lock = &studyimport.Lock{C: r.ch, DB: r.Raw, Table: "_hub_import_lock", Owner: r.Owner,
		Wait: r.LockWait, Stale: r.LockStale, Poll: r.LockPoll, Heartbeat: r.LockHeartbeat, Logf: job.Logf}
	if err := r.lock.Acquire(ctx); err != nil {
		job.Logf("%v", err)
		r.summary.Add("- :x: could not take the hub import lock (see logs)")
		return r.finish("failure", "Could not take the hub import lock")
	}
	defer r.lock.Release()
	// A newer commit may have been pushed while this Job waited.
	if latest, err := gh.PR(ctx, DatahubRepo, r.PR); err == nil && latest.Head.SHA != "" && latest.Head.SHA != r.HeadSHA {
		r.summary.Add("- superseded by %s", latest.Head.SHA)
		return r.finish("skipped", "Superseded by a newer commit")
	}

	dirs := make([]string, len(studies))
	for i, s := range studies {
		dirs[i] = filepath.Join(repo, s)
	}
	var failures job.Summary
	res, err := studyimport.Run(studyimport.Options{
		URL: job.ClickHouseURL(), DB: r.DB, RawDB: r.Raw, RefDB: r.Ref,
		Seed:       datahub.NewestSeed(repo),
		GenePanels: filepath.Join(repo, datahub.GenePanelsDir), ReloadPanels: panelsChanged,
		Prepare: func(dir string) error { _, err := co.PullLFS(ctx, rel(repo, dir)); return err },
		Done: func(dir string) {
			os.RemoveAll(dir)
		},
		Progress: func(st studyimport.StudyResult) {
			if st.Status != studyimport.Failed {
				return
			}
			r.fails++
			failures.Add("%s", r.studyLine(repo, st))
			r.summary.Add("%s", r.studyLine(repo, st))
			r.status.Progress(ctx, fmt.Sprintf("Importing... (%d of %d studies, %d failed)", r.ok+r.fails, r.studies, r.fails), r.summary.String())
		},
		Logf: job.Logf,
	}, dirs)
	if err != nil {
		job.Logf("%v", err)
		r.summary.Add("- :x: loading the reference data failed")
		return r.finish("failure", "Loading the reference data failed")
	}
	r.addResults(res, repo, panelsChanged)
	return r.finish(Conclusion(r.ok, r.fails))
}

// Plan returns the study directories of a pull request's changed files,
// narrowed by a `hub-import-only:` list in its description, and whether it
// changes the shared gene panels.
func Plan(files []string, body string) ([]string, bool) {
	studies := datahub.StudyDirs(files)
	if only := job.BodyList(body, "hub-import-only"); len(only) > 0 {
		job.Logf("hub-import-only: %s", strings.Join(only, " "))
		studies = job.Intersect(studies, only)
	}
	return studies, datahub.TouchesGenePanels(files)
}

// studyLine is the summary line of a study.
func (r *run) studyLine(repo string, st studyimport.StudyResult) string {
	switch st.Status {
	case studyimport.Imported:
		return fmt.Sprintf("- :white_check_mark: [%[1]s](%[2]s/study/summary?id=%[1]s) (%[3]ds)", st.StudyID, r.Base, int(st.Duration/time.Second))
	case studyimport.Skipped:
		return fmt.Sprintf("- :heavy_minus_sign: `%s` — no meta_study.txt in the PR, skipped", rel(repo, st.Dir))
	case studyimport.Removed:
		return fmt.Sprintf("- :x: `%s` — building the hub failed (see logs); removed from the hub", st.StudyID)
	}
	return fmt.Sprintf("- :x: `%s` — import failed (see logs); removed from the hub", st.StudyID)
}

// addResults adds the summary lines after the transform: the gene panels,
// then the studies that were not reported as they failed.
func (r *run) addResults(res *studyimport.Result, repo string, panelsChanged bool) {
	if res.PanelsErr != nil {
		r.summary.Add("- :x: loading the reference gene panels failed")
		r.fails++
	} else if panelsChanged && res.PanelsLoaded {
		r.summary.Add("- :white_check_mark: reference gene panels")
		r.ok++
	}
	for _, st := range res.Studies {
		switch st.Status {
		case studyimport.Imported:
			r.ok++
		case studyimport.Removed:
			r.fails++
		case studyimport.Failed:
			continue // reported when it failed
		}
		r.summary.Add("%s", r.studyLine(repo, st))
	}
	if res.RebuildErr != nil {
		job.Logf("the rebuild failed too; the hub keeps serving its previous build")
	}
}

// Conclusion is the Check Run conclusion and title for the counts of
// imported and failed items.
func Conclusion(ok, failed int) (string, string) {
	total := ok + failed
	switch {
	case failed == 0 && ok > 0:
		return "success", fmt.Sprintf("Imported %d of %d", ok, total)
	case ok > 0:
		return "neutral", fmt.Sprintf("Imported %d of %d", ok, total)
	case failed == 0:
		return "neutral", "Nothing imported"
	}
	return "failure", fmt.Sprintf("Imported 0 of %d", total)
}

// finish records a complete import, releases the lock, and posts the final
// Check Run and Deployment status.
func (r *run) finish(conclusion, title string) error {
	// Only a complete import is recorded: a recreated Job retries a
	// partial one.
	if r.lock != nil && r.lock.Held() && conclusion == "success" {
		if _, err := r.ch.ExecuteServerwide(fmt.Sprintf("INSERT INTO %s SELECT %d, '%s', '%s', now()",
			r.importLog(), r.PR, ch.SQLLit(r.HeadSHA), conclusion)); err != nil {
			job.Logf("WARN: could not record the import: %v", err)
		}
	}
	if r.lock != nil {
		if err := r.lock.Release(); err != nil {
			job.Logf("%v", err)
		}
	}
	firstOK := FirstStudyLink(r.summary.String())
	details := r.LogURL
	if conclusion == "success" && firstOK != "" {
		details = firstOK
	}
	r.summary.Add("\n[Import logs](%s)", r.LogURL)
	job.Logf("result: %s: %s", conclusion, title)
	fmt.Fprint(os.Stderr, r.summary.String())
	env := firstOK
	if env == "" {
		env = r.Base + "/"
	}
	r.status.Finish(r.ctx, conclusion, title, r.summary.String(), details, env)
	if conclusion == "failure" {
		return ErrFailed
	}
	return nil
}

// FirstStudyLink is the link of the first imported study in a summary.
func FirstStudyLink(summary string) string {
	for line := range strings.SplitSeq(summary, "\n") {
		if !strings.Contains(line, ":white_check_mark:") {
			continue
		}
		if i := strings.Index(line, "https://"); i >= 0 {
			link := line[i:]
			if j := strings.IndexByte(link, ')'); j >= 0 {
				link = link[:j]
			}
			return link
		}
		return ""
	}
	return ""
}

func rel(base, path string) string {
	if r, err := filepath.Rel(base, path); err == nil {
		return r
	}
	return path
}
