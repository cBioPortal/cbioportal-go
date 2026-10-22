package ingest

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

// On the server named by CLICKHOUSE_TEST_URL: a staging cancer_study created
// before the license field was read gains the column, empty for the study
// already staged, and a study's license is staged as meta_study.txt gives it.
func TestIngestStudyLicense(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	db := fmt.Sprintf("ingest_license_test_%d", os.Getpid())
	c := ch.New(base, db)
	if _, err := c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`") })
	if err := c.CreateDatabase(); err != nil {
		t.Fatal(err)
	}
	old := studyMetaFields[:len(studyMetaFields)-1]
	defs := []string{"`study_id` String"}
	for _, f := range old {
		defs = append(defs, "`"+f+"` String")
	}
	for _, sql := range []string{
		"CREATE TABLE `" + db + "`.cancer_study (" + strings.Join(defs, ", ") + ") ENGINE = MergeTree ORDER BY study_id",
		"INSERT INTO `" + db + "`.cancer_study (study_id, name) VALUES ('old', 'Old')",
	} {
		if _, err := c.Execute(sql); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"meta_study.txt": "type_of_cancer: brca\ncancer_study_identifier: lic_study\nname: L\ndescription: d\n" +
			"license: CC-BY-NC-ND-4.0\n",
	})
	s, err := parser.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := IngestStudy(c, s); err != nil {
		t.Fatal(err)
	}
	out, err := c.Execute("SELECT license, study_id FROM `" + db + "`.cancer_study ORDER BY study_id FORMAT TSV")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(out), "\n")
	if want := []string{"CC-BY-NC-ND-4.0\tlic_study", "\told"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cancer_study rows %q, want %q", got, want)
	}
}
