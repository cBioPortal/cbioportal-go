package studyimport

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

func TestStudyID(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "meta_study.txt"), []byte("type_of_cancer: brca\r\ncancer_study_identifier: brca_x \r\nname: X\n"), 0o644)
	if id, err := StudyID(dir); id != "brca_x" || err != nil {
		t.Errorf("got %q, %v", id, err)
	}
	if _, err := StudyID(t.TempDir()); err == nil {
		t.Error("no meta_study.txt: no error")
	}
}

const golden = "../transform/testdata/golden"

// goldenSeed gzips the golden seed slice for LoadReference.
func goldenSeed(t *testing.T) string {
	sql, err := os.ReadFile(filepath.Join(golden, "reference", "seed.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(sql)
	w.Close()
	path := filepath.Join(t.TempDir(), "seed.sql.gz")
	if err := os.WriteFile(path, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLiveRun(t *testing.T) {
	base, db := liveDB(t, "import")
	_, raw := liveDB(t, "import_raw")
	_, ref := liveDB(t, "import_ref")
	o := Options{URL: base, DB: db, RawDB: raw, RefDB: ref, Logf: t.Logf}
	studyA := filepath.Join(golden, "studies", "golden_a")

	if _, err := Run(o, []string{studyA}); err == nil || !strings.Contains(err.Error(), "no genes") {
		t.Fatalf("empty reference database without a seed: %v", err)
	}

	o.Seed = goldenSeed(t)
	var prepared, done []string
	o.Prepare = func(dir string) error { prepared = append(prepared, dir); return nil }
	o.Done = func(dir string) { done = append(done, dir) }
	res, err := Run(o, []string{studyA, t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Count(Imported) != 1 || res.Count(Skipped) != 1 || res.TransformErr != nil {
		t.Fatalf("result %+v", res)
	}
	if len(prepared) != 1 || len(done) != 2 {
		t.Errorf("prepared %q, done %q", prepared, done)
	}
	c := ch.New(base, db)
	count := func(q string) string {
		out, err := c.Execute(q)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	samples := count("SELECT count() FROM `" + db + "`.sample_derived")
	if samples == "0" {
		t.Fatal("no samples after the import")
	}
	if n := count("SELECT count() FROM system.tables WHERE database = '" + db + "' AND startsWith(name, '__next_')"); n != "0" {
		t.Errorf("%s staged objects left after publishing", n)
	}

	// A study that cannot be staged is removed from the portal, and the
	// rest of the portal stays.
	o.Prepare = func(string) error { return os.ErrNotExist }
	res, err = Run(o, []string{studyA})
	if err != nil {
		t.Fatal(err)
	}
	if res.Count(Failed) != 1 || res.Studies[0].StudyID != "golden_a" {
		t.Fatalf("broken study: %+v", res.Studies)
	}
	if n := count("SELECT count() FROM `" + db + "`.sample_derived WHERE cancer_study_identifier = 'golden_a'"); n != "0" {
		t.Errorf("%s samples of the failed study still served", n)
	}
}
