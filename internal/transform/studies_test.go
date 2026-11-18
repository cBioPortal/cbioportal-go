package transform

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/javaorder"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

// TestLiveStudyBuild checks that a build of the changed studies alone
// gives the portal a full build of the same staged studies gives it, up to
// the values of the internal ids (a study build keeps the unchanged
// studies' ids and numbers the changed ones after them, a full build
// numbers all from 1): after a study is staged again, after one is
// removed, and after one is staged with new columns; and that a table
// whose columns changed makes it a full build.
func TestLiveStudyBuild(t *testing.T) {
	base := goldenServer(t)
	refDB := goldenReference(t, base, "sb_ref")
	rawDB, db := goldenDatabase(t, base, "sb_raw"), goldenDatabase(t, base, "sb")
	evo := t.TempDir()
	copyDir(t, filepath.Join(goldenDir, "evolution", "v1"), evo)
	for _, dir := range []string{
		filepath.Join(goldenDir, "studies", "golden_a"), filepath.Join(goldenDir, "studies", "golden_b"),
		filepath.Join(goldenDir, "studies", "golden_c"), evo,
	} {
		ingestGolden(t, base, rawDB, dir)
	}

	var mu sync.Mutex
	var logged []string
	Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { Logf = nil })
	apply := func(want string) {
		t.Helper()
		logged = nil
		if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, want) }) {
			t.Fatalf("build logged %q, want %q", logged, want)
		}
	}
	// A full build of the same staged studies, in a database of its own.
	compareFull := func(step string) {
		t.Helper()
		fresh := goldenDatabase(t, base, "sb_fresh")
		if _, err := Apply(ch.New(base, rawDB), rawDB, fresh, refDB); err != nil {
			t.Fatal(err)
		}
		got, want := idOrderDump(t, base, db), idOrderDump(t, base, fresh)
		for name, body := range want {
			if got[name] != body {
				t.Errorf("%s: %s differs from a full build:\n%s", step, name, diffLines(body, got[name]))
			}
		}
		for name := range got {
			if _, ok := want[name]; !ok {
				t.Errorf("%s: %s is not in a full build", step, name)
			}
		}
	}

	apply("full build")
	ingestGolden(t, base, rawDB, filepath.Join(goldenDir, "studies", "golden_b"))
	apply("rebuilding 1 changed and 0 removed of 4 studies: golden_b")
	compareFull("golden_b staged again")

	if _, err := ingest.RemoveStudy(ch.New(base, rawDB), "golden_a"); err != nil {
		t.Fatal(err)
	}
	apply("rebuilding 0 changed and 1 removed of 3 studies: golden_a")
	compareFull("golden_a removed")

	copyDir(t, filepath.Join(goldenDir, "evolution", "v2"), evo)
	ingestGolden(t, base, rawDB, evo)
	apply("rebuilding 1 changed and 0 removed of 3 studies: golden_evo")
	compareFull("golden_evo staged with new columns")

	// When a table's columns differ from what the build makes, every
	// study is built.
	if _, err := ch.New(base, db).Execute("ALTER TABLE `" + db + "`.sample_derived ADD COLUMN extra String"); err != nil {
		t.Fatal(err)
	}
	ingestGolden(t, base, rawDB, filepath.Join(goldenDir, "studies", "golden_c"))
	apply("transform: full build: a study build could not build every table")
	compareFull("sample_derived with other columns")

	// A changed fingerprint (here: the shared gene panels) builds every
	// study.
	if _, err := ch.New(base, rawDB).Execute(fmt.Sprintf(
		"INSERT INTO `%s`.gene_panel (study_id, gene_panel_id, description, hugo_symbol) VALUES ('', 'SHARED_X', '', 'TP53')", rawDB)); err != nil {
		t.Fatal(err)
	}
	apply("full build: the transform code, reference data or shared gene panels changed")

	// The build records the version of the transform that made it.
	text, err := ch.New(base, db).Execute("SELECT transform_version FROM `" + db + "`._transform_build FORMAT TabSeparated")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(text); got != sourceHash() {
		t.Errorf("transform_version = %q, want %q", got, sourceHash())
	}
}

// TestSourceHash checks that the transform's version covers the Go files of
// each package that decides what it builds, and leaves out their tests.
func TestSourceHash(t *testing.T) {
	if len(sourceHash()) != 64 {
		t.Fatalf("sourceHash() = %q", sourceHash())
	}
	for _, src := range []fs.FS{Source, ch.Source, parser.Source, javaorder.Source} {
		files, err := fs.Glob(src, "*.go")
		if err != nil || len(files) < 2 {
			t.Errorf("embedded files %v, %v", files, err)
		}
	}
	if _, err := fs.Stat(Source, "studies.go"); err != nil {
		t.Errorf("transform source: %v", err)
	}
}

// TestLiveBatchedBuild checks that a full build that fills the tables a
// batch of studies at a time gives the portal the same tables as one that
// fills them in one go.
func TestLiveBatchedBuild(t *testing.T) {
	base := goldenServer(t)
	refDB := goldenReference(t, base, "bb_ref")
	rawDB := goldenDatabase(t, base, "bb_raw")
	evo := t.TempDir()
	copyDir(t, filepath.Join(goldenDir, "evolution", "v1"), evo)
	for _, dir := range []string{
		filepath.Join(goldenDir, "studies", "golden_a"), filepath.Join(goldenDir, "studies", "golden_b"),
		filepath.Join(goldenDir, "studies", "golden_c"), evo,
	} {
		ingestGolden(t, base, rawDB, dir)
	}
	whole, batched := goldenDatabase(t, base, "bb_whole"), goldenDatabase(t, base, "bb_batched")
	if _, err := Apply(ch.New(base, rawDB), rawDB, whole, refDB); err != nil {
		t.Fatal(err)
	}
	var logged []string
	Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	defer func(n int) { batchStudies, Logf = n, nil }(batchStudies)
	batchStudies = 1
	if _, err := Apply(ch.New(base, rawDB), rawDB, batched, refDB); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(logged, "transform: filling the tables in 4 batches of up to 1 studies") {
		t.Fatalf("build logged %q, want 4 batches", logged)
	}
	got, want := dump(t, base, batched), dump(t, base, whole)
	delete(got, "_objects")
	delete(want, "_objects")
	for name, body := range want {
		if got[name] != body {
			t.Errorf("%s differs from a build in one go:\n%s", name, diffLines(body, got[name]))
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s is not in a build in one go", name)
		}
	}
}

// idOrderDump is dump with every internal id column replaced by the id's
// position among the column's ids (per type in clinical_data_derived,
// whose internal_id is a sample's or a patient's).
func idOrderDump(t *testing.T, base, db string) map[string]string {
	t.Helper()
	c := ch.New(base, db)
	scratch := db + "_ids"
	admin := ch.New(base, "default")
	for _, q := range []string{"DROP DATABASE IF EXISTS `" + scratch + "`", "CREATE DATABASE `" + scratch + "`"} {
		if _, err := admin.ExecuteServerwide(q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { admin.ExecuteServerwide("DROP DATABASE IF EXISTS `" + scratch + "`") })
	text, err := c.Execute(fmt.Sprintf(
		"SELECT table, groupArray(name), countIf(name = 'type') FROM system.columns WHERE database = '%s' "+
			"AND name IN ('internal_id', 'patient_internal_id', 'seg_id', 'list_internal_id', 'type') GROUP BY table FORMAT TabSeparated", db))
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for _, line := range lines(text) {
		f := strings.Split(line, "\t")
		var cols []string
		partition := ""
		if f[2] != "0" {
			partition = "PARTITION BY type "
		}
		for _, col := range strings.Split(strings.Trim(f[1], "[]"), ",") {
			col = strings.Trim(col, "'")
			if col != "type" {
				cols = append(cols, fmt.Sprintf("toUInt64(dense_rank() OVER (%sORDER BY `%s`)) AS `%s`", partition, col, col))
			}
		}
		if len(cols) == 0 {
			continue
		}
		if _, err := c.Execute(fmt.Sprintf("CREATE TABLE `%s`.`%s` ENGINE = Memory AS SELECT * REPLACE (%s) FROM `%s`.`%s`",
			scratch, f[0], strings.Join(cols, ", "), db, f[0])); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, f[0])
	}
	out := dump(t, base, db)
	for name, body := range dump(t, base, scratch) {
		if slices.Contains(tables, name) {
			out[name] = body
		}
	}
	delete(out, "_objects")
	return out
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
