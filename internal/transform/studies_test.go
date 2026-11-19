package transform

import (
	"errors"
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

	// A new release of the transform keeps the tables it would build the
	// same, after a study build too.
	release := func(version string) {
		prev := sourceHash
		sourceHash = func() string { return version }
		t.Cleanup(func() { sourceHash = prev })
	}
	release("release-1")
	apply("full build: the transform code, reference data or shared gene panels changed")
	keptTables := func() []string {
		for _, l := range logged {
			if _, names, ok := strings.Cut(l, "unchanged tables: "); ok {
				return strings.Split(names, ", ")
			}
		}
		return nil
	}
	for _, name := range []string{"sample", "patient", "gene_key_map", "genetic_alteration_derived", "clinical_data_derived", "mutation"} {
		if !slices.Contains(keptTables(), name) {
			t.Errorf("release-1 rebuilt %s, kept %v", name, keptTables())
		}
	}
	compareFull("release-1")

	// A release that changes a table builds it again, and the tables that
	// read it.
	hashSalt = func(name string) string {
		if name == "gene_key_map" {
			return "changed"
		}
		return ""
	}
	t.Cleanup(func() { hashSalt = nil })
	release("release-2")
	apply("full build: the transform code, reference data or shared gene panels changed")
	kept := keptTables()
	// Mutations are matched to genes through gene_key_map, and the
	// mutation count in clinical_data_derived counts them.
	for _, name := range []string{"gene_key_map", "genetic_alteration_derived", "mutation", "clinical_data_derived"} {
		if slices.Contains(kept, name) {
			t.Errorf("release-2 kept %s", name)
		}
	}
	for _, name := range []string{"sample", "patient", "clinical_event_derived"} {
		if !slices.Contains(kept, name) {
			t.Errorf("release-2 rebuilt %s, kept %v", name, kept)
		}
	}
	compareFull("release-2")
	ingestGolden(t, base, rawDB, filepath.Join(goldenDir, "studies", "golden_b"))
	apply("rebuilding 1 changed and 0 removed of 3 studies: golden_b")
	compareFull("golden_b staged again after release-2")

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

// TestLiveResourceTCGABarcodes checks that resource rows naming samples and
// patients by longer TCGA barcodes are stored under the ids of the
// study's samples and patients, as ImportResourceData reads them through
// StableIdUtil.
func TestLiveResourceTCGABarcodes(t *testing.T) {
	base := goldenServer(t)
	refDB := goldenReference(t, base, "rt_ref")
	rawDB, db := goldenDatabase(t, base, "rt_raw"), goldenDatabase(t, base, "rt")
	dir := t.TempDir()
	files := map[string]string{
		"meta_study.txt": "type_of_cancer: coad\ncancer_study_identifier: rt_tcga\nname: TCGA resources\n" +
			"short_name: RT\ndescription: d\nreference_genome: hg19\n",
		"meta_clinical_sample.txt": "cancer_study_identifier: rt_tcga\ngenetic_alteration_type: CLINICAL\n" +
			"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
		"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\n#p\t#s\n#STRING\tSTRING\n#1\t1\n" +
			"PATIENT_ID\tSAMPLE_ID\nTCGA-3L-AA1B\tTCGA-3L-AA1B-01\n",
		"meta_resource_definition.txt": "cancer_study_identifier: rt_tcga\nresource_type: DEFINITION\n" +
			"data_filename: data_resource_definition.txt\n",
		"data_resource_definition.txt": "RESOURCE_ID\tDISPLAY_NAME\tDESCRIPTION\tRESOURCE_TYPE\tOPEN_BY_DEFAULT\tPRIORITY\n" +
			"SLIDE\tSlide\td\tSAMPLE\ttrue\t1\nNOTES\tNotes\td\tPATIENT\tfalse\t2\n",
		"meta_resource_sample.txt": "cancer_study_identifier: rt_tcga\nresource_type: SAMPLE\n" +
			"data_filename: data_resource_sample.txt\n",
		"data_resource_sample.txt": "PATIENT_ID\tSAMPLE_ID\tRESOURCE_ID\tURL\nTCGA-3L-AA1B\tTCGA-3L-AA1B-01A\tSLIDE\thttp://slide\n",
		"meta_resource_patient.txt": "cancer_study_identifier: rt_tcga\nresource_type: PATIENT\n" +
			"data_filename: data_resource_patient.txt\n",
		"data_resource_patient.txt": "PATIENT_ID\tRESOURCE_ID\tURL\nTCGA-3L-AA1B-01A\tNOTES\thttp://notes\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ingestGolden(t, base, rawDB, dir)
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	got, err := ch.New(base, db).Execute("SELECT resource_scope, patient_id, sample_id FROM `" + db +
		"`.resource ORDER BY resource_scope FORMAT TabSeparated")
	if err != nil {
		t.Fatal(err)
	}
	if want := "PATIENT\tTCGA-3L-AA1B\t\\N\nSAMPLE\tTCGA-3L-AA1B\tTCGA-3L-AA1B-01\n"; got != want {
		t.Errorf("resource rows:\n%s\nwant:\n%s", got, want)
	}
}

// TestLiveInvalidSegments checks that copy-number segments whose start is
// not below their end are left out, as ImportCopyNumberSegmentData skips
// them: from the segments, the fraction of genome altered, and whether a
// sample has segments.
func TestLiveInvalidSegments(t *testing.T) {
	base := goldenServer(t)
	refDB := goldenReference(t, base, "seg_ref")
	rawDB, db := goldenDatabase(t, base, "seg_raw"), goldenDatabase(t, base, "seg")
	dir := t.TempDir()
	files := map[string]string{
		"meta_study.txt": "type_of_cancer: coad\ncancer_study_identifier: seg_study\nname: Segments\nshort_name: S\ndescription: d\nreference_genome: hg19\n",
		"meta_clinical_sample.txt": "cancer_study_identifier: seg_study\ngenetic_alteration_type: CLINICAL\n" +
			"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
		"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\n#p\t#s\n#STRING\tSTRING\n#1\t1\nPATIENT_ID\tSAMPLE_ID\nP1\tS1\nP2\tS2\n",
		"meta_cna_seg.txt": "cancer_study_identifier: seg_study\ngenetic_alteration_type: COPY_NUMBER_ALTERATION\ndatatype: SEG\n" +
			"reference_genome_id: hg19\ndescription: Segments\ndata_filename: data_cna.seg\n",
		// S1: one segment, and one that ends where it starts; S2: one that
		// ends before it starts (whole numbers, as Java reads them).
		"data_cna.seg": "ID\tchrom\tloc.start\tloc.end\tnum.mark\tseg.mean\n" +
			"S1\t1\t100\t1000\t10\t0.5\nS1\t1\t2000.2\t2000.9\t5\t0.1\nS2\t1\t500\t400\t5\t1.0\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ingestGolden(t, base, rawDB, dir)
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	c := ch.New(base, db)
	for _, q := range []struct{ sql, want string }{
		{"SELECT sample_id, start, end FROM seg ORDER BY sample_id, start", "S1\t100\t1000"},
		{"SELECT sample_id, attr_value FROM clinical_sample WHERE attr_id = 'FRACTION_GENOME_ALTERED' ORDER BY sample_id", "S1\t1.0"},
		{"SELECT sample_stable_id, copy_number_segment_present FROM sample_derived ORDER BY sample_stable_id", "S1\t1\nS2\t0"},
	} {
		got, err := c.Execute(q.sql + " FORMAT TabSeparated")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(got) != q.want {
			t.Errorf("%s:\n%s\nwant:\n%s", q.sql, got, q.want)
		}
	}
}

// TestLiveDuplicateMatrixLines checks that a matrix gene listed on more
// than one line, with the very same symbol and Entrez id, is stored once
// from its first line, as ImportTabDelimData skips the later ones: in
// genetic_alteration_derived and in the discrete copy-number events.
func TestLiveDuplicateMatrixLines(t *testing.T) {
	base := goldenServer(t)
	refDB := goldenReference(t, base, "dup_ref")
	rawDB, db := goldenDatabase(t, base, "dup_raw"), goldenDatabase(t, base, "dup")
	dir := t.TempDir()
	files := map[string]string{
		"meta_study.txt": "type_of_cancer: coad\ncancer_study_identifier: dup_study\nname: Duplicates\nshort_name: D\ndescription: d\nreference_genome: hg19\n",
		"meta_clinical_sample.txt": "cancer_study_identifier: dup_study\ngenetic_alteration_type: CLINICAL\n" +
			"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
		"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\n#p\t#s\n#STRING\tSTRING\n#1\t1\nPATIENT_ID\tSAMPLE_ID\nP1\tS1\nP2\tS2\n",
		"meta_cna.txt": "cancer_study_identifier: dup_study\ngenetic_alteration_type: COPY_NUMBER_ALTERATION\ndatatype: DISCRETE\n" +
			"stable_id: gistic\nshow_profile_in_analysis_tab: true\nprofile_name: CNA\nprofile_description: CNA\ndata_filename: data_cna.txt\n",
		// TP53 twice with the same symbol and Entrez id, on two cytobands
		// (as datahub's copy-number files list some genes): the first line
		// wins.
		"data_cna.txt": "Hugo_Symbol\tEntrez_Gene_Id\tCytoband\tS1\tS2\nTP53\t7157\t17p13.1\t-2\t0\n" +
			"KRAS\t3845\t12p12.1\t2\t1\nTP53\t7157\t5q23.1\t2\t-1\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ingestGolden(t, base, rawDB, dir)
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	c := ch.New(base, db)
	for _, q := range []struct{ sql, want string }{
		{"SELECT sample_unique_id, toString(hugo_gene_symbol), alteration_value FROM genetic_alteration_derived ORDER BY 2, 1",
			"dup_study_S1\tKRAS\t2\ndup_study_S2\tKRAS\t1\ndup_study_S1\tTP53\t-2\ndup_study_S2\tTP53\t0"},
		{"SELECT sample_id, hugo_symbol, alteration FROM cna ORDER BY hugo_symbol, sample_id",
			"S1\tKRAS\t2\nS2\tKRAS\t1\nS1\tTP53\t-2\nS2\tTP53\t0"},
	} {
		got, err := c.Execute(q.sql + " FORMAT TabSeparated")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(got) != q.want {
			t.Errorf("%s:\n%s\nwant:\n%s", q.sql, got, q.want)
		}
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

// TestLiveResumedBuild checks that a full build that stopped part of the
// way (here: in the middle of a table filled batch by batch) is resumed by
// the next build of the same staged studies, and gives the same tables as
// a build that ran through; and that a build of other inputs starts over.
func TestLiveResumedBuild(t *testing.T) {
	base := goldenServer(t)
	refDB := goldenReference(t, base, "rb_ref")
	rawDB := goldenDatabase(t, base, "rb_raw")
	for _, study := range []string{"golden_a", "golden_b", "golden_c"} {
		ingestGolden(t, base, rawDB, filepath.Join(goldenDir, "studies", study))
	}
	defer func(n int) { batchStudies, stepHook, Logf = n, nil, nil }(batchStudies)
	batchStudies = 1
	whole, resumed := goldenDatabase(t, base, "rb_whole"), goldenDatabase(t, base, "rb_resumed")
	if _, err := Apply(ch.New(base, rawDB), rawDB, whole, refDB); err != nil {
		t.Fatal(err)
	}

	stop := errors.New("stopped")
	stepHook = func(step string) error {
		if step == "batch:genetic_alteration_derived:1" {
			return stop
		}
		return nil
	}
	if _, err := Apply(ch.New(base, rawDB), rawDB, resumed, refDB); !errors.Is(err, stop) {
		t.Fatalf("stopped build: %v", err)
	}
	stepHook = nil
	var logged []string
	Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	if _, err := Apply(ch.New(base, rawDB), rawDB, resumed, refDB); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, "transform: resuming the previous build") }) {
		t.Fatalf("build logged %q, want it to resume", logged)
	}
	got, want := dump(t, base, resumed), dump(t, base, whole)
	for name, body := range want {
		if name != "_objects" && got[name] != body {
			t.Errorf("%s differs from a build that ran through:\n%s", name, diffLines(body, got[name]))
		}
	}

	// A build of other inputs does not resume an unfinished one. Staging
	// two of the three studies again makes a full build.
	ingestGolden(t, base, rawDB, filepath.Join(goldenDir, "studies", "golden_a"))
	ingestGolden(t, base, rawDB, filepath.Join(goldenDir, "studies", "golden_b"))
	stepHook = func(step string) error {
		if step == "batch:genetic_alteration_derived:1" {
			return stop
		}
		return nil
	}
	if _, err := Apply(ch.New(base, rawDB), rawDB, resumed, refDB); !errors.Is(err, stop) {
		t.Fatalf("stopped build: %v", err)
	}
	stepHook = nil
	ingestGolden(t, base, rawDB, filepath.Join(goldenDir, "studies", "golden_c"))
	logged = nil
	if _, err := Apply(ch.New(base, rawDB), rawDB, resumed, refDB); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, "transform: resuming") }) {
		t.Errorf("a build of other inputs resumed: %q", logged)
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
