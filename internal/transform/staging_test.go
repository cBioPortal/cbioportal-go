package transform

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/parser"
	"github.com/cbioportal/cbioportal-go/internal/reference"
)

func TestStagingRewrite(t *testing.T) {
	s := newStaging("portal")
	got := s.rewrite("CREATE OR REPLACE TABLE `portal`.`sample` AS SELECT * FROM `portal`.gene_key_map g " +
		"JOIN portal.sample_profile p USING (study_id) JOIN `portal_raw`.cancer_study c USING (study_id) " +
		"JOIN `portal`.`__next_patient` x USING (study_id) WHERE p.portal = 1")
	want := "CREATE OR REPLACE TABLE `portal`.`__next_sample` AS SELECT * FROM `portal`.`__next_gene_key_map` g " +
		"JOIN `portal`.`__next_sample_profile` p USING (study_id) JOIN `portal_raw`.cancer_study c USING (study_id) " +
		"JOIN `portal`.`__next_patient` x USING (study_id) WHERE p.portal = 1"
	if got != want {
		t.Errorf("rewrite:\n got %s\nwant %s", got, want)
	}
}

func TestStagingRecordsViews(t *testing.T) {
	s := newStaging("portal")
	for _, q := range []string{
		"CREATE OR REPLACE VIEW `portal`.`clinical_sample` AS SELECT 1",
		"CREATE OR REPLACE TABLE `portal`.`sample` ENGINE = MergeTree ORDER BY tuple() AS SELECT 1",
		"CREATE VIEW portal.mutation AS SELECT 2",
		"  CREATE OR REPLACE VIEW `portal`.`clinical_sample` AS SELECT 3",
		"CREATE OR REPLACE VIEW `portal_raw`.`cna_matrix` AS SELECT 4",
	} {
		s.record(q)
	}
	if want := []string{"mutation", "clinical_sample"}; !reflect.DeepEqual(s.views, want) {
		t.Errorf("views %v, want %v", s.views, want)
	}
	if got := s.viewSQL["clinical_sample"]; !strings.HasSuffix(got, "SELECT 3") {
		t.Errorf("clinical_sample keeps the last definition, got %q", got)
	}
}

// scratchDatabases names raw, portal and reference databases for a live
// test, dropped at its end; the reference database has empty tables.
func scratchDatabases(t *testing.T, base, prefix string) (rawDB, db, refDB string) {
	t.Helper()
	rawDB = fmt.Sprintf("%s_raw_%d", prefix, os.Getpid())
	db = fmt.Sprintf("%s_%d", prefix, os.Getpid())
	refDB = fmt.Sprintf("%s_ref_%d", prefix, os.Getpid())
	admin := ch.New(base, "default")
	for _, d := range []string{rawDB, db, refDB} {
		admin.ExecuteServerwide("DROP DATABASE IF EXISTS `" + d + "`")
		t.Cleanup(func() { admin.ExecuteServerwide("DROP DATABASE IF EXISTS `" + d + "`") })
	}
	var seed bytes.Buffer
	gzip.NewWriter(&seed).Close()
	seedPath := filepath.Join(t.TempDir(), "seed.sql.gz")
	if err := os.WriteFile(seedPath, seed.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reference.LoadReference(ch.New(base, refDB), seedPath); err != nil {
		t.Fatal(err)
	}
	return rawDB, db, refDB
}

// ingestFiles writes a study's files to a directory and ingests it.
func ingestFiles(t *testing.T, base, rawDB string, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := parser.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.IngestStudy(ch.New(base, rawDB), st); err != nil {
		t.Fatal(err)
	}
}

// TestLiveClinicalAttributeIDs: an attribute id that is not a plain
// identifier keeps its header spelling in the clinical data, as in its
// clinical_attribute_meta row ("BMI>25", "1P19Q_CODELETION").
func TestLiveClinicalAttributeIDs(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	rawDB, db, refDB := scratchDatabases(t, base, "transform_attr_test")
	ingestFiles(t, base, rawDB, map[string]string{
		"meta_study.txt": "type_of_cancer: brca\ncancer_study_identifier: attr_study\nname: a\ndescription: d\n",
		"meta_clinical_patient.txt": "cancer_study_identifier: attr_study\ngenetic_alteration_type: CLINICAL\n" +
			"datatype: PATIENT_ATTRIBUTES\ndata_filename: data_clinical_patient.txt\n",
		"data_clinical_patient.txt": "#Patient Identifier\tBMI>25\t1p19q\tAge\n#P\tB\tC\tA\n#STRING\tSTRING\tSTRING\tNUMBER\n#1\t1\t1\t1\n" +
			"PATIENT_ID\tBMI>25\t1P19Q_CODELETION\tAGE\nP1\tYes\tCodeleted\t40\n",
	})
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	out, err := ch.New(base, "default").Execute(fmt.Sprintf(
		"SELECT arrayStringConcat(arraySort(groupArray(attr_id)), ',') FROM `%s`.clinical_patient WHERE attr_id != 'SAMPLE_COUNT' FORMAT TabSeparated", db))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), "1P19Q_CODELETION,AGE,BMI>25"; got != want {
		t.Errorf("patient attribute ids %q, want %q", got, want)
	}
}

// TestLiveTransformPublishesAtomically transforms scratch databases on the
// server named by CLICKHOUSE_TEST_URL: while a run builds, and after a run
// that fails before publishing, the portal database serves the previous
// build and holds no staged objects; a successful run replaces it.
func TestLiveTransformPublishesAtomically(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	rawDB, db, refDB := scratchDatabases(t, base, "transform_stage_test")
	admin := ch.New(base, "default")
	ingestStudy := func(id string, samples ...string) {
		t.Helper()
		dir := t.TempDir()
		rows := ""
		for _, s := range samples {
			rows += "P_" + s + "\t" + s + "\n"
		}
		for name, body := range map[string]string{
			"meta_study.txt": "type_of_cancer: brca\ncancer_study_identifier: " + id + "\nname: " + id + "\ndescription: d\n",
			"meta_clinical_sample.txt": "cancer_study_identifier: " + id + "\ngenetic_alteration_type: CLINICAL\n" +
				"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
			"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\n#P\tS\n#STRING\tSTRING\n#1\t1\n" +
				"PATIENT_ID\tSAMPLE_ID\n" + rows,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		st, err := parser.Scan(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ingest.IngestStudy(ch.New(base, rawDB), st); err != nil {
			t.Fatal(err)
		}
	}
	query := func(sql string) string {
		t.Helper()
		out, err := admin.Execute(sql)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	samples := func(table string) string {
		col := "sample_id"
		if strings.HasSuffix(table, "_derived") {
			col = "sample_stable_id"
		}
		return query(fmt.Sprintf("SELECT arrayStringConcat(arraySort(groupUniqArray(%s)), ',') FROM `%s`.`%s` FORMAT TabSeparated", col, db, table))
	}
	staged := func() string {
		return query(fmt.Sprintf("SELECT count() FROM system.tables WHERE database = '%s' AND startsWith(name, '%s') FORMAT TabSeparated", db, stagePrefix))
	}
	t.Cleanup(func() { beforePublish = nil })

	ingestStudy("study_a", "A1", "A2")
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	if got := samples("sample"); got != "A1,A2" {
		t.Fatalf("first build: samples %q", got)
	}

	ingestStudy("study_b", "B1")
	failure := errors.New("injected")
	beforePublish = func() error {
		if got := samples("sample"); got != "A1,A2" {
			t.Errorf("during the build the API reads samples %q, want the previous A1,A2", got)
		}
		if got := samples(stagePrefix + "sample"); got != "A1,A2,B1" {
			t.Errorf("staged samples %q, want A1,A2,B1", got)
		}
		return failure
	}
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); !errors.Is(err, failure) {
		t.Fatalf("failed build returned %v", err)
	}
	if got := samples("sample"); got != "A1,A2" {
		t.Errorf("after a failed build: samples %q, want A1,A2", got)
	}
	if got := samples("sample_derived"); got != "A1,A2" {
		t.Errorf("after a failed build: sample_derived %q, want A1,A2", got)
	}
	if n := staged(); n != "0" {
		t.Errorf("%s staged objects left after a failed build", n)
	}

	beforePublish = nil
	if _, err := Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sample", "sample_derived"} {
		if got := samples(table); got != "A1,A2,B1" {
			t.Errorf("after publishing: %s %q, want A1,A2,B1", table, got)
		}
	}
	if n := staged(); n != "0" {
		t.Errorf("%s staged objects left after publishing", n)
	}
	if n := query(fmt.Sprintf("SELECT count() FROM system.tables WHERE database = '%s' AND create_table_query LIKE '%%%s%%' FORMAT TabSeparated",
		db, stagePrefix)); n != "0" {
		t.Errorf("%s published objects read staged names", n)
	}
}
