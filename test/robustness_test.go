package contract

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Contracts of Java's serialization and error handling that hold for every
// endpoint: null properties are never written (CustomObjectMapper is
// NON_NULL), and a path no controller serves answers Spring's error body.

// nullPaths lists the JSON paths of null object properties in v.
func nullPaths(v any, at string, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if e == nil {
				*out = append(*out, at+"."+k)
				continue
			}
			nullPaths(e, at+"."+k, out)
		}
	case []any:
		for i, e := range x {
			nullPaths(e, fmt.Sprintf("%s[%d]", at, i), out)
		}
	}
}

func wantNoNulls(t *testing.T, method, path, body string) []any {
	t.Helper()
	resp, raw := rawDo(t, method, path, body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s %s -> %d: %s", method, path, resp.StatusCode, raw)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	var nulls []string
	nullPaths(v, "$", &nulls)
	if len(nulls) > 0 {
		t.Errorf("%s %s: %d null properties, e.g. %v", method, path, len(nulls), nulls[:min(3, len(nulls))])
	}
	list, _ := v.([]any)
	return list
}

func TestContractNoNullProperties(t *testing.T) {
	requireAPI(t)
	es0 := `{"studyIds":["study_es_0"]}`
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/studies/lgg_ucsf_2014_test_generic_assay/patients/P01/clinical-events", ""},
		{"POST", "/api/structuralvariant-counts/fetch", es0},
		{"POST", "/api/structuralvariant-genes/fetch", es0},
		{"POST", "/api/mutated-genes/fetch", es0},
		{"POST", "/api/cna-genes/fetch", es0},
		{"GET", "/api/gene-panels?projection=DETAILED", ""},
		{"POST", "/api/mutations/fetch?projection=DETAILED", `{"molecularProfileIds":["study_es_0_mutations"],"entrezGeneIds":[7157,672]}`},
		{"POST", "/api/molecular-data/fetch?projection=DETAILED", `{"molecularProfileIds":["study_es_0_gistic"],"entrezGeneIds":[7157,672]}`},
	} {
		wantNoNulls(t, c.method, c.path, c.body)
	}
}

// TestContractClinicalEventOpenEndOmitted pins the patient-view timeline: an
// event without a stop date has no endNumberOfDaysSinceDiagnosis property,
// one with a stop date has it.
func TestContractClinicalEventOpenEndOmitted(t *testing.T) {
	requireAPI(t)
	events := wantNoNulls(t, "GET", "/api/studies/lgg_ucsf_2014_test_generic_assay/patients/P01/clinical-events", "")
	var open, closed int
	for _, e := range events {
		m := e.(map[string]any)
		if _, ok := m["startNumberOfDaysSinceDiagnosis"]; !ok {
			t.Errorf("event without start day: %v", m)
		}
		if _, ok := m["endNumberOfDaysSinceDiagnosis"]; ok {
			closed++
		} else {
			open++
		}
	}
	if open == 0 || closed == 0 {
		t.Errorf("want both open and closed events, got %d open, %d closed", open, closed)
	}
}

func TestContractUnknownAPIPath(t *testing.T) {
	requireAPI(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/no-such-endpoint"},
		{"POST", "/api/no-such-endpoint/fetch"},
	} {
		resp, body := rawDo(t, c.method, c.path, "{}")
		wantEq(t, resp.StatusCode, 404)
		var e map[string]any
		if err := json.Unmarshal(body, &e); err != nil {
			t.Fatalf("%s %s: %v (%s)", c.method, c.path, err, body)
		}
		wantEq(t, e["status"], float64(404))
		wantEq(t, e["error"], "Not Found")
		wantEq(t, e["path"], c.path)
		if e["timestamp"] == nil {
			t.Errorf("%s %s: no timestamp", c.method, c.path)
		}
	}
	resp, body := rawDo(t, "PUT", "/api/mutation-counts-by-position/fetch", "[]")
	wantEq(t, resp.StatusCode, 405)
	wantEq(t, resp.Header.Get("Allow"), "POST")
	var e map[string]any
	json.Unmarshal(body, &e)
	wantEq(t, e["error"], "Method Not Allowed")
}

// TestContractMutationCountsByPositionStoredRecords pins that only stored
// mutations are counted (MutationMapper counts mutation_derived): the lgg
// study's silent MYC p.T73= is filtered at import, so position 73 has none
// and reads null, as the Java backend returns it.
func TestContractMutationCountsByPositionStoredRecords(t *testing.T) {
	requireAPI(t)
	var got []map[string]any
	post(t, "/api/mutation-counts-by-position/fetch", []map[string]int{
		{"entrezGeneId": 4609, "proteinPosStart": 73, "proteinPosEnd": 73},
		{"entrezGeneId": 7157, "proteinPosStart": 273, "proteinPosEnd": 273},
	}, &got)
	wantEq(t, got, []map[string]any{
		nil,
		{"entrezGeneId": float64(7157), "proteinPosStart": float64(273), "proteinPosEnd": float64(273), "count": float64(9)},
	})
}
