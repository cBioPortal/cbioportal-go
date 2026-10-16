package studies

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

// resourceTableStudy is a study with sample-, patient- and study-level
// resources, the sample rows carrying DISPLAY_NAME and METADATA.
func resourceTableStudy(id, slideName, customMetadata, sampleRows string) map[string]string {
	meta := func(typ, file string) string {
		return "cancer_study_identifier: " + id + "\nresource_type: " + typ + "\ndata_filename: " + file + "\n"
	}
	return map[string]string{
		"meta_study.txt": "type_of_cancer: mixed\ncancer_study_identifier: " + id + "\nname: " + id +
			"\ndescription: Resource table study\nreference_genome: hg19\n",
		"meta_clinical_sample.txt": "cancer_study_identifier: " + id + "\ngenetic_alteration_type: CLINICAL\n" +
			"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
		"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\n#Patient Identifier\tSample Identifier\n" +
			"#STRING\tSTRING\n#1\t1\nPATIENT_ID\tSAMPLE_ID\nP1\tS1\nP2\tS2\nP3\tS3\n",
		"meta_resource_definition.txt": meta("DEFINITION", "data_resource_definition.txt"),
		"data_resource_definition.txt": "RESOURCE_ID\tDISPLAY_NAME\tDESCRIPTION\tRESOURCE_TYPE\tOPEN_BY_DEFAULT\tPRIORITY\tCUSTOM_METADATA\n" +
			"PATHOLOGY_SLIDE\t" + slideName + "\tSlides\tSAMPLE\tTRUE\t1\t" + customMetadata + "\n" +
			"NOTES\tNotes\tNotes\tPATIENT\tFALSE\t2\t\n" +
			"SPONSORS\t\tSponsors\tSTUDY\tTRUE\t3\t\n",
		"meta_resource_sample.txt":  meta("SAMPLE", "data_resource_sample.txt"),
		"data_resource_sample.txt":  "PATIENT_ID\tSAMPLE_ID\tRESOURCE_ID\tURL\tDISPLAY_NAME\tMETADATA\n" + sampleRows,
		"meta_resource_patient.txt": meta("PATIENT", "data_resource_patient.txt"),
		"data_resource_patient.txt": "PATIENT_ID\tRESOURCE_ID\tURL\nP1\tNOTES\thttp://notes/" + id + "\n",
		"meta_resource_study.txt":   meta("STUDY", "data_resource_study.txt"),
		"data_resource_study.txt":   "RESOURCE_ID\tURL\nSPONSORS\thttp://sponsors/" + id + "\n",
	}
}

// TestLiveResourceTable ingests and transforms two studies that share
// sample and patient ids when CLICKHOUSE_TEST_URL is set (genes from
// CLICKHOUSE_TEST_REF_DB, default cbioportal_ref), and checks the resource
// table endpoints over them: rt_a declares a metadata contract, rt_b does
// not and carries an extra key.
func TestLiveResourceTable(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	refDB := os.Getenv("CLICKHOUSE_TEST_REF_DB")
	if refDB == "" {
		refDB = "cbioportal_ref"
	}
	admin := ch.New(base, "default")
	db := fmt.Sprintf("cbioportal_go_test_restable_%d", os.Getpid())
	rawDB := db + "_raw"
	for _, d := range []string{db, rawDB} {
		if _, err := admin.Execute("CREATE DATABASE " + d); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = admin.Execute("DROP DATABASE IF EXISTS " + d) })
	}
	contract := `{"version":1,"fields":[{"key":"stain","type":"string","label":"Stain","visibleByDefault":true},` +
		`{"key":"slide_number","type":"number","label":"Slide #","description":"Slide order"}]}`
	studies := map[string]map[string]string{
		"rt_a": resourceTableStudy("rt_a", "Pathology Slide", contract,
			"P1\tS1\tPATHOLOGY_SLIDE\thttp://a/1\tSlide one\t{\"stain\": \"HE\", \"slide_number\": 10}\n"+
				"P1\tS1\tPATHOLOGY_SLIDE\thttp://a/2\tNA\t{\"stain\": \"PAS\", \"slide_number\": 2}\n"+
				"P2\tS2\tPATHOLOGY_SLIDE\thttp://a/3\t\t{\"stain\": \"HE\", \"slide_number\": 1}\n"),
		"rt_b": resourceTableStudy("rt_b", "Slides B", "",
			"P1\tS1\tPATHOLOGY_SLIDE\thttp://b/1\tB slide\t{\"stain\": \"IHC\", \"Zoom\": \"40x\"}\n"+
				"P3\tS3\tPATHOLOGY_SLIDE\thttp://b/3\t\t\n"),
	}
	for id, files := range studies {
		dir := filepath.Join(t.TempDir(), id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
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
	}
	if _, err := transform.Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}

	c := ch.New(base, db)
	c.SetDatabaseAliases(rawDB, refDB)
	s := New(&core.State{CH: c})
	mux := http.NewServeMux()
	s.routeResourceTable(mux)
	post := func(path, body string) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/resource-table/"+path, strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", path, body, w.Code, w.Body.String())
		}
		return strings.TrimSpace(w.Body.String())
	}
	expect := func(path, body, want string) {
		t.Helper()
		if got := post(path, body); got != want {
			t.Errorf("%s %s:\n got %s\nwant %s", path, body, got, want)
		}
	}

	both := `"studyIds":["rt_a","rt_b"]`
	// Tabs: the label of the first study naming the resource; patients and
	// samples are counted per study.
	expect("tabs/fetch", "{"+both+"}",
		`[{"resourceId":"NOTES","label":"Notes","totalCount":2,"patientCount":2,"sampleCount":0},`+
			`{"resourceId":"PATHOLOGY_SLIDE","label":"Pathology Slide","totalCount":5,"patientCount":4,"sampleCount":4},`+
			`{"resourceId":"SPONSORS","label":"SPONSORS","totalCount":2,"patientCount":0,"sampleCount":0}]`)
	// A patient-view cohort: sample rows by sample, patient rows by patient,
	// no study rows; with two studies, rt_b's same-named S1 and P1 stay out.
	cohort := `"patientIdentifiers":[{"studyId":"rt_a","patientId":"P1"}],"sampleIdentifiers":[{"studyId":"rt_a","sampleId":"S1"}]`
	expect("tabs/fetch", "{"+both+","+cohort+"}",
		`[{"resourceId":"NOTES","label":"Notes","totalCount":1,"patientCount":1,"sampleCount":0},`+
			`{"resourceId":"PATHOLOGY_SLIDE","label":"Pathology Slide","totalCount":2,"patientCount":1,"sampleCount":1}]`)
	expect("tabs/fetch", `{}`, `[]`)

	slides := both + `,"resourceId":"PATHOLOGY_SLIDE","pageNumber":0,"pageSize":10`
	urls := func(body string) string {
		t.Helper()
		var rows []ResourceTableRow
		if err := json.Unmarshal([]byte(post("query/fetch", body)), &rows); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.URL)
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct{ body, want string }{
		// Default order: patient, sample, then file order.
		{"{" + slides + "}", "http://a/1 http://a/2 http://b/1 http://a/3 http://b/3"},
		// A numeric key sorts by number, blanks last either way.
		{"{" + slides + `,"sortBy":"metadata:slide_number","direction":"desc"}`, "http://a/1 http://a/2 http://a/3 http://b/1 http://b/3"},
		{"{" + slides + `,"sortBy":"metadata:slide_number"}`, "http://a/3 http://a/2 http://a/1 http://b/1 http://b/3"},
		{"{" + slides + `,"sortBy":"displayName","direction":"desc"}`, "http://a/1 http://b/1 http://a/2 http://a/3 http://b/3"},
		{"{" + slides + `,"pageNumber":1,"pageSize":2}`, "http://b/1 http://a/3"},
		{"{" + slides + `,"filters":[{"columnId":"metadata:stain","operator":"in","values":["HE","IHC"]}]}`, "http://a/1 http://b/1 http://a/3"},
		{"{" + slides + `,"filters":[{"columnId":"metadata:slide_number","operator":"between","values":["2","10"]}]}`, "http://a/1 http://a/2"},
		{"{" + slides + `,"filters":[{"columnId":"sampleId","operator":"in","values":[]}]}`, ""},
		{"{" + slides + `,"search":"40x"}`, "http://b/1"},
		{"{" + slides + `,"search":"b/"}`, "http://b/1 http://b/3"},
		{"{" + both + `,"resourceId":"PATHOLOGY_SLIDE","pageSize":10,"sampleIdentifiers":[{"studyId":"rt_a","sampleId":"S1"}]}`, "http://a/1 http://a/2"},
		{"{" + slides + `,"pageSize":0}`, ""},
		{`{"studyIds":["rt_a"]}`, ""},
	} {
		if got := urls(c.body); got != c.want {
			t.Errorf("query %s:\n got %s\nwant %s", c.body, got, c.want)
		}
	}
	expect("query/fetch", `{"studyIds":["rt_a"],"resourceId":"PATHOLOGY_SLIDE","pageSize":1}`,
		`[{"studyId":"rt_a","resourceId":"PATHOLOGY_SLIDE","resourceDisplayName":"Pathology Slide","resourceType":"SAMPLE",`+
			`"patientId":"P1","sampleId":"S1","url":"http://a/1","displayName":"Slide one","metadata":{"stain":"HE","slide_number":10}}]`)
	expect("query/fetch", `{"studyIds":["rt_a"],"resourceId":"SPONSORS","pageSize":1}`,
		`[{"studyId":"rt_a","resourceId":"SPONSORS","resourceDisplayName":"SPONSORS","resourceType":"STUDY",`+
			`"url":"http://sponsors/rt_a","metadata":{}}]`)

	builtin := `{"id":"patientId","label":"Patient ID","source":"builtin","dataType":"string","filterable":true,"sortable":true,"visibleByDefault":true},` +
		`{"id":"sampleId","label":"Sample ID","source":"builtin","dataType":"string","filterable":true,"sortable":true,"visibleByDefault":true},` +
		`{"id":"url","label":"Link","source":"builtin","dataType":"link","filterable":false,"sortable":false,"visibleByDefault":true},` +
		`{"id":"displayName","label":"Display Name","source":"builtin","dataType":"string","filterable":true,"sortable":true,"visibleByDefault":true}`
	stain := `{"id":"metadata:stain","label":"Stain","source":"metadata","dataType":"string","filterable":true,"sortable":true,"visibleByDefault":true}`
	slide := `{"id":"metadata:slide_number","label":"Slide #","source":"metadata","dataType":"number","filterable":true,"sortable":true,"visibleByDefault":false,"description":"Slide order"}`
	// rt_a alone: the contract is the column list.
	expect("metadata/fetch", `{"studyIds":["rt_a"],"resourceId":"PATHOLOGY_SLIDE"}`,
		`{"columns":[`+builtin+`,`+stain+`,`+slide+`],"totalRowCount":3,"filteredPatientCount":2,"filteredSampleCount":2,`+
			`"facets":{"metadata:stain":[{"value":"HE","count":2},{"value":"PAS","count":1}]},`+
			`"facetRanges":{"metadata:slide_number":{"min":1.0,"max":10.0}},`+
			`"distinctValueCounts":{"resourceDisplayName":1,"resourceType":1,"displayName":2}}`)
	// Both studies: rt_b declares no contract, so its key follows the
	// declared ones. The counts apply the filter, the facets do not.
	expect("metadata/fetch", "{"+slides+`,"filters":[{"columnId":"metadata:stain","operator":"in","values":["IHC"]}]}`,
		`{"columns":[`+builtin+`,`+stain+`,`+slide+`,`+
			`{"id":"metadata:Zoom","label":"Zoom","source":"metadata","dataType":"string","filterable":true,"sortable":true,"visibleByDefault":false}],`+
			`"totalRowCount":1,"filteredPatientCount":1,"filteredSampleCount":1,`+
			`"facets":{"metadata:stain":[{"value":"HE","count":2},{"value":"IHC","count":1},{"value":"PAS","count":1}],"metadata:Zoom":[{"value":"40x","count":1}]},`+
			`"facetRanges":{"metadata:slide_number":{"min":1.0,"max":10.0}},`+
			`"distinctValueCounts":{"resourceDisplayName":1,"resourceType":1,"displayName":1}}`)
	expect("metadata/fetch", `{"studyIds":["rt_a"]}`,
		`{"columns":[],"totalRowCount":0,"filteredPatientCount":0,"filteredSampleCount":0,"facets":{},"facetRanges":{},"distinctValueCounts":{}}`)
}
