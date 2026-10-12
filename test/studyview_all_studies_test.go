package contract

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A study-view filter that names no studies and no samples searches every
// study where the column-store controller does not validate it, as Java's
// filter SQL then has no study condition; with nothing else to narrow by it
// selects nothing. Where the controller validates the filter, it is a 400.

const primaryFilter = `"clinicalDataFilters":[{"attributeId":"SAMPLE_TYPE","values":[{"value":"Primary"}]}]`

func allStudyIDs(t *testing.T) string {
	t.Helper()
	var studies []struct {
		StudyID string `json:"studyId"`
	}
	get(t, "/api/studies", &studies)
	ids := make([]string, len(studies))
	for i, s := range studies {
		ids[i] = s.StudyID
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

func TestContractStudyViewFilterWithoutStudies(t *testing.T) {
	requireAPI(t)
	all := allStudyIDs(t)
	for _, c := range []struct{ path, body, named string }{
		{"/api/filtered-samples/fetch", `{` + primaryFilter + `}`, `{"studyIds":` + all + `,` + primaryFilter + `}`},
		{"/api/filtered-samples/fetch", `{"studyIds":[],"sampleIdentifiers":[],` + primaryFilter + `}`, `{"studyIds":` + all + `,` + primaryFilter + `}`},
		{"/api/mutated-genes/fetch", `{` + primaryFilter + `}`, `{"studyIds":` + all + `,` + primaryFilter + `}`},
		{"/api/cna-genes/fetch", `{` + primaryFilter + `}`, `{"studyIds":` + all + `,` + primaryFilter + `}`},
		{"/api/mutation-data-counts/fetch", `{"genomicDataFilters":[{"hugoGeneSymbol":"TP53","profileType":"mutations"}],"studyViewFilter":{` + primaryFilter + `}}`,
			`{"genomicDataFilters":[{"hugoGeneSymbol":"TP53","profileType":"mutations"}],"studyViewFilter":{"studyIds":` + all + `,` + primaryFilter + `}}`},
	} {
		want, wantBody := rawDo(t, "POST", c.path, c.named)
		got, gotBody := rawDo(t, "POST", c.path, c.body)
		if got.StatusCode != http.StatusOK || got.StatusCode != want.StatusCode || string(gotBody) != string(wantBody) {
			t.Errorf("POST %s %s: %d %.200q, want %d %.200q as with every study named",
				c.path, c.body, got.StatusCode, gotBody, want.StatusCode, wantBody)
		}
	}
	var samples []map[string]any
	post(t, "/api/filtered-samples/fetch", map[string]any{}, &samples)
	if len(samples) != 0 {
		t.Errorf("a filter without studies or other filters selected %d samples", len(samples))
	}
	post(t, "/api/filtered-samples/fetch", json.RawMessage(`{`+primaryFilter+`}`), &samples)
	if len(samples) == 0 {
		t.Error("a filter without studies selected no samples")
	}
}

func TestContractStudyViewFilterValidation(t *testing.T) {
	requireAPI(t)
	const (
		either = "eitherSampleIdentifiersOrStudyIdsPresent must be true"
		size   = "size must be between 1 and 2147483647"
		badRaw = "There is an error in the JSON format of the request payload"
	)
	for _, c := range []struct {
		path, body string
		status     int
		message    string // "": an empty body
	}{
		// Column-store controllers with @Valid.
		{"/api/sample-lists-counts/fetch", `{` + primaryFilter + `}`, 400, either},
		{"/api/sample-lists-counts/fetch", `{"studyIds":[]}`, 400, "studyIds " + size},
		{"/api/sample-lists-counts/fetch", `{"studyIds":[],"sampleIdentifiers":[]}`, 400, either},
		{"/api/molecular-profile-sample-counts/fetch", `{"studyIds":["study_es_0"],"sampleIdentifiers":[{"studyId":"study_es_0","sampleId":"x"}]}`, 400, either},
		{"/api/structuralvariant-genes/fetch", `{}`, 400, either},
		{"/api/clinical-event-type-counts/fetch", `{"sampleIdentifiers":[]}`, 400, "sampleIdentifiers " + size},
		{"/api/treatments/patient-counts/fetch", `{}`, 400, either},
		{"/api/treatments/sample-counts/fetch", `{}`, 400, either},
		{"/api/treatments/patient-counts/fetch?tier=Bad", `{}`, 400,
			"Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.model.ClinicalEventKeyCode.Bad"},
		{"/api/clinical-data-violin-plots/fetch?categoricalAttributeId=SEX&numericalAttributeId=AGE", `{}`, 400, either},
		{"/api/sample-lists-counts/fetch", `{"studyIds":"study_es_0"}`, 400, badRaw},
		{"/api/filtered-samples/fetch", `{"studyIds":"study_es_0"}`, 400, badRaw},
		// Legacy controllers: the intercepted filter.
		{"/api/structuralvariant-counts/fetch", `{}`, 400, "interceptedStudyViewFilter must be true"},
		{"/api/clinical-data-table/fetch", `{"studyIds":[]}`, 400, "interceptedStudyViewFilter " + size},
		{"/api/treatments/patient", `{` + primaryFilter + `}`, 400, "interceptedStudyViewFilter must be true"},
		{"/api/treatments/sample", `{"studyIds":"study_es_0"}`, 200, ""},
		{"/api/legacy/mutated-genes/fetch", `{` + primaryFilter + `}`, 400, "interceptedStudyViewFilter must be true"},
		{"/api/legacy/filtered-samples/fetch", `{"sampleIdentifiers":[]}`, 400, "interceptedStudyViewFilter " + size},
	} {
		resp, body := rawDo(t, "POST", c.path, c.body)
		ok := resp.StatusCode == c.status
		if c.message == "" {
			ok = ok && len(body) == 0
		} else {
			var e struct {
				Message string `json:"message"`
			}
			ok = ok && json.Unmarshal(body, &e) == nil && e.Message == c.message
		}
		if !ok {
			t.Errorf("POST %s %s: %d %q, want %d %q", c.path, c.body, resp.StatusCode, strings.TrimSpace(string(body)), c.status, c.message)
		}
	}
}
