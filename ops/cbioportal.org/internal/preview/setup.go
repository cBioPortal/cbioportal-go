package preview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/datahub"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/reference"
	"github.com/cbioportal/cbioportal-go/internal/studyimport"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/github"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/job"
)

// DatahubRepo is the GitHub repository of datahub.
const DatahubRepo = "cBioPortal/datahub"

// Settings of a preview job, from the environment the job manifests set.
type Settings struct {
	PR          int
	HeadSHA     string // the commit of this image
	Body        string // the PR description, instead of reading it from GitHub
	HasBody     bool
	Prefix      string // DB_PREFIX
	PreviewURL  string
	GoRepo      string
	DatahubRef  string // for studies
	Force       bool
	MaxAttempts int
	Workdir     string
	LogURL      string
	Label       string // PREVIEW_LABEL, for gc
	// Rewrite is the cbioportal-rewrite-test binary that clones production
	// for `preview-data: prod`.
	Rewrite string
	// DryRun makes teardown and gc log what they would remove, and
	// remove nothing.
	DryRun bool
}

// SettingsFromEnv reads the Settings; pr is the pull request number (0:
// from PR_NUMBER).
func SettingsFromEnv(pr int) (*Settings, error) {
	s := &Settings{
		HeadSHA:    os.Getenv("HEAD_SHA"),
		Prefix:     cli.EnvOr("DB_PREFIX", "dev_cbioportal_go_pr"),
		GoRepo:     cli.EnvOr("GO_REPO", "cBioPortal/cbioportal-go"),
		DatahubRef: cli.EnvOr("DATAHUB_REF", "master"),
		Force:      os.Getenv("FORCE") == "1",
		Workdir:    cli.EnvOr("WORKDIR", "/tmp/pr-preview"),
		Label:      cli.EnvOr("PREVIEW_LABEL", "preview"),
		Rewrite:    cli.EnvOr("CBIOPORTAL_REWRITE_TEST", "cbioportal-rewrite-test"),
	}
	s.Body, s.HasBody = os.LookupEnv("PR_BODY")
	if pr == 0 {
		if v := os.Getenv("PR_NUMBER"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || strings.TrimLeft(v, "0123456789") != "" {
				return nil, fmt.Errorf("PR_NUMBER must be a number, not %q", v)
			}
			pr = n
		}
	}
	s.PR = pr
	var err error
	if s.MaxAttempts, err = job.EnvInt("MAX_ATTEMPTS", 2); err != nil {
		return nil, err
	}
	if pr > 0 {
		s.PreviewURL = cli.EnvOr("PREVIEW_URL", fmt.Sprintf("https://pr-%d.gopreview.cbioportal.org", pr))
	}
	s.LogURL = cli.EnvOr("LOG_URL", s.PreviewURL)
	return s, nil
}

// setup is one run of Setup.
type setup struct {
	*Settings
	ctx     context.Context
	gh      *github.Client
	ch      *ch.Client
	dbs     Databases
	key     string
	summary job.Summary
	status  *github.Status
	prevLog string
}

// ErrFailed is returned by Setup when the build failed (after reporting
// it).
var ErrFailed = errors.New("preview build failed")

// setupLogDDL creates the log of builds in the raw database. A build is
// recorded with the PR commit and the configuration key, so a scheduled
// run rebuilds only when either changes.
func (s *setup) setupLog() string { return fmt.Sprintf("`%s`.`_preview_setup`", s.dbs.Raw) }

func (s *setup) logDDL() string {
	return "CREATE TABLE IF NOT EXISTS " + s.setupLog() +
		" (sha String, config String, conclusion String, finished DateTime DEFAULT now()) ENGINE = MergeTree ORDER BY finished"
}

// Setup builds the preview databases of a pull request with this image's
// importer and reports the result on the pull request. It writes
// <Workdir>/state.env with REBUILT=1 after a successful build, for the step
// that restarts the preview's API.
func Setup(ctx context.Context, st *Settings, gh *github.Client) error {
	if st.PR <= 0 {
		return errors.New("set PR_NUMBER")
	}
	s := &setup{Settings: st, ctx: ctx, gh: gh, ch: ch.New(job.ClickHouseURL(), "default"), dbs: DatabasesFor(st.Prefix, st.PR)}
	if err := os.MkdirAll(s.Workdir, 0o755); err != nil {
		return err
	}
	if err := s.writeState(false); err != nil {
		return err
	}

	if !s.HasBody {
		pr, err := gh.PR(ctx, s.GoRepo, s.PR)
		if err != nil {
			return fmt.Errorf("cannot read %s#%d: %w", s.GoRepo, s.PR, err)
		}
		s.Body = pr.Body
		if s.HeadSHA == "" {
			s.HeadSHA = pr.Head.SHA
		}
		if pr.Head.SHA != s.HeadSHA {
			job.Logf("PR head is %s, this run is for %s; leaving it to the newer image", pr.Head.SHA, s.HeadSHA)
			return nil
		}
	}
	if s.HeadSHA == "" {
		return errors.New("set HEAD_SHA")
	}

	cfg := ParseConfig(s.Body)
	var dhPR *github.PullRequest
	if cfg.Err == "" && cfg.Mode == ModeDatahubPR {
		var err error
		if dhPR, err = gh.PR(ctx, DatahubRepo, cfg.DatahubPR); err != nil {
			return fmt.Errorf("cannot read %s#%d: %w", DatahubRepo, cfg.DatahubPR, err)
		}
		s.key = cfg.Key(dhPR.Head.SHA)
	} else {
		s.key = cfg.Key("")
	}
	note := ""
	if cfg.Err != "" {
		note = " (" + cfg.Err + ")"
	}
	job.Logf("PR #%d at %s: %s%s", s.PR, s.HeadSHA, s.key, note)

	// Up to date?
	for _, d := range s.dbs.All() {
		if _, err := s.ch.ExecuteServerwide(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", d)); err != nil {
			return fmt.Errorf("cannot create %s: %w", d, err)
		}
	}
	if _, err := s.ch.ExecuteServerwide(s.logDDL()); err != nil {
		return err
	}
	counts, err := job.Query(s.ch, fmt.Sprintf(
		"SELECT countIf(conclusion != 'failure'), countIf(conclusion = 'failure') FROM %s WHERE sha = '%s' AND config = '%s' FORMAT TSV",
		s.setupLog(), ch.SQLLit(s.HeadSHA), ch.SQLLit(s.key)))
	if err != nil {
		return err
	}
	built, failed := parsePair(counts)
	if !s.Force {
		if built > 0 {
			job.Logf("up to date")
			return nil
		}
		if failed >= s.MaxAttempts {
			job.Logf("failed %d times already; push a commit or change the configuration to retry", failed)
			return nil
		}
	}
	if s.prevLog, err = job.Query(s.ch, "SELECT sha, config, conclusion, finished FROM "+s.setupLog()+" FORMAT TSV"); err != nil {
		return err
	}

	s.status = &github.Status{C: gh, Repo: s.GoRepo, SHA: s.HeadSHA, CheckName: "Preview",
		Environment: fmt.Sprintf("pr-%d-preview", s.PR), Description: "Preview on gopreview.cbioportal.org",
		LogURL: s.LogURL, Logf: job.Logf}
	s.status.Start(ctx, "Building the preview databases...", fmt.Sprintf("Data: `%s`\n\n[Logs](%s)", s.key, s.LogURL), s.PreviewURL+"/")

	if cfg.Err != "" {
		return s.fail("Invalid Preview Configuration: " + cfg.Err)
	}
	if cfg.Mode == ModeProd {
		return s.buildProd()
	}
	return s.buildStudies(cfg, dhPR)
}

func parsePair(s string) (int, int) {
	a, b, _ := strings.Cut(s, "\t")
	x, _ := strconv.Atoi(strings.TrimSpace(a))
	y, _ := strconv.Atoi(strings.TrimSpace(b))
	return x, y
}

func (s *setup) writeState(rebuilt bool) error {
	v := "0"
	if rebuilt {
		v = "1"
	}
	return os.WriteFile(filepath.Join(s.Workdir, "state.env"), []byte("REBUILT="+v+"\n"), 0o644)
}

// fail records a failed build with reason as its only summary line.
func (s *setup) fail(reason string) error {
	s.summary.Add("- :x: %s", reason)
	return s.finish("failure", reason)
}

// finish records the build, completes the Check Run and Deployment, and
// returns ErrFailed for a failure.
func (s *setup) finish(conclusion, title string) error {
	// Cloning production empties the raw database, the setup log included.
	if _, err := s.ch.ExecuteServerwide(s.logDDL()); err != nil {
		job.Logf("WARN: could not recreate the setup log: %v", err)
	}
	if s.prevLog != "" {
		if n, err := job.Query(s.ch, "SELECT count() FROM "+s.setupLog()+" FORMAT TSV"); err == nil && n == "0" {
			if _, err := s.ch.ExecuteServerwide("INSERT INTO " + s.setupLog() + " FORMAT TSV\n" + s.prevLog + "\n"); err != nil {
				job.Logf("WARN: could not restore the setup log: %v", err)
			}
		}
	}
	if _, err := s.ch.ExecuteServerwide(fmt.Sprintf("INSERT INTO %s (sha, config, conclusion) VALUES ('%s', '%s', '%s')",
		s.setupLog(), ch.SQLLit(s.HeadSHA), ch.SQLLit(s.key), ch.SQLLit(conclusion))); err != nil {
		job.Logf("WARN: could not record the build: %v", err)
	}
	s.summary.Add("\nData: `%s`\n\n[Preview](%s) · [Logs](%s)", s.key, s.PreviewURL, s.LogURL)
	job.Logf("result: %s: %s", conclusion, title)
	fmt.Fprint(os.Stderr, s.summary.String())
	details := s.PreviewURL + "/"
	if conclusion == "failure" {
		details = s.LogURL
	}
	s.status.Finish(s.ctx, conclusion, title, s.summary.String(), details, s.PreviewURL+"/")
	if conclusion == "failure" {
		return ErrFailed
	}
	return s.writeState(true)
}

// buildProd clones production into the preview databases.
func (s *setup) buildProd() error {
	cmd := exec.CommandContext(s.ctx, s.Rewrite, "java-v7-to-go", "mirror")
	cmd.Env = append(os.Environ(), "TARGET_DB="+s.dbs.DB, "WORKDIR="+filepath.Join(s.Workdir, "mirror"))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		job.Logf("cloning production: %v", err)
		return s.fail("Cloning production failed (see logs)")
	}
	n, _ := job.Query(s.ch, fmt.Sprintf("SELECT count() FROM `%s`.sample_derived FORMAT TSV", s.dbs.DB))
	s.summary.Add("- :white_check_mark: production: %s samples", n)
	return s.finish("success", "Production data")
}

// buildStudies imports studies from datahub into empty preview databases.
func (s *setup) buildStudies(cfg Config, dhPR *github.PullRequest) error {
	studies, ref := cfg.Studies, s.DatahubRef
	fallback := ""
	if cfg.Mode == ModeDatahubPR {
		files, err := s.gh.PRFiles(s.ctx, DatahubRepo, cfg.DatahubPR)
		if err != nil {
			job.Logf("listing the files of %s#%d: %v", DatahubRepo, cfg.DatahubPR, err)
			return s.fail(fmt.Sprintf("Could not list the files of %s#%d", DatahubRepo, cfg.DatahubPR))
		}
		studies = datahub.StudyDirs(files)
		if len(cfg.Studies) > 0 {
			studies = job.Intersect(studies, cfg.Studies)
		}
		ref = fmt.Sprintf("pull/%d/head", cfg.DatahubPR)
		fallback = cli.EnvOr("DATAHUB_GIT", datahub.DefaultGit)
		if u := dhPR.HeadCloneURL(); u != "" {
			fallback = u
		}
		fallback += "/info/lfs"
	}
	if len(studies) == 0 {
		return s.fail("No studies to import")
	}
	job.Logf("studies: %s", strings.Join(studies, " "))

	repo := filepath.Join(s.Workdir, "datahub")
	co := &datahub.Checkout{Dir: repo, FallbackLFSURL: fallback, Logf: job.Logf}
	commit, err := co.Fetch(s.ctx, ref, studies)
	if err == nil {
		err = co.CheckoutFetched(s.ctx)
	}
	if err != nil {
		job.Logf("%v", err)
		return s.fail(fmt.Sprintf("Could not check out %s %s", DatahubRepo, ref))
	}
	defer os.RemoveAll(repo)
	job.Logf("datahub %s at %s", ref, commit)

	if err := EmptyDatabases(s.ch, s.dbs, s.dbs.Raw+"._preview_setup"); err != nil {
		job.Logf("%v", err)
		return s.fail("Could not empty the preview databases")
	}
	seed := datahub.NewestSeed(repo)
	job.Logf("loading reference data from %s", seed)
	if seed == "" {
		return s.fail("Loading the reference data failed")
	}
	if _, _, err := reference.LoadReference(ch.New(job.ClickHouseURL(), s.dbs.Ref), seed); err != nil {
		job.Logf("%v", err)
		return s.fail("Loading the reference data failed")
	}
	_, err = co.PullLFS(s.ctx, datahub.GenePanelsDir)
	if err == nil {
		_, _, err = ingest.LoadGenePanelsDir(ch.New(job.ClickHouseURL(), s.dbs.Raw), filepath.Join(repo, datahub.GenePanelsDir))
	}
	if err != nil {
		job.Logf("%v", err)
		return s.fail("Loading the shared gene panels failed")
	}

	dirs := make([]string, len(studies))
	for i, st := range studies {
		dirs[i] = filepath.Join(repo, st)
	}
	res, err := studyimport.Run(studyimport.Options{
		URL: job.ClickHouseURL(), DB: s.dbs.DB, RawDB: s.dbs.Raw, RefDB: s.dbs.Ref,
		Prepare: func(dir string) error { _, err := co.PullLFS(s.ctx, rel(repo, dir)); return err },
		Done:    func(dir string) { os.RemoveAll(dir) },
		Logf:    job.Logf,
	}, dirs)
	if err != nil {
		job.Logf("%v", err)
		return s.fail("Loading the reference data failed")
	}
	ok, failed := s.studyLines(res, repo)
	if res.TransformErr != nil {
		return s.fail("Transform failed (see logs)")
	}
	switch {
	case ok == 0:
		return s.fail("No study imported")
	case failed > 0:
		return s.finish("neutral", fmt.Sprintf("Imported %d of %d studies", ok, ok+failed))
	}
	return s.finish("success", fmt.Sprintf("Imported %d studies", ok))
}

// studyLines adds a summary line per study and counts the imported and
// failed ones.
func (s *setup) studyLines(res *studyimport.Result, repo string) (ok, failed int) {
	for _, st := range res.Studies {
		switch st.Status {
		case studyimport.Skipped:
			s.summary.Add("- :heavy_minus_sign: `%s`: no meta_study.txt, skipped", rel(repo, st.Dir))
		case studyimport.Imported:
			ok++
			s.summary.Add("- :white_check_mark: [%s](%s/study/summary?id=%s) (%ds)", st.StudyID, s.PreviewURL, st.StudyID, int(st.Duration/time.Second))
		default:
			failed++
			id := st.StudyID
			if id == "" {
				id = rel(repo, st.Dir)
			}
			s.summary.Add("- :x: `%s`: import failed (see logs)", id)
		}
	}
	return ok, failed
}

func rel(base, path string) string {
	if r, err := filepath.Rel(base, path); err == nil {
		return r
	}
	return path
}

// EmptyDatabases drops every table and view of the preview databases but
// keep (db.table); the databases themselves stay, since a job's user may
// not be allowed to create them.
func EmptyDatabases(c *ch.Client, dbs Databases, keep string) error {
	for _, d := range dbs.All() {
		for _, views := range []bool{true, false} {
			op := "="
			kind := "VIEW"
			if !views {
				op, kind = "!=", "TABLE"
			}
			names, err := job.Query(c, fmt.Sprintf("SELECT name FROM system.tables WHERE database = '%s' AND engine %s 'View' FORMAT TSV", ch.SQLLit(d), op))
			if err != nil {
				return err
			}
			for name := range strings.SplitSeq(names, "\n") {
				if name == "" || d+"."+name == keep {
					continue
				}
				sync := ""
				if !views {
					sync = " SYNC"
				}
				if _, err := c.ExecuteServerwide(fmt.Sprintf("DROP %s IF EXISTS `%s`.`%s`%s", kind, d, name, sync)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
