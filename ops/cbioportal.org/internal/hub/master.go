package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/datahub"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/studyimport"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/job"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/preview"
)

// syncTable records each master sync of the hub, in its raw database.
const syncTable = "_hub_master_sync"

// MasterSettings are the settings of a master sync, from the environment.
type MasterSettings struct {
	Hub        preview.Databases
	Branch     string // DATAHUB_BRANCH, default master
	DatahubGit string
	Workdir    string
	// RetryAfter is how long a sync waits before it tries a commit whose
	// import failed again (RETRY_AFTER).
	RetryAfter time.Duration
	LockWait   time.Duration
	LockStale  time.Duration
	LockPoll   time.Duration
}

// MasterSettingsFromEnv reads the MasterSettings.
func MasterSettingsFromEnv() (*MasterSettings, error) {
	s := &MasterSettings{
		Hub:        HubDatabases(),
		Branch:     cli.EnvOr("DATAHUB_BRANCH", "master"),
		DatahubGit: cli.EnvOr("DATAHUB_GIT", datahub.DefaultGit),
		Workdir:    cli.EnvOr("WORKDIR", "/tmp/hub-sync"),
	}
	var err error
	if s.RetryAfter, err = job.EnvSeconds("RETRY_AFTER", 30*time.Minute); err != nil {
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

// SyncState is what the hub's sync table says about earlier syncs.
type SyncState struct {
	Imported string    // the last commit imported completely ("": none yet)
	Last     string    // the commit of the last sync
	Failed   bool      // the last sync failed
	At       time.Time // when the last sync ended
}

// SyncDecision is what a sync does about master's head commit.
type SyncDecision struct {
	Run    bool
	Reason string
}

// DecideSync says whether a sync imports head: not when it is the commit
// imported last, nor while a failed import of it waits RetryAfter.
func DecideSync(st SyncState, head string, now time.Time, retryAfter time.Duration) SyncDecision {
	switch {
	case head == st.Imported:
		return SyncDecision{false, "up to date at " + short(head)}
	case head == st.Last && st.Failed && now.Sub(st.At) < retryAfter:
		return SyncDecision{false, fmt.Sprintf("importing %s failed at %s; trying again after %s",
			short(head), st.At.Format(time.DateTime), st.At.Add(retryAfter).Format(time.DateTime))}
	case st.Imported == "":
		return SyncDecision{true, "first import of " + short(head)}
	}
	return SyncDecision{true, fmt.Sprintf("master moved from %s to %s", short(st.Imported), short(head))}
}

// SyncPlan is what a sync changes on the hub.
type SyncPlan struct {
	Import []string // study directories to import
	Gone   []string // study directories deleted since the last import
	Panels bool     // reload the shared gene panels
}

// PlanSync plans a sync from the study directories at the new commit and
// the files that changed since the last imported one. With no last import
// (first), every study is imported.
func PlanSync(headStudies, changed []string, first bool) SyncPlan {
	if first {
		return SyncPlan{Import: headStudies, Panels: true}
	}
	var p SyncPlan
	for _, d := range datahub.StudyDirs(changed) {
		if slices.Contains(headStudies, d) {
			p.Import = append(p.Import, d)
		} else {
			p.Gone = append(p.Gone, d)
		}
	}
	p.Panels = datahub.TouchesGenePanels(changed)
	return p
}

// SyncMaster brings hub.cbioportal.org up to datahub's master branch. It
// runs every minute, so it first compares master's head commit with the
// last one imported, which takes a second, and stops when they are the
// same. Otherwise it imports, in one import at the head commit, every
// study that changed since the last imported commit, and removes the
// studies deleted from master. Several merges since the last sync are one
// import; the commits between are not imported one by one. The head is
// recorded as imported only when the import succeeded.
func SyncMaster(ctx context.Context, st *MasterSettings) error {
	head, err := datahub.HeadCommit(ctx, st.DatahubGit, st.Branch)
	if err != nil {
		return err
	}
	c := ch.New(job.ClickHouseURL(), "default")
	state, err := readSyncState(c, st.Hub.Raw)
	if err != nil {
		return err
	}
	d := DecideSync(state, head, time.Now().UTC(), st.RetryAfter)
	job.Logf("%s", d.Reason)
	if !d.Run {
		return nil
	}

	repo := filepath.Join(st.Workdir, "datahub")
	defer os.RemoveAll(repo)
	// Files the LFS store lacks are taken from GitHub's LFS of datahub.
	co := &datahub.Checkout{Dir: repo, Git: st.DatahubGit, FallbackLFSURL: strings.TrimSuffix(st.DatahubGit, "/") + "/info/lfs", Logf: job.Logf}
	head, err = co.Fetch(ctx, st.Branch, nil)
	if err != nil {
		return err
	}
	files, err := co.Files(ctx, head)
	if err != nil {
		return err
	}
	headStudies := datahub.StudyDirsWithMeta(files)
	if len(headStudies) == 0 {
		// A checkout without studies is broken, not an empty datahub:
		// syncing it would remove every study from the hub.
		return recordFailure(c, st.Hub.Raw, head, fmt.Errorf("%s at %s has no studies", st.Branch, short(head)))
	}
	var changed []string
	if state.Imported != "" {
		if err := co.FetchCommit(ctx, state.Imported); err != nil {
			return fmt.Errorf("fetching the last imported commit %s: %w", short(state.Imported), err)
		}
		if changed, err = co.ChangedFiles(ctx, state.Imported, head); err != nil {
			return err
		}
	}
	plan := PlanSync(headStudies, changed, state.Imported == "")
	var goneIDs []string
	for _, dir := range plan.Gone {
		meta, err := co.Show(ctx, state.Imported, dir+"/meta_study.txt")
		if id := datahub.StudyIDFromMeta(meta); err == nil && id != "" {
			goneIDs = append(goneIDs, id)
		}
	}
	job.Logf("%d studies to import, %d to remove, gene panels changed: %v", len(plan.Import), len(goneIDs), plan.Panels)
	if len(plan.Import) == 0 && len(goneIDs) == 0 && !plan.Panels {
		return recordSync(c, st.Hub.Raw, head, "success", 0, 0, 0)
	}
	if err := co.SetPaths(ctx, plan.Import); err != nil {
		return err
	}
	if err := co.CheckoutFetched(ctx); err != nil {
		return err
	}

	lock := &studyimport.Lock{C: c, DB: st.Hub.Raw, Table: HubLockTable, Owner: owner("master-" + short(head)),
		Wait: st.LockWait, Stale: st.LockStale, Poll: st.LockPoll, Logf: job.Logf}
	if err := lock.Acquire(ctx); err != nil {
		return err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			job.Logf("%v", err)
		}
	}()

	dirs := make([]string, len(plan.Import))
	for i, s := range plan.Import {
		dirs[i] = filepath.Join(repo, s)
	}
	raw := ch.New(job.ClickHouseURL(), st.Hub.Raw)
	if state.Imported == "" {
		// The first import also removes the studies the hub holds that
		// master does not have.
		extra, err := studiesNotIn(raw, st.Hub.Raw, dirs)
		if err != nil {
			return err
		}
		goneIDs = append(goneIDs, extra...)
	}
	for _, id := range goneIDs {
		if _, err := ingest.RemoveStudy(raw, id); err != nil {
			return recordFailure(c, st.Hub.Raw, head, fmt.Errorf("removing %s: %w", id, err))
		}
		job.Logf("removed %s", id)
	}
	res, err := studyimport.Run(studyimport.Options{
		URL: job.ClickHouseURL(), DB: st.Hub.DB, RawDB: st.Hub.Raw, RefDB: st.Hub.Ref,
		Seed:       datahub.NewestSeed(repo),
		GenePanels: filepath.Join(repo, datahub.GenePanelsDir), ReloadPanels: plan.Panels,
		Prepare: func(dir string) error {
			_, err := co.PullLFS(ctx, rel(repo, dir))
			return err
		},
		Done: func(dir string) { os.RemoveAll(dir) },
		Logf: job.Logf,
	}, dirs)
	switch {
	case err != nil:
		return recordFailure(c, st.Hub.Raw, head, err)
	case res.TransformErr != nil:
		return recordFailure(c, st.Hub.Raw, head, fmt.Errorf("building the hub: %w", res.TransformErr))
	case res.PanelsErr != nil:
		return recordFailure(c, st.Hub.Raw, head, fmt.Errorf("loading the gene panels: %w", res.PanelsErr))
	}
	failed := res.Count(studyimport.Failed)
	for _, s := range res.Studies {
		if s.Status == studyimport.Failed {
			job.Logf("study %s failed to import and is not on the hub: %v", rel(repo, s.Dir), s.Err)
		}
	}
	job.Logf("hub at %s: %d studies imported, %d removed, %d failed", short(head), res.Count(studyimport.Imported), len(goneIDs), failed)
	return recordSync(c, st.Hub.Raw, head, "success", res.Count(studyimport.Imported), len(goneIDs), failed)
}

// studiesNotIn lists the studies of the raw database whose ids are not
// those of the study directories dirs.
func studiesNotIn(raw *ch.Client, rawDB string, dirs []string) ([]string, error) {
	keep := map[string]bool{}
	for _, d := range dirs {
		if id, err := studyimport.StudyID(d); err == nil {
			keep[id] = true
		}
	}
	text, err := job.Query(raw, fmt.Sprintf("SELECT DISTINCT study_id FROM `%s`.cancer_study ORDER BY study_id FORMAT TSV", rawDB))
	if err != nil {
		if strings.Contains(err.Error(), "UNKNOWN_TABLE") {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for id := range strings.SplitSeq(text, "\n") {
		if id != "" && !keep[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func syncTableName(rawDB string) string { return fmt.Sprintf("`%s`.`%s`", rawDB, syncTable) }

// readSyncState reads the sync table, creating it the first time.
func readSyncState(c *ch.Client, rawDB string) (SyncState, error) {
	exists, err := job.Query(c, fmt.Sprintf("EXISTS TABLE %s FORMAT TSV", syncTableName(rawDB)))
	if err != nil {
		return SyncState{}, err
	}
	if exists != "1" {
		if err := c.EnsureDatabase(rawDB); err != nil {
			return SyncState{}, err
		}
		if _, err := c.ExecuteServerwide("CREATE TABLE IF NOT EXISTS " + syncTableName(rawDB) +
			" (sha String, conclusion String, imported UInt32, removed UInt32, failed UInt32, error String, finished DateTime)" +
			" ENGINE = MergeTree ORDER BY finished"); err != nil {
			return SyncState{}, err
		}
		return SyncState{}, nil
	}
	text, err := job.Query(c, fmt.Sprintf("SELECT argMaxIf(sha, finished, conclusion = 'success'), argMax(sha, finished), "+
		"argMax(conclusion, finished), toUnixTimestamp(max(finished)) FROM %s FORMAT TSV", syncTableName(rawDB)))
	if err != nil {
		return SyncState{}, err
	}
	f := strings.Split(text, "\t")
	if len(f) != 4 {
		return SyncState{}, fmt.Errorf("reading %s: unexpected %q", syncTable, text)
	}
	at, _ := strconv.ParseInt(f[3], 10, 64)
	return SyncState{Imported: f[0], Last: f[1], Failed: f[2] == "failure", At: time.Unix(at, 0).UTC()}, nil
}

func recordSync(c *ch.Client, rawDB, sha, conclusion string, imported, removed, failed int, errText ...string) error {
	_, err := c.ExecuteServerwide(fmt.Sprintf("INSERT INTO %s SELECT '%s', '%s', %d, %d, %d, '%s', now()",
		syncTableName(rawDB), ch.SQLLit(sha), conclusion, imported, removed, failed, ch.SQLLit(strings.Join(errText, ""))))
	return err
}

// recordFailure records a failed sync of sha, so the next syncs wait
// RetryAfter before trying it again, and returns err.
func recordFailure(c *ch.Client, rawDB, sha string, err error) error {
	if recErr := recordSync(c, rawDB, sha, "failure", 0, 0, 0, err.Error()); recErr != nil {
		job.Logf("WARN: could not record the failed sync: %v", recErr)
	}
	return err
}
