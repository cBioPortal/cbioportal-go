package contract

import (
	"strings"
	"testing"
)

// Java matches a path with one trailing slash to the request mapping without
// it (WebAppConfig setUseTrailingSlashMatch), for every method.

func TestContractTrailingSlashGet(t *testing.T) {
	requireAPI(t)
	for _, p := range []string{
		"/api/studies",
		"/api/studies/study_es_0",
		"/api/studies/study_es_0/clinical-attributes?attributeIds=AGE",
		"/api/studies/study_es_0/clinical-data",
		"/api/studies/study_es_0/molecular-profiles",
		"/api/studies/study_es_0/sample-lists",
		"/api/studies/study_es_0/samples?pageSize=5",
		"/api/molecular-profiles/study_es_0_mutations",
		"/api/sample-lists/study_es_0_all",
		"/api/genes/TP53",
		"/api/cancer-types",
		"/api/legacy/studies/study_es_0",
	} {
		path, query, _ := strings.Cut(p, "?")
		if query != "" {
			query = "?" + query
		}
		resp, want := rawDo(t, "GET", p, "")
		wantEq(t, resp.StatusCode, 200)
		resp, got := rawDo(t, "GET", path+"/"+query, "")
		if resp.StatusCode != 200 || string(got) != string(want) {
			t.Errorf("GET %s/%s: status %d, body differs from the path without the slash: %.200s", path, query, resp.StatusCode, got)
		}
	}
	resp, body := rawDo(t, "GET", "/api/studies/no_such_study/", "")
	wantEq(t, resp.StatusCode, 404)
	wantEq(t, string(body), `{"message":"Study not found: no_such_study"}`)
}

func TestContractTrailingSlashPost(t *testing.T) {
	requireAPI(t)
	for _, c := range []struct{ path, body string }{
		{"/api/studies/fetch", `["study_es_0"]`},
		{"/api/samples/fetch", `{"sampleListIds":["study_es_0_all"]}`},
		{"/api/clinical-data-counts/fetch", `{"attributes":[{"attributeId":"CANCER_TYPE"}],"studyViewFilter":{"studyIds":["study_es_0"]}}`},
	} {
		_, want := rawDo(t, "POST", c.path, c.body)
		resp, got := rawDo(t, "POST", c.path+"/", c.body)
		if resp.StatusCode != 200 || string(got) != string(want) {
			t.Errorf("POST %s/: status %d, body differs from the path without the slash: %.200s", c.path, resp.StatusCode, got)
		}
	}
	// The legacy endpoints' filter is parsed by an interceptor that matches
	// the exact path; without it the controller fails.
	resp, body := rawDo(t, "POST", "/api/clinical-data/fetch/?clinicalDataType=SAMPLE",
		`{"attributeIds":["CANCER_TYPE"],"identifiers":[{"entityId":"TCGA-A1-A0SB-01","studyId":"study_es_0"}]}`)
	wantEq(t, resp.StatusCode, 500)
	if !strings.Contains(string(body), `"path":"/api/clinical-data/fetch/"`) {
		t.Errorf("POST /api/clinical-data/fetch/: body %s", body)
	}
	resp, body = rawDo(t, "POST", "/api/studies/study_es_0/", "{}")
	wantEq(t, resp.StatusCode, 405)
	if !strings.Contains(string(body), `"path":"/api/studies/study_es_0/"`) {
		t.Errorf("POST /api/studies/study_es_0/: body %s", body)
	}
}
