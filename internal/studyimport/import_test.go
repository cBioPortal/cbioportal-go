package studyimport

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
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

	// Studies staged at once rank in the order they were given, whichever
	// finishes first.
	o.Prepare = nil
	studies := []string{"golden_c", "golden_b", "golden_a"}
	dirs := make([]string, len(studies))
	for i, s := range studies {
		dirs[i] = filepath.Join(golden, "studies", s)
	}
	if res, err = Run(o, dirs); err != nil || res.Count(Imported) != len(dirs) || res.TransformErr != nil {
		t.Fatalf("three studies: %+v, %v", res, err)
	}
	for i, r := range res.Studies {
		if r.StudyID != studies[i] {
			t.Errorf("result %d is %s, want %s", i, r.StudyID, studies[i])
		}
	}
	want := strings.Join(studies, ",")
	if got := count("SELECT arrayStringConcat(groupArray(study_id), ',') FROM (SELECT study_id FROM `" + raw +
		"`.cancer_study ORDER BY import_time)"); got != want {
		t.Errorf("studies by import time %s, want %s", got, want)
	}
	if got := count("SELECT arrayStringConcat(groupArray(study_id), ',') FROM (SELECT study_id, min(internal_id) AS i FROM `" +
		db + "`.sample GROUP BY study_id ORDER BY i)"); got != want {
		t.Errorf("studies by sample internal id %s, want %s", got, want)
	}

	// A portal built by another version of the transform is rebuilt
	// before the studies are staged: that build keeps the tables, and the
	// import is then a study build.
	if _, err := c.Execute("TRUNCATE TABLE `" + db + "`._transform_build"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute("INSERT INTO `" + db + "`._transform_build VALUES ('old', 'old')"); err != nil {
		t.Fatal(err)
	}
	var logged []string
	o.Logf = func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
		t.Logf(format, args...)
	}
	if res, err = Run(o, dirs[1:2]); err != nil || res.Count(Imported) != 1 || res.TransformErr != nil {
		t.Fatalf("new version: %+v, %v", res, err)
	}
	for _, step := range []string{"new version, rebuilding the portal before staging", "kept", "staged golden_b",
		"rebuilding 1 changed and 0 removed of 3 studies: golden_b"} {
		i := slices.IndexFunc(logged, func(l string) bool { return strings.Contains(l, step) })
		if i < 0 {
			t.Fatalf("no %q in %q", step, logged)
		}
		logged = logged[i+1:]
	}

	// When the raw database no longer holds the studies the published build
	// was made from, that build could keep nothing, so the import builds
	// once.
	for _, q := range []string{"TRUNCATE TABLE `" + db + "`._transform_build", "INSERT INTO `" + db + "`._transform_build VALUES ('old', 'old')"} {
		if _, err := c.Execute(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ingest.RemoveStudy(ch.New(base, raw), "golden_a"); err != nil {
		t.Fatal(err)
	}
	logged = nil
	if res, err = Run(o, dirs[1:2]); err != nil || res.Count(Imported) != 1 || res.TransformErr != nil {
		t.Fatalf("new version, studies changed: %+v, %v", res, err)
	}
	if slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, "rebuilding the portal before staging") }) {
		t.Errorf("rebuilt before staging although the studies changed: %q", logged)
	}
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, "full build") }) {
		t.Errorf("no full build: %q", logged)
	}
}
