package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

func TestInsertSQLNumbersRows(t *testing.T) {
	c := ch.New("http://localhost:0", "raw")
	f := &parser.DataFile{DataPath: "/s/data_clinical_sample.txt"}
	got := insertSQL(c, "clinical_sample", provenance("st", f), 3, []string{"patient_id", "sample_id"})
	want := "INSERT INTO `raw`.`clinical_sample` (`study_id`, `source_file`, `alteration_type`, `datatype`, `profile_stable_id`, " +
		"`file_num`, `row_num`, `patient_id`, `sample_id`) SELECT 'st', 'data_clinical_sample.txt', '', '', '', toUInt32(3), * " +
		"FROM input('`row_num` UInt64, `patient_id` String, `sample_id` String') FORMAT TabSeparatedRaw"
	if got != want {
		t.Errorf("insertSQL:\n got %s\nwant %s", got, want)
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestIngestRowNumbers ingests a study into a scratch database on the server
// named by CLICKHOUSE_TEST_URL: rows are numbered per file in file order
// (comment, header and blank lines not counted), a staging table created
// before the order columns existed gains them (its old rows reading 0), and
// re-ingesting the study renumbers its rows identically.
func TestIngestRowNumbers(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	db := fmt.Sprintf("ingest_rownum_test_%d", os.Getpid())
	c := ch.New(base, db)
	if _, err := c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`") })

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"meta_study.txt": "type_of_cancer: brca\ncancer_study_identifier: rn_study\nname: RN\ndescription: d\n",
		"meta_clinical_sample.txt": "cancer_study_identifier: rn_study\ngenetic_alteration_type: CLINICAL\n" +
			"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
		"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\tSubtype\n#P\tS\tT\n#STRING\tSTRING\tSTRING\n#1\t1\t1\n" +
			"PATIENT_ID\tSAMPLE_ID\tSUBTYPE\n" +
			"P3\tS3\tx\n\n#late comment\nP1\tS1\ty\n\r\nP2\tS2\tz\n",
		"meta_expression.txt": "cancer_study_identifier: rn_study\ngenetic_alteration_type: MRNA_EXPRESSION\n" +
			"datatype: CONTINUOUS\nstable_id: rna\nprofile_name: r\nprofile_description: r\nshow_profile_in_analysis_tab: false\n" +
			"data_filename: data_expression.txt\n",
		"data_expression.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS3\tS1\tS2\n" +
			"TP53\t7157\t1.5\t\t2\n\nKRAS\t3845\t0.1\t0.2\t0.3\n",
	})

	// A staging table from before the order columns, holding another study.
	if err := c.CreateDatabase(); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"CREATE TABLE `" + db + "`.clinical_sample (study_id String, source_file String, alteration_type String, datatype String, " +
			"profile_stable_id String, patient_id String, sample_id String) ENGINE = MergeTree ORDER BY (study_id, source_file)",
		"INSERT INTO `" + db + "`.clinical_sample (study_id, source_file, patient_id, sample_id) VALUES ('old', 'f', 'PO', 'SO')",
	} {
		if _, err := c.Execute(sql); err != nil {
			t.Fatal(err)
		}
	}

	query := func(sql string) []string {
		t.Helper()
		out, err := c.Execute(sql + " FORMAT TSV")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(out), "\n")
	}
	check := func(round string) {
		t.Helper()
		got := query("SELECT study_id, sample_id, file_num, row_num FROM `" + db + "`.clinical_sample ORDER BY study_id, file_num, row_num")
		want := []string{"old\tSO\t0\t0", "rn_study\tS3\t1\t1", "rn_study\tS1\t1\t2", "rn_study\tS2\t1\t3"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: clinical_sample rows %q, want %q", round, got, want)
		}
		got = query("SELECT hugo_symbol, sample_id, file_num, row_num FROM `" + db + "`.mrna_expression_matrix ORDER BY row_num")
		want = []string{"TP53\tS3\t2\t1", "TP53\tS2\t2\t2", "KRAS\tS3\t2\t3", "KRAS\tS1\t2\t4", "KRAS\tS2\t2\t5"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: matrix rows %q, want %q", round, got, want)
		}
		got = query("SELECT sample_id, file_num, row_num FROM `" + db + "`.profile_sample ORDER BY row_num")
		want = []string{"S3\t2\t1", "S1\t2\t2", "S2\t2\t3"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: profile_sample rows %q, want %q", round, got, want)
		}
	}
	for _, round := range []string{"ingest", "re-ingest"} {
		s, err := parser.Scan(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := IngestStudy(c, s); err != nil {
			t.Fatalf("%s: %v", round, err)
		}
		check(round)
	}

	// Removing a file drops its rows on the next ingest, and only its rows.
	for _, f := range []string{"meta_expression.txt", "data_expression.txt"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := parser.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := IngestStudy(c, s); err != nil {
		t.Fatalf("after removal: %v", err)
	}
	if got := query("SELECT count() FROM `" + db + "`.mrna_expression_matrix WHERE study_id = 'rn_study'"); got[0] != "0" {
		t.Errorf("removed matrix file still staged: %v rows", got)
	}
	if got := query("SELECT count() FROM `" + db + "`.profile_sample WHERE study_id = 'rn_study'"); got[0] != "0" {
		t.Errorf("removed matrix file's profile samples still staged: %v rows", got)
	}
	got := query("SELECT study_id, sample_id FROM `" + db + "`.clinical_sample ORDER BY study_id, file_num, row_num")
	if want := []string{"old\tSO", "rn_study\tS3", "rn_study\tS1", "rn_study\tS2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after removal: clinical_sample %q, want %q", got, want)
	}
}
