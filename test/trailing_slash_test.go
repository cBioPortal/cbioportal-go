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
		// Legacy endpoints whose filter Java's
		// InvolvedCancerStudyExtractorInterceptor parses: it matches the
		// exact path, so Java fails on these (docs/rewrite/java-quirks.md);
		// go answers them as without the slash.
		{"/api/clinical-data/fetch?clinicalDataType=SAMPLE", `{"attributeIds":["CANCER_TYPE"],"identifiers":[{"entityId":"TCGA-A1-A0SB-01","studyId":"study_es_0"}]}`},
		{"/api/clinical-events-meta/fetch", `{"patientIdentifiers":[{"patientId":"TCGA-A1-A0SB","studyId":"study_es_0"}]}`},
		{"/api/survival-data/fetch", `{"attributeIdPrefix":"CUSTOM","patientIdentifiers":[{"patientId":"TCGA-A1-A0SB","studyId":"study_es_0"}],"startEventRequestIdentifier":{"clinicalEventRequests":[{"eventType":"SPECIMEN"}],"position":"FIRST"},"endEventRequestIdentifier":{"clinicalEventRequests":[{"eventType":"STATUS"}],"position":"LAST"},"censoredEventRequestIdentifier":{"clinicalEventRequests":[{"eventType":"STATUS"}],"position":"LAST"}}`},
		{"/api/generic_assay_data/fetch", `{"molecularProfileIds":["study_es_0_mutational_signature"],"genericAssayStableIds":["confidence_1"]}`},
		{"/api/mutations/fetch?projection=SUMMARY", `{"molecularProfileIds":["study_es_0_mutations"],"entrezGeneIds":[7157]}`},
		{"/api/treatments/patient?tier=Agent", `{"studyIds":["study_es_0"]}`},
	} {
		path, query, _ := strings.Cut(c.path, "?")
		if query != "" {
			query = "?" + query
		}
		_, want := rawDo(t, "POST", c.path, c.body)
		resp, got := rawDo(t, "POST", path+"/"+query, c.body)
		if resp.StatusCode != 200 || string(got) != string(want) {
			t.Errorf("POST %s/%s: status %d, body differs from the path without the slash: %.200s", path, query, resp.StatusCode, got)
		}
	}
	// A body that is not JSON answers as without the slash.
	wantResp, want := rawDo(t, "POST", "/api/clinical-data/fetch", "{")
	resp, body := rawDo(t, "POST", "/api/clinical-data/fetch/", "{")
	wantEq(t, resp.StatusCode, wantResp.StatusCode)
	wantEq(t, string(body), string(want))
	resp, body = rawDo(t, "POST", "/api/studies/study_es_0/", "{}")
	wantEq(t, resp.StatusCode, 405)
	if !strings.Contains(string(body), `"path":"/api/studies/study_es_0/"`) {
		t.Errorf("POST /api/studies/study_es_0/: body %s", body)
	}
}
