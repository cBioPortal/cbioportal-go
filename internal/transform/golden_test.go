package transform

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/parser"
	"github.com/cbioportal/cbioportal-go/internal/reference"
)

// Golden tests: tiny fixture studies (testdata/golden/studies) and a slice
// of a Java-schema database (testdata/golden/prod) are transformed on the
// ClickHouse server named by CLICKHOUSE_TEST_URL, and every object of the
// resulting databases is compared with its dump under
// testdata/golden/expected. -update rewrites the dumps.
var (
	updateGolden = flag.Bool("update", false, "rewrite the golden dumps under testdata/golden/expected")
	extractProd  = flag.String("extract-prod", "",
		"rewrite testdata/golden/prod from this Java-schema `database` (read only, e.g. java_e2e) before the from-prod golden test")
)

const goldenDir = "testdata/golden"

func goldenServer(t *testing.T) string {
	t.Helper()
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	return base
}

// goldenDatabase names a database for this test process, dropped before
// and after the test.
func goldenDatabase(t *testing.T, base, suffix string) string {
	t.Helper()
	db := fmt.Sprintf("cbioportal_go_test_tx_%d_%s", os.Getpid(), suffix)
	admin := ch.New(base, "default")
	if _, err := admin.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`") })
	return db
}

// goldenReference loads the seed slice testdata/golden/reference/seed.sql
// into a new reference database.
func goldenReference(t *testing.T, base, suffix string) string {
	t.Helper()
	db := goldenDatabase(t, base, suffix)
	sql, err := os.ReadFile(filepath.Join(goldenDir, "reference", "seed.sql"))
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
	if _, _, err := reference.LoadReference(ch.New(base, db), path); err != nil {
		t.Fatal(err)
	}
	return db
}

// ingestGolden ingests a study directory. The importer takes a directory's
// entries in the file system's order, which differs between machines, so
// the recorded import order is replaced by the order of the names.
func ingestGolden(t *testing.T, base, rawDB, dir string) {
	t.Helper()
	st, err := parser.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := ch.New(base, rawDB)
	if _, err := ingest.IngestStudy(c, st); err != nil {
		t.Fatalf("ingest %s: %v", dir, err)
	}
	for _, q := range []string{
		"CREATE TABLE `{r}`.__import_order ENGINE = Memory AS SELECT study_id, kind, name, " +
			"toUInt32(row_number() OVER (PARTITION BY study_id, kind ORDER BY name)) AS seq FROM `{r}`.import_order",
		"TRUNCATE TABLE `{r}`.import_order",
		"INSERT INTO `{r}`.import_order SELECT * FROM `{r}`.__import_order",
		"DROP TABLE `{r}`.__import_order",
	} {
		if _, err := c.Execute(strings.ReplaceAll(q, "{r}", rawDB)); err != nil {
			t.Fatal(err)
		}
	}
}

// dump renders every table and view of db (or those listed in only): per
// object a TSV of its columns in name order (a line of names, a line of
// types, then the rows, sorted), and "_objects" listing each object's
// engine.
func dump(t *testing.T, base, db string, only ...string) map[string]string {
	t.Helper()
	c := ch.New(base, db)
	query := func(sql string) string {
		t.Helper()
		out, err := c.Execute(sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}
	out := map[string]string{}
	var objects []string
	for _, line := range lines(query(fmt.Sprintf(
		"SELECT name, engine FROM system.tables WHERE database = '%s' ORDER BY name FORMAT TabSeparated", db))) {
		name, _, _ := strings.Cut(line, "\t")
		if len(only) > 0 && !slices.Contains(only, name) {
			continue
		}
		objects = append(objects, line)
		var names, types, cols []string
		for _, col := range lines(query(fmt.Sprintf(
			"SELECT name, type FROM system.columns WHERE database = '%s' AND table = '%s' ORDER BY name FORMAT TabSeparated", db, name))) {
			n, typ, _ := strings.Cut(col, "\t")
			names, types, cols = append(names, n), append(types, typ), append(cols, "`"+n+"`")
		}
		rows := lines(query(fmt.Sprintf("SELECT %s FROM `%s`.`%s` FORMAT TabSeparated", strings.Join(cols, ", "), db, name)))
		slices.Sort(rows)
		var b strings.Builder
		b.WriteString(strings.Join(names, "\t") + "\n" + strings.Join(types, "\t") + "\n")
		for _, r := range rows {
			b.WriteString(r + "\n")
		}
		out[name] = b.String()
	}
	out["_objects"] = strings.Join(objects, "\n") + "\n"
	return out
}

func lines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// compareGolden compares a dump with the files of testdata/golden/expected/<dir>,
// or rewrites them with -update.
func compareGolden(t *testing.T, dir string, got map[string]string) {
	t.Helper()
	path := filepath.Join(goldenDir, "expected", dir)
	if *updateGolden {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range got {
			if err := os.WriteFile(filepath.Join(path, name+".tsv"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create the golden files)", err)
	}
	want := map[string]bool{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".tsv")
		want[name] = true
		body, err := os.ReadFile(filepath.Join(path, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		g, ok := got[name]
		switch {
		case !ok:
			t.Errorf("%s/%s: object not built", dir, name)
		case g != string(body):
			t.Errorf("%s/%s differs from the golden file:\n%s", dir, name, diffLines(string(body), g))
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("%s/%s: no golden file (run with -update)", dir, name)
		}
	}
}

// diffLines lists the lines only in want (-) and only in got (+).
func diffLines(want, got string) string {
	count := map[string]int{}
	for _, l := range lines(want) {
		count[l]++
	}
	for _, l := range lines(got) {
		count[l]--
	}
	var b strings.Builder
	for _, l := range lines(want) {
		if count[l] > 0 {
			count[l]--
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range lines(got) {
		if count[l] < 0 {
			count[l]++
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}

// TestLiveGoldenStaged ingests the fixture studies (every file type the
// importer stages) and compares every object the transform builds.
func TestLiveGoldenStaged(t *testing.T) {
	base := goldenServer(t)
	t.Parallel()
	refDB := goldenReference(t, base, "staged_ref")
	rawDB := goldenDatabase(t, base, "staged_raw")
	db := goldenDatabase(t, base, "staged")
	for _, study := range []string{"golden_a", "golden_b", "golden_c"} {
		ingestGolden(t, base, rawDB, filepath.Join(goldenDir, "studies", study))
	}
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	compareGolden(t, "staged", dump(t, base, db))
	// The transform adds the RPPA phosphoproteins to the reference genes,
	// once: a second run finds them there.
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	compareGolden(t, "staged_ref", dump(t, base, refDB, "gene", "gene_alias"))
}

// TestLiveGoldenSchemaEvolution re-ingests a study whose second version
// adds columns to staged tables (MAF, clinical, resource definition): the
// transform over the evolved staging tables equals the one over a fresh
// ingest of the second version, and the objects reading the new columns
// match their golden dumps.
func TestLiveGoldenSchemaEvolution(t *testing.T) {
	base := goldenServer(t)
	t.Parallel()
	refDB := goldenReference(t, base, "evo_ref")
	v1 := filepath.Join(goldenDir, "evolution", "v1")
	v2 := t.TempDir()
	for _, src := range []string{v1, filepath.Join(goldenDir, "evolution", "v2")} {
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			body, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(v2, e.Name()), body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	rawDB, db := goldenDatabase(t, base, "evo_raw"), goldenDatabase(t, base, "evo")
	ingestGolden(t, base, rawDB, v1)
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	before := dump(t, base, rawDB, "mutation_extended")["mutation_extended"]
	ingestGolden(t, base, rawDB, v2)
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	after := dump(t, base, rawDB, "mutation_extended")["mutation_extended"]
	for _, col := range []string{"hgvsp_short", "cbp_driver", "zygosity_status", "license"} {
		if strings.Contains(strings.SplitN(before, "\n", 2)[0], col) || !strings.Contains(strings.SplitN(after, "\n", 2)[0], col) {
			t.Errorf("mutation_extended: column %s not added by the re-ingest", col)
		}
	}

	freshRaw, fresh := goldenDatabase(t, base, "evo_fresh_raw"), goldenDatabase(t, base, "evo_fresh")
	ingestGolden(t, base, freshRaw, v2)
	if _, err := Apply(ch.New(base, freshRaw), freshRaw, fresh, refDB); err != nil {
		t.Fatal(err)
	}
	evolved, want := dump(t, base, db), dump(t, base, fresh)
	for name, body := range want {
		if evolved[name] != body {
			t.Errorf("%s after re-ingest differs from a fresh ingest:\n%s", name, diffLines(body, evolved[name]))
		}
	}
	compareGolden(t, "evolution", dump(t, base, db, "clinical_attribute_meta", "clinical_sample", "clinical_data_derived",
		"molecular_profile", "mutation", "genomic_event_derived", "resource_definition", "resource_sample"))
}

// TestLiveGoldenFromProd builds a database with the schema of cBioPortal's
// Java importer from testdata/golden/prod (a slice of three studies of the
// Java oracle database, java_e2e) and compares every object
// `transform --from-prod` builds in the portal and raw databases.
func TestLiveGoldenFromProd(t *testing.T) {
	base := goldenServer(t)
	t.Parallel()
	if *extractProd != "" {
		extractProdSlice(t, base, *extractProd)
	}
	testGoldenFromProd(t, base, "prod", nil)
}

// TestLiveGoldenFromProdUnifiedResources builds from the same slice moved to
// schema 3.1.0, which keeps every resource in one table, resource: the
// objects are the same.
func TestLiveGoldenFromProdUnifiedResources(t *testing.T) {
	base := goldenServer(t)
	t.Parallel()
	testGoldenFromProd(t, base, "prod31", func(c *ch.Client, db string) {
		for _, stmt := range []string{
			"CREATE TABLE {db}.resource (internal_id Int64, resource_id String, cancer_study_id Int64, " +
				"resource_scope String, patient_id Nullable(String), sample_id Nullable(String), url String, " +
				"display_name Nullable(String), metadata Nullable(String)) ENGINE = MergeTree ORDER BY (cancer_study_id, resource_id)",
			"INSERT INTO {db}.resource SELECT row_number() OVER (), r.resource_id, r.internal_id, 'STUDY', NULL, NULL, r.url, NULL, NULL " +
				"FROM {db}.resource_study r",
			"INSERT INTO {db}.resource SELECT 1000 + row_number() OVER (), r.resource_id, p.cancer_study_id, 'PATIENT', p.stable_id, NULL, r.url, NULL, NULL " +
				"FROM {db}.resource_patient r INNER JOIN {db}.patient p ON p.internal_id = r.internal_id",
			"INSERT INTO {db}.resource SELECT 100000 + row_number() OVER (), r.resource_id, p.cancer_study_id, 'SAMPLE', p.stable_id, s.stable_id, r.url, NULL, NULL " +
				"FROM {db}.resource_sample r INNER JOIN {db}.sample s ON s.internal_id = r.internal_id INNER JOIN {db}.patient p ON p.internal_id = s.patient_id",
			"DROP TABLE {db}.resource_study",
			"DROP TABLE {db}.resource_patient",
			"DROP TABLE {db}.resource_sample",
		} {
			if _, err := c.Execute(strings.ReplaceAll(stmt, "{db}", "`"+db+"`")); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// testGoldenFromProd loads the production slice into a database named by
// suffix, lets migrate change it, builds from it and compares the result
// with the fromprod golden files.
func testGoldenFromProd(t *testing.T, base, suffix string, migrate func(c *ch.Client, db string)) {
	t.Helper()
	prodDB := goldenDatabase(t, base, suffix)
	rawDB := goldenDatabase(t, base, suffix+"_raw")
	db := goldenDatabase(t, base, suffix+"_portal")
	c := ch.New(base, prodDB)
	if err := c.CreateDatabase(); err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(goldenDir, "prod", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(string(schema), ";\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := c.Execute(strings.ReplaceAll(stmt, "{db}", "`"+prodDB+"`")); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range prodSliceTables {
		body, err := os.ReadFile(filepath.Join(goldenDir, "prod", table.name+".tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(body), "\n") < 2 {
			continue
		}
		if err := c.InsertStream(fmt.Sprintf("INSERT INTO `%s`.`%s` FORMAT TabSeparatedWithNames", prodDB, table.name),
			bytes.NewReader(body)); err != nil {
			t.Fatalf("loading %s: %v", table.name, err)
		}
	}
	if migrate != nil {
		migrate(c, prodDB)
	}
	if _, err := ApplyFromProd(c, prodDB, rawDB, db); err != nil {
		t.Fatal(err)
	}
	compareGolden(t, "fromprod/portal", dump(t, base, db))
	compareGolden(t, "fromprod/raw", dump(t, base, rawDB))
}

// prodSliceTables are the Java-schema tables the from-prod transform reads,
// each with the condition selecting its rows of the slice: patients of
// three studies (ASCN, study_es_0's resources, structural variants and
// namespaces, the generic assay study's timelines), restricted to a few
// genes, two generic assay entities per profile and three segments per
// sample. {s} is the source database; P, SM (samples), SU / PU (unique
// sample / patient ids), G, GS (genes) and E (generic assay entities) the
// slice's keys.
var prodSliceTables = []struct{ name, where string }{
	{"cancer_study", "cancer_study_id IN S"},
	{"cancer_study_tags", "cancer_study_id IN S"},
	{"clinical_attribute_meta", "cancer_study_id IN S"},
	{"reference_genome", "1"},
	{"patient", "internal_id IN P"},
	{"sample", "patient_id IN P"},
	{"genetic_profile", "cancer_study_id IN S"},
	{"genetic_profile_samples", "genetic_profile_id IN (SELECT genetic_profile_id FROM {s}.genetic_profile WHERE cancer_study_id IN S)"},
	{"sample_list", "cancer_study_id IN S"},
	{"sample_list_list", "sample_id IN SM AND list_id IN (SELECT list_id FROM {s}.sample_list WHERE cancer_study_id IN S)"},
	{"gene_panel", "1"},
	{"gene_panel_list", "gene_id IN G"},
	{"gene", "entrez_gene_id IN G"},
	{"clinical_sample", "internal_id IN SM"},
	{"clinical_patient", "internal_id IN P"},
	{"sample_profile", "sample_id IN SM"},
	{"mutation_derived", "sampleInternalId IN SM AND entrezGeneId IN G"},
	{"genetic_alteration_derived", "sample_unique_id IN SU AND hugo_gene_symbol IN GS"},
	{"cna_event", "entrez_gene_id IN G AND cna_event_id IN (SELECT cna_event_id FROM {s}.sample_cna_event WHERE sample_id IN SM)"},
	{"sample_cna_event", "sample_id IN SM AND cna_event_id IN (SELECT cna_event_id FROM {s}.cna_event WHERE entrez_gene_id IN G)"},
	{"alteration_driver_annotation", "sample_id IN SM"},
	{"structural_variant", "sample_id IN SM"},
	{"copy_number_seg", "sample_id IN SM ORDER BY seg_id LIMIT 3 BY sample_id"},
	{"sample_derived", "internal_id IN SM"},
	{"clinical_data_derived", "patient_unique_id IN PU"},
	{"genomic_event_derived", "sample_unique_id IN SU AND (entrez_gene_id IN G OR hugo_gene_symbol IN GS)"},
	{"generic_assay_data_derived", "patient_unique_id IN PU AND (profile_stable_id, entity_stable_id) IN E"},
	{"generic_assay_profile_entity_derived", "(profile_stable_id, entity_stable_id) IN E"},
	{"generic_assay_meta_derived", "entity_stable_id IN (SELECT entity_stable_id FROM E)"},
	{"sample_to_gene_panel_derived", "sample_unique_id IN SU"},
	{"gene_panel_to_gene_derived", "gene IN GS"},
	{"clinical_event", "patient_id IN P"},
	{"clinical_event_data", "clinical_event_id IN (SELECT clinical_event_id FROM {s}.clinical_event WHERE patient_id IN P)"},
	{"clinical_event_derived", "patient_id IN P"},
	{"clinical_event_data_derived", "patient_unique_id IN PU"},
	{"resource_definition", "cancer_study_id IN S"},
	{"resource_study", "internal_id IN S"},
	{"resource_patient", "internal_id IN P"},
	{"resource_sample", "internal_id IN SM"},
	{"gistic", "cancer_study_id IN S"},
	{"gistic_to_gene", "gistic_roi_id IN (SELECT gistic_roi_id FROM {s}.gistic WHERE cancer_study_id IN S)"},
	{"mut_sig", "cancer_study_id IN S"},
}

// extractProdSlice rewrites testdata/golden/prod from the Java-schema
// database src: schema.sql (each table's DDL) and one TSV per table.
// genetic_profile_samples keeps only the slice's samples.
func extractProdSlice(t *testing.T, base, src string) {
	t.Helper()
	c := ch.New(base, src)
	keys := strings.NewReplacer(
		" IN SM", " IN (SELECT internal_id FROM {s}.sample WHERE patient_id IN P)",
		" IN SU", " IN (SELECT sample_unique_id FROM {s}.sample_derived WHERE patient_internal_id IN P)",
		" IN PU", " IN (SELECT DISTINCT patient_unique_id FROM {s}.sample_derived WHERE patient_internal_id IN P)",
		" IN GS", " IN (SELECT hugo_gene_symbol FROM {s}.gene WHERE entrez_gene_id IN G)",
		" IN E", " IN (SELECT profile_stable_id, entity_stable_id FROM {s}.generic_assay_profile_entity_derived "+
			"WHERE profile_stable_id IN (SELECT stable_id FROM {s}.genetic_profile WHERE cancer_study_id IN S) "+
			"ORDER BY profile_stable_id, entity_stable_id LIMIT 2 BY profile_stable_id)",
		"FROM E)", "FROM (SELECT profile_stable_id, entity_stable_id FROM {s}.generic_assay_profile_entity_derived "+
			"WHERE profile_stable_id IN (SELECT stable_id FROM {s}.genetic_profile WHERE cancer_study_id IN S) "+
			"ORDER BY profile_stable_id, entity_stable_id LIMIT 2 BY profile_stable_id))",
	)
	values := strings.NewReplacer(
		" IN S", " IN (1, 4, 5)",
		" IN P", " IN (1, 4, 578, 688, 844, 845, 850)",
		" IN G", " IN (238, 546, 672, 673, 675, 1956, 2064, 2073, 2078, 2115, 3009, 5295, 5774, 5979, 7113, 7157, 7273, 8031, 9780, 27436, 57670)",
		"{s}", "`"+src+"`",
	)
	dir := filepath.Join(goldenDir, "prod")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var schema strings.Builder
	schema.WriteString("-- The Java importer's tables the from-prod transform reads, from " + src + ".\n")
	for _, table := range prodSliceTables {
		ddl, err := c.Execute(fmt.Sprintf("SHOW CREATE TABLE `%s`.`%s` FORMAT TSVRaw", src, table.name))
		if err != nil {
			t.Fatal(err)
		}
		ddl = strings.Replace(strings.TrimSpace(ddl), "CREATE TABLE "+src+".", "CREATE TABLE {db}.", 1)
		schema.WriteString(ddl + ";\n")

		cols := "*"
		if table.name == "genetic_profile_samples" {
			cols = "genetic_profile_id, arrayStringConcat(arrayMap(x -> concat(x, ','), arrayFilter(x -> toInt64OrZero(x) IN SM, " +
				"splitByChar(',', ordered_sample_list))), '') AS ordered_sample_list"
		}
		sel := fmt.Sprintf("SELECT * FROM (SELECT %s FROM {s}.`%s` WHERE %s) ORDER BY ALL FORMAT TabSeparatedWithNames",
			cols, table.name, table.where)
		// The derived keys expand to SQL over the base keys.
		sel = values.Replace(keys.Replace(sel))
		out, err := c.Execute(sel)
		if err != nil {
			t.Fatalf("%s: %v\n%s", table.name, err, sel)
		}
		if err := os.WriteFile(filepath.Join(dir, table.name+".tsv"), []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"), []byte(schema.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
