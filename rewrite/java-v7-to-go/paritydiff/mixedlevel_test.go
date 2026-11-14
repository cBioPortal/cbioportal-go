package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMixedLevelAttributes checks the known-if-mixed-level option's parts:
// the attributes the selected studies define at both levels, asked of
// origin A once per set of studies, and a comparison that leaves out only
// those attributes.
func TestMixedLevelAttributes(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.Write([]byte(`[{"clinicalAttributeId":"HISTOLOGY","patientAttribute":true,"studyId":"a"},` +
			`{"clinicalAttributeId":"HISTOLOGY","patientAttribute":false,"studyId":"b"},` +
			`{"clinicalAttributeId":"AGE","patientAttribute":true,"studyId":"a"},` +
			`{"clinicalAttributeId":"AGE","patientAttribute":true,"studyId":"b"}]`))
	}))
	defer srv.Close()
	fa := &fetcher{origin: srv.URL, client: srv.Client()}
	m := mixedLevels{}
	body := `{"attributes":[],"studyViewFilter":{"studyIds":["b","a"]}}`
	attrs := m.of(fa, body)
	if len(attrs) != 1 || !attrs["HISTOLOGY"] {
		t.Fatalf("mixed-level attributes %v", attrs)
	}
	m.of(fa, `{"studyViewFilter":{"studyIds":["a"],"sampleIdentifiers":[{"studyId":"b"}]}}`)
	if asked != 1 {
		t.Errorf("asked origin A %d times for one set of studies", asked)
	}
	if got := m.of(fa, `{"studyViewFilter":{"studyIds":["a"]}}`); got != nil {
		t.Errorf("one study: %v", got)
	}

	const age = `{"attributeId":"AGE","counts":[{"value":"50","count":3}]}`
	histology := func(n int) string {
		return `{"attributeId":"HISTOLOGY","counts":[{"value":"Adenocarcinoma","count":` + string(rune('0'+n)) + `}]}`
	}
	resp := func(s string) response { return response{Status: 200, Body: []byte(s)} }
	if !onlyAttributesDiffer(resp("["+age+","+histology(1)+"]"), resp("["+histology(2)+","+age+"]"), attrs, request{Opts: requestOptions{Unordered: true}}, defaultOptions()) {
		t.Error("a difference in HISTOLOGY only was not accepted")
	}
	if onlyAttributesDiffer(resp("["+age+","+histology(1)+"]"), resp(`[{"attributeId":"AGE","counts":[{"value":"50","count":4}]},`+histology(2)+"]"), attrs, request{}, defaultOptions()) {
		t.Error("a difference in AGE was accepted")
	}
}
