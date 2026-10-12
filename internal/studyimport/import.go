package studyimport

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/parser"
	"github.com/cbioportal/cbioportal-go/internal/reference"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

// Options configure Run. DB, RawDB and RefDB are the three databases a
// portal is served from.
type Options struct {
	URL              string // ClickHouse HTTP endpoint
	DB, RawDB, RefDB string

	// Seed is a seed dump (seed-*.sql.gz) loaded into RefDB when it has no
	// genes yet; without it such a RefDB is an error.
	Seed string
	// GenePanels is a directory of gene panel files loaded into RawDB when
	// it has no shared gene panels yet, or always with ReloadPanels.
	GenePanels   string
	ReloadPanels bool

	// Prepare, when set, runs just before a study is staged (e.g. to
	// download its files); an error fails that study. Done runs after it,
	// whatever the outcome (e.g. to delete the files again).
	Prepare func(dir string) error
	Done    func(dir string)
	// Progress, when set, gets each study's result as soon as it is
	// staged (or failed to be), before the transform.
	Progress func(StudyResult)

	Logf func(format string, args ...any)
}

// Status is the outcome of one study.
type Status string

const (
	Imported Status = "imported" // staged and published
	Failed   Status = "failed"   // could not be staged; removed from the portal
	Removed  Status = "removed"  // staged, but the transform failed; removed from the portal
	Skipped  Status = "skipped"  // not a study directory (no meta_study.txt)
)

// StudyResult is what happened to one study directory.
type StudyResult struct {
	Dir      string
	StudyID  string
	Status   Status
	Err      error
	Duration time.Duration // of staging it
}

// Result is the outcome of Run.
type Result struct {
	Studies []StudyResult
	// PanelsLoaded is set when the shared gene panels were (re)loaded;
	// PanelsErr when that failed.
	PanelsLoaded bool
	PanelsErr    error
	// TransformErr is set when the portal tables could not be built with
	// the new studies; they were then removed again and RebuildErr is the
	// outcome of building the portal without them.
	TransformErr error
	RebuildErr   error
}

// Count returns the number of studies with status s.
func (r *Result) Count(s Status) int {
	n := 0
	for _, st := range r.Studies {
		if st.Status == s {
			n++
		}
	}
	return n
}

func (o *Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// Run stages each study directory in RawDB and then builds the portal
// tables of DB once, from every staged study. The transform builds the new
// tables beside the served ones and swaps them in together at the end, so
// the portal serves either its previous state or the new one. A study that
// fails to stage is removed from RawDB; when the transform fails, this
// run's studies are removed and the portal rebuilt without them. Run
// returns an error only when the reference data cannot be set up; the
// per-study outcomes are in the Result. Callers that share the databases
// with other importers hold a Lock around Run.
func Run(o Options, dirs []string) (*Result, error) {
	raw := ch.New(o.URL, o.RawDB)
	if err := o.ensureReference(); err != nil {
		return nil, err
	}
	res := &Result{}
	if err := o.ensurePanels(raw, res); err != nil {
		res.PanelsErr = err
		o.logf("loading the shared gene panels failed: %v", err)
	}

	var imported []int
	for i, dir := range dirs {
		o.logf("[%d/%d] %s", i+1, len(dirs), dir)
		r := o.stage(raw, dir)
		res.Studies = append(res.Studies, r)
		if o.Progress != nil {
			o.Progress(r)
		}
		if r.Status == Imported {
			imported = append(imported, len(res.Studies)-1)
		}
	}
	// A failed study was removed from RawDB, so the transform runs to take
	// it out of the portal too; only when nothing changed is it skipped.
	if len(dirs) > 0 && res.Count(Skipped) == len(dirs) && !res.PanelsLoaded {
		o.logf("nothing staged; the portal is left as is")
		return res, nil
	}

	o.logf("transform")
	start := time.Now()
	counts, err := transform.Apply(ch.New(o.URL, o.RawDB), o.RawDB, o.DB, o.RefDB)
	if err == nil {
		o.logf("transform: %d objects in %.0fs", len(counts), time.Since(start).Seconds())
		return res, nil
	}
	res.TransformErr = err
	o.logf("transform failed: %v", err)
	if len(imported) == 0 {
		return res, nil
	}
	o.logf("removing this run's studies and rebuilding without them")
	for _, i := range imported {
		st := &res.Studies[i]
		st.Status = Removed
		st.Err = err
		if _, rmErr := ingest.RemoveStudy(raw, st.StudyID); rmErr != nil {
			o.logf("  removing %s: %v", st.StudyID, rmErr)
		}
	}
	if _, err := transform.Apply(ch.New(o.URL, o.RawDB), o.RawDB, o.DB, o.RefDB); err != nil {
		res.RebuildErr = err
		o.logf("the rebuild failed too; the portal keeps serving its previous build: %v", err)
	}
	return res, nil
}

// PlannedStudy is what Run would do with one study directory.
type PlannedStudy struct {
	Dir     string `json:"dir"`
	StudyID string `json:"studyId,omitempty"`
	// Action is "import" for a new study, "replace" for one the raw
	// database already holds (its rows are replaced), or "skip".
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	// Tables is the number of raw tables holding the study's rows now.
	Tables int `json:"tables,omitempty"`
}

// ImportPlan is what Run would do, found without changing anything.
type ImportPlan struct {
	Studies    []PlannedStudy `json:"studies"`
	LoadSeed   bool           `json:"loadSeed"`
	LoadPanels bool           `json:"loadPanels"`
	Transform  bool           `json:"transform"`
}

// Plan reports what Run(o, dirs) would do, reading the databases and the
// study directories (which must be local: Prepare is not called) but
// changing nothing.
func Plan(o Options, dirs []string) (*ImportPlan, error) {
	p := &ImportPlan{}
	n, err := countRows(ch.New(o.URL, o.RefDB), o.RefDB, "gene", "")
	if err != nil {
		return nil, err
	}
	if n == 0 {
		if o.Seed == "" {
			return nil, fmt.Errorf("reference database %s has no genes: pass a seed dump to load them", o.RefDB)
		}
		p.LoadSeed = true
	}
	raw := ch.New(o.URL, o.RawDB)
	if o.GenePanels != "" {
		p.LoadPanels = o.ReloadPanels
		if !o.ReloadPanels {
			n, err := countRows(raw, o.RawDB, "gene_panel", "study_id = ''")
			if err != nil {
				return nil, err
			}
			p.LoadPanels = n == 0
		}
	}
	for _, dir := range dirs {
		ps := PlannedStudy{Dir: dir, Action: "skip"}
		if _, err := os.Stat(filepath.Join(dir, "meta_study.txt")); err != nil {
			ps.Reason = "no meta_study.txt"
		} else if ps.StudyID, err = StudyID(dir); err != nil {
			ps.Reason = err.Error()
		} else {
			tables, err := ingest.StudyTables(raw, ps.StudyID)
			if err != nil {
				return nil, err
			}
			ps.Action, ps.Tables = "import", len(tables)
			if len(tables) > 0 {
				ps.Action = "replace"
			}
			p.Transform = true
		}
		p.Studies = append(p.Studies, ps)
	}
	p.Transform = p.Transform || p.LoadPanels
	return p, nil
}

// stage ingests one study directory, removing it from RawDB on failure.
func (o *Options) stage(raw *ch.Client, dir string) StudyResult {
	r := StudyResult{Dir: dir}
	if o.Done != nil {
		defer o.Done(dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta_study.txt")); err != nil {
		o.logf("  no meta_study.txt, skipped")
		r.Status, r.Err = Skipped, err
		return r
	}
	r.StudyID, _ = StudyID(dir)
	start := time.Now()
	err := func() error {
		if o.Prepare != nil {
			if err := o.Prepare(dir); err != nil {
				return err
			}
		}
		s, err := parser.Scan(dir)
		if err != nil {
			return err
		}
		r.StudyID = s.StudyID
		_, err = ingest.IngestStudy(raw, s)
		return err
	}()
	r.Duration = time.Since(start)
	if err == nil {
		r.Status = Imported
		o.logf("  staged %s in %.0fs", r.StudyID, r.Duration.Seconds())
		return r
	}
	r.Status, r.Err = Failed, err
	o.logf("  staging failed: %v", err)
	if r.StudyID != "" {
		o.logf("  removing %s from the portal", r.StudyID)
		if _, rmErr := ingest.RemoveStudy(raw, r.StudyID); rmErr != nil {
			o.logf("  removing %s: %v", r.StudyID, rmErr)
		}
	}
	return r
}

// ensureReference loads the seed into RefDB when it has no genes.
func (o *Options) ensureReference() error {
	n, err := countRows(ch.New(o.URL, o.RefDB), o.RefDB, "gene", "")
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if o.Seed == "" {
		return fmt.Errorf("reference database %s has no genes: pass a seed dump to load them", o.RefDB)
	}
	o.logf("loading reference data from %s", o.Seed)
	if _, _, err := reference.LoadReference(ch.New(o.URL, o.RefDB), o.Seed); err != nil {
		return fmt.Errorf("loading the reference data: %w", err)
	}
	return nil
}

// ensurePanels loads the shared gene panels when asked to, or when RawDB
// has none yet and a directory is given.
func (o *Options) ensurePanels(raw *ch.Client, res *Result) error {
	if o.GenePanels == "" {
		return nil
	}
	if !o.ReloadPanels {
		n, err := countRows(raw, o.RawDB, "gene_panel", "study_id = ''")
		if err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	o.logf("loading the shared gene panels from %s", o.GenePanels)
	if o.Prepare != nil {
		if err := o.Prepare(o.GenePanels); err != nil {
			return err
		}
	}
	if _, _, err := ingest.LoadGenePanelsDir(raw, o.GenePanels); err != nil {
		return err
	}
	res.PanelsLoaded = true
	return nil
}

// countRows counts the rows of db.table matching where (all when empty);
// 0 when the table does not exist.
func countRows(c *ch.Client, db, table, where string) (int64, error) {
	text, err := c.ExecuteServerwide(fmt.Sprintf(
		"SELECT count() FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TSV", ch.SQLLit(db), ch.SQLLit(table)))
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(text) == "0" {
		return 0, nil
	}
	q := fmt.Sprintf("SELECT count() FROM `%s`.`%s`", db, table)
	if where != "" {
		q += " WHERE " + where
	}
	text, err = c.ExecuteServerwide(q + " FORMAT TSV")
	if err != nil {
		return 0, err
	}
	var n int64
	_, err = fmt.Sscan(strings.TrimSpace(text), &n)
	return n, err
}

// StudyID returns the cancer_study_identifier of a study directory's
// meta_study.txt.
func StudyID(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "meta_study.txt"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "cancer_study_identifier:"); ok {
			return strings.TrimSpace(v), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s/meta_study.txt has no cancer_study_identifier", dir)
}
