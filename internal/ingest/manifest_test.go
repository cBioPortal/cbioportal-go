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

// TestIngestSkipsUnchangedFiles checks, against the server named by
// CLICKHOUSE_TEST_URL, that a re-ingest stages only the files that changed:
// an unchanged file is skipped, a changed one is staged again, a file whose
// rows went missing is staged again, and a raw database without the list of
// staged files stages every file and lists them.
func TestIngestSkipsUnchangedFiles(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	db := fmt.Sprintf("ingest_skip_test_%d", os.Getpid())
	c := ch.New(base, db)
	if _, err := c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`") })

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"meta_study.txt": "type_of_cancer: brca\ncancer_study_identifier: skip_study\nname: S\ndescription: d\n",
		"meta_clinical_sample.txt": "cancer_study_identifier: skip_study\ngenetic_alteration_type: CLINICAL\n" +
			"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
		"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\n#P\tS\n#STRING\tSTRING\n#1\t1\n" +
			"PATIENT_ID\tSAMPLE_ID\nP1\tS1\nP2\tS2\n",
		"meta_expression.txt": "cancer_study_identifier: skip_study\ngenetic_alteration_type: MRNA_EXPRESSION\n" +
			"datatype: CONTINUOUS\nstable_id: rna\nprofile_name: r\nprofile_description: r\nshow_profile_in_analysis_tab: false\n" +
			"data_filename: data_expression.txt\n",
		"data_expression.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS1\tS2\nTP53\t7157\t1.5\t2\n",
	})
	ingest := func(round string) map[string]string {
		t.Helper()
		s, err := parser.Scan(dir)
		if err != nil {
			t.Fatal(err)
		}
		stats, err := IngestStudy(c, s)
		if err != nil {
			t.Fatalf("%s: %v", round, err)
		}
		got := map[string]string{}
		for _, st := range stats {
			got[st.SourceFile] = st.Skipped
		}
		return got
	}
	query := func(sql string) string {
		t.Helper()
		out, err := c.Execute(sql + " FORMAT TSV")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	rows := func() string {
		return query("SELECT (SELECT count() FROM `" + db + "`.clinical_sample), (SELECT count() FROM `" + db + "`.mrna_expression_matrix)")
	}
	want := func(round string, got, skipped map[string]string, counts string) {
		t.Helper()
		if !reflect.DeepEqual(got, skipped) {
			t.Errorf("%s: skipped %v, want %v", round, got, skipped)
		}
		if r := rows(); r != counts {
			t.Errorf("%s: clinical and matrix rows %q, want %q", round, r, counts)
		}
	}
	const clinical, expression = "data_clinical_sample.txt", "data_expression.txt"

	want("first ingest", ingest("first ingest"), map[string]string{clinical: "", expression: ""}, "2\t2")
	want("unchanged", ingest("unchanged"), map[string]string{clinical: "unchanged", expression: "unchanged"}, "2\t2")

	writeFiles(t, dir, map[string]string{expression: "Hugo_Symbol\tEntrez_Gene_Id\tS1\tS2\nTP53\t7157\t1.5\t2\nKRAS\t3845\t0.1\t0.2\n"})
	want("changed file", ingest("changed file"), map[string]string{clinical: "unchanged", expression: ""}, "2\t4")

	stored, err := storageTable(c, "clinical_sample")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteWhere(stored, "row_num = 2"); err != nil {
		t.Fatal(err)
	}
	want("missing rows", ingest("missing rows"), map[string]string{clinical: "", expression: "unchanged"}, "2\t4")

	if _, err := c.Execute("DROP TABLE `" + db + "`.`" + stagedFilesTable + "`"); err != nil {
		t.Fatal(err)
	}
	want("no list", ingest("no list"), map[string]string{clinical: "", expression: ""}, "2\t4")
	want("listed again", ingest("listed again"), map[string]string{clinical: "unchanged", expression: "unchanged"}, "2\t4")

	if err := os.Remove(filepath.Join(dir, "meta_expression.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, expression)); err != nil {
		t.Fatal(err)
	}
	want("removed file", ingest("removed file"), map[string]string{clinical: "unchanged"}, "2\t0")
	if got := query("SELECT count() FROM `" + db + "`.`" + stagedFilesTable + "` FINAL WHERE source_file = '" + expression + "'"); got != "0" {
		t.Errorf("removed file still listed: %s", got)
	}
}
