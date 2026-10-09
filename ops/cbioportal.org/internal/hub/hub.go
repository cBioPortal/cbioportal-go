// Package hub imports the studies of cBioPortal/datahub pull requests into
// hub.cbioportal.org and reports the result on each pull request.
package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// DefaultValidator is where the image keeps cbioportal-core's validator.
const DefaultValidator = "/opt/cbioportal-core/scripts/importer/validateData.py"

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

	// Validation with cbioportal-core's validateData.py: Validator is the
	// script ("" skips validation), Python its interpreter, HubAPI the API
	// the portal information is read from.
	Validator       string
	Python          string
	HubAPI          string
	ValidateTimeout time.Duration
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
	s.HubAPI = job.Env("HUB_API", s.Base)
	s.Validator = job.Env("VALIDATOR", DefaultValidator)
	if s.Validator == "off" {
		s.Validator = ""
	}
	s.Python = job.Env("PYTHON", "python3")
	if s.ValidateTimeout, err = job.EnvSeconds("VALIDATE_TIMEOUT", time.Hour); err != nil {
		return nil, err
	}
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

	validator   *Validator
	validations map[string]Validation // by study directory, relative to the checkout
	invalid     int                   // studies with validation errors
	order       []string              // validated study directories, in order
	pending     func()                // collects the running validation
	complete    bool                  // every study was imported
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
	r.setupValidation(ctx, co, files)

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
		Prepare: func(dir string) error {
			if _, err := co.PullLFS(ctx, rel(repo, dir)); err != nil {
				return err
			}
			r.validate(ctx, rel(repo, dir), dir)
			return nil
		},
		// Done runs before Progress and the summary, so they have the
		// study's validation.
		Done: func(dir string) {
			r.waitValidation()
			os.RemoveAll(dir)
		},
		Progress: func(st studyimport.StudyResult) {
			if st.Status != studyimport.Failed {
				return
			}
			r.fails++
			failures.Add("%s", r.studyLine(repo, st))
			r.summary.Add("%s", r.studyLine(repo, st))
			r.status.Text = ValidationText(r.order, r.validations, r.LogURL)
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
	conclusion, title := Conclusion(r.ok, r.fails)
	r.complete = conclusion == "success"
	r.status.DeploymentConclusion = conclusion
	return r.finish(WithValidation(conclusion, title, r.invalid))
}

// setupValidation writes the portal information the validator reads, from
// the hub's API and the gene panel files the pull request changes (files).
// Without them the studies are imported unvalidated.
func (r *run) setupValidation(ctx context.Context, co *datahub.Checkout, files []string) {
	if r.Validator == "" {
		return
	}
	if _, err := os.Stat(r.Validator); err != nil {
		job.Logf("not validating: %v", err)
		r.summary.Add("- :heavy_minus_sign: studies not validated: validateData.py is not installed")
		return
	}
	info := filepath.Join(r.Workdir, "portal-info")
	var panels []string
	for _, f := range files {
		if strings.HasPrefix(f, datahub.GenePanelsDir+"/") {
			panels = append(panels, filepath.Join(co.Dir, f))
		}
	}
	if len(panels) > 0 {
		if _, err := co.PullLFS(ctx, datahub.GenePanelsDir); err != nil {
			job.Logf("not validating: %v", err)
			r.summary.Add("- :heavy_minus_sign: studies not validated: could not download the gene panels (see logs)")
			return
		}
	}
	job.Logf("reading the portal information for validation from %s", r.HubAPI)
	if err := WritePortalInfo(ctx, &http.Client{Timeout: 5 * time.Minute}, r.HubAPI, info, panels); err != nil {
		job.Logf("not validating: %v", err)
		r.summary.Add("- :heavy_minus_sign: studies not validated: could not read the hub's portal information (see logs)")
		return
	}
	r.validator = &Validator{Python: r.Python, Script: r.Validator, InfoDir: info, Timeout: r.ValidateTimeout, Logf: job.Logf}
	r.validations = map[string]Validation{}
}

// validate starts validating a study while it is staged; waitValidation
// collects the result. The outcome never stops the import. Prepare also
// runs for the gene panels, which are not a study.
func (r *run) validate(ctx context.Context, name, dir string) {
	if r.validator == nil {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "meta_study.txt")); err != nil {
		return
	}
	done := make(chan Validation, 1)
	go func() { done <- r.validator.Validate(ctx, dir) }()
	r.pending = func() {
		v := <-done
		r.validations[name] = v
		r.order = append(r.order, name)
		if v.Failed() {
			r.invalid++
		}
	}
}

// waitValidation waits for the study validate started, if any.
func (r *run) waitValidation() {
	if r.pending != nil {
		r.pending()
		r.pending = nil
	}
}

// WithValidation turns an import's conclusion into a failure when studies
// have validation errors, as datahub's own validation does; the studies are
// imported all the same.
func WithValidation(conclusion, title string, invalid int) (string, string) {
	if invalid == 0 || conclusion == "skipped" {
		return conclusion, title
	}
	return "failure", fmt.Sprintf("%s; %d with validation errors", title, invalid)
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

// studyLine is the summary line of a study, with its validation.
func (r *run) studyLine(repo string, st studyimport.StudyResult) string {
	line := r.importLine(repo, st)
	if v, ok := r.validations[rel(repo, st.Dir)]; ok {
		line += " · " + v.Note()
	}
	return line
}

// importLine is the import part of a study's summary line.
func (r *run) importLine(repo string, st studyimport.StudyResult) string {
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
	if r.lock != nil && r.lock.Held() && r.complete {
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
	r.status.Text = ValidationText(r.order, r.validations, r.LogURL)
	r.status.Finish(r.ctx, conclusion, title, r.summary.String(), details, env)
	if conclusion == "failure" && !r.complete {
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
