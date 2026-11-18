package ingest

import (
	"fmt"
	"os"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

// TestRecordRowsAndColumns checks, against the server named by
// CLICKHOUSE_TEST_URL, that a clinical file is stored as rows with a map of
// values in a new raw database and with a column per header in one whose
// table already has them, that both read the same through the table's
// name, that a second file with a new column extends the view, and that a
// changed file's rows replace its old ones.
func TestRecordRowsAndColumns(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	const sample = "data_clinical_sample.txt"
	files := map[string]string{
		"meta_study.txt": "type_of_cancer: brca\ncancer_study_identifier: rows_study\nname: R\ndescription: d\n",
		"meta_clinical_sample.txt": "cancer_study_identifier: rows_study\ngenetic_alteration_type: CLINICAL\n" +
			"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
		sample: "#Patient Identifier\tSample Identifier\tGrade\n#P\tS\tG\n#STRING\tSTRING\tSTRING\n#1\t1\t1\n" +
			"PATIENT_ID\tSAMPLE_ID\tGRADE\nP1\tS1\t2\nP2\tS2\t\n",
		"meta_clinical_sample_b.txt": "cancer_study_identifier: rows_study\ngenetic_alteration_type: CLINICAL\n" +
			"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample_b.txt\n",
		"data_clinical_sample_b.txt": "#Patient Identifier\tSample Identifier\tStage\n#P\tS\tS\n#STRING\tSTRING\tSTRING\n#1\t1\t1\n" +
			"PATIENT_ID\tSAMPLE_ID\tSTAGE\nP3\tS3\tIV\n",
	}
	read := "SELECT source_file, row_num, patient_id, sample_id, grade, stage FROM `%s`.clinical_sample " +
		"ORDER BY source_file, row_num FORMAT TSV"
	want := "data_clinical_sample.txt\t1\tP1\tS1\t2\t\n" +
		"data_clinical_sample.txt\t2\tP2\tS2\t\t\n" +
		"data_clinical_sample_b.txt\t1\tP3\tS3\t\tIV"

	for _, mode := range []string{"rows", "columns"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, files)
			db := fmt.Sprintf("ingest_rows_test_%s_%d", mode, os.Getpid())
			c := ch.New(base, db)
			if _, err := c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`") })
			if mode == "columns" {
				if err := c.CreateDatabase(); err != nil {
					t.Fatal(err)
				}
				if err := ensureSchema(c, "clinical_sample", []string{"patient_id", "sample_id"}); err != nil {
					t.Fatal(err)
				}
			}
			ingest := func() {
				t.Helper()
				s, err := parser.Scan(dir)
				if err != nil {
					t.Fatal(err)
				}
				stats, err := IngestStudy(c, s)
				if err != nil {
					t.Fatal(err)
				}
				for _, st := range stats {
					if st.RowsLoaded != st.RowsExpected {
						t.Errorf("%s: %d rows loaded, %d expected", st.SourceFile, st.RowsLoaded, st.RowsExpected)
					}
				}
			}
			query := func(sql string) string {
				t.Helper()
				out, err := c.Execute(sql)
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(out)
			}
			ingest()
			if got := query(fmt.Sprintf(read, db)); got != want {
				t.Errorf("rows\n%s\nwant\n%s", got, want)
			}
			engine, err := c.TableEngine("clinical_sample")
			if err != nil {
				t.Fatal(err)
			}
			if wantEngine := map[string]string{"rows": "View", "columns": "MergeTree"}[mode]; engine != wantEngine {
				t.Errorf("clinical_sample is a %s, want a %s", engine, wantEngine)
			}

			writeFiles(t, dir, map[string]string{sample: "#Patient Identifier\tSample Identifier\tGrade\n#P\tS\tG\n" +
				"#STRING\tSTRING\tSTRING\n#1\t1\t1\nPATIENT_ID\tSAMPLE_ID\tGRADE\nP1\tS1\t3\n"})
			ingest()
			if got, want := query(fmt.Sprintf(read, db)), "data_clinical_sample.txt\t1\tP1\tS1\t3\t\n"+
				"data_clinical_sample_b.txt\t1\tP3\tS3\t\tIV"; got != want {
				t.Errorf("after re-ingest\n%s\nwant\n%s", got, want)
			}
		})
	}
}
