package studyview

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/parser"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

// treatmentTierStudy has a treatment timeline with AGENT_CLASS and
// AGENT_TARGET columns, which the Java importer stores as event data like
// any column after EVENT_TYPE.
var treatmentTierStudy = map[string]string{
	"meta_study.txt": "type_of_cancer: mixed\ncancer_study_identifier: treatment_tiers\nname: Treatment tiers\n" +
		"description: Treatment timeline with agent classes and targets\nreference_genome: hg19\n",
	"meta_clinical_sample.txt": "cancer_study_identifier: treatment_tiers\ngenetic_alteration_type: CLINICAL\n" +
		"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
	"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\n#Patient Identifier\tSample Identifier\n" +
		"#STRING\tSTRING\n#1\t1\nPATIENT_ID\tSAMPLE_ID\nP1\tS1\nP2\tS2\n",
	"meta_timeline_treatment.txt": "cancer_study_identifier: treatment_tiers\ngenetic_alteration_type: CLINICAL\n" +
		"datatype: TIMELINE\ndata_filename: data_timeline_treatment.txt\n",
	"data_timeline_treatment.txt": "PATIENT_ID\tSTART_DATE\tSTOP_DATE\tEVENT_TYPE\tAGENT\tAGENT_CLASS\tAGENT_TARGET\n" +
		"P1\t10\t40\tTreatment\tTamoxifen\tHormone Therapy\tESR1\n" +
		"P2\t0\t100\tTreatment\tTrastuzumab\tHER2 Targeted\tERBB2\n",
}

// TestLiveTreatmentTiers ingests and transforms treatmentTierStudy when
// CLICKHOUSE_TEST_URL is set (genes from CLICKHOUSE_TEST_REF_DB, default
// cbioportal_ref): the AgentClass and AgentTarget tiers read the timeline's
// AGENT_CLASS and AGENT_TARGET columns.
func TestLiveTreatmentTiers(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	refDB := os.Getenv("CLICKHOUSE_TEST_REF_DB")
	if refDB == "" {
		refDB = "cbioportal_ref"
	}
	admin := ch.New(base, "default")
	db := fmt.Sprintf("cbioportal_go_test_tiers_%d", os.Getpid())
	rawDB := db + "_raw"
	for _, d := range []string{db, rawDB} {
		if _, err := admin.Execute("CREATE DATABASE " + d); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = admin.Execute("DROP DATABASE IF EXISTS " + d) })
	}
	dir := t.TempDir()
	for name, content := range treatmentTierStudy {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	study, err := parser.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.IngestStudy(ch.New(base, rawDB), study); err != nil {
		t.Fatal(err)
	}
	if _, err := transform.Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}

	c := ch.New(base, db)
	c.SetDatabaseAliases(rawDB, refDB)
	s := New(&core.State{CH: c})
	mux := http.NewServeMux()
	s.routeTreatments(mux)
	post := func(path, body string) string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return strings.TrimSpace(w.Body.String())
	}
	if got := post("/api/treatments/display-patient?tier=AgentClass", `["treatment_tiers"]`); got != "true" {
		t.Errorf("display-patient AgentClass = %s, want true", got)
	}
	for tier, want := range map[string][]string{
		"Agent":       {"Tamoxifen", "Trastuzumab"},
		"AgentClass":  {"HER2 Targeted", "Hormone Therapy"},
		"AgentTarget": {"ERBB2", "ESR1"},
	} {
		var rows []struct {
			Treatment string `json:"treatment"`
			Count     int    `json:"count"`
		}
		body := post("/api/treatments/patient?tier="+tier, `{"studyIds":["treatment_tiers"]}`)
		if err := json.Unmarshal([]byte(body), &rows); err != nil {
			t.Fatalf("%s: %v: %s", tier, err, body)
		}
		got := map[string]int{}
		for _, r := range rows {
			got[r.Treatment] = r.Count
		}
		if len(got) != len(want) {
			t.Errorf("%s: treatments %s, want %v", tier, body, want)
		}
		for _, tr := range want {
			if got[tr] != 1 {
				t.Errorf("%s: %q count %d, want 1 (%s)", tier, tr, got[tr], body)
			}
		}
	}
}
