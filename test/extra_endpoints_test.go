package contract

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Contracts for the Java endpoints served outside the main route table: the
// /api/legacy/ and /api/not-ready-yet/ aliases, GET /api/studies/meta, and the
// cache-statistics endpoints of a portal without a cache.

func rawDo(t *testing.T, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, apiBase()+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestContractStudiesMeta(t *testing.T) {
	requireAPI(t)
	var all []map[string]any
	get(t, "/api/studies?projection=SUMMARY", &all)
	resp, body := rawDo(t, "GET", "/api/studies/meta", "")
	wantEq(t, resp.StatusCode, 200)
	wantEq(t, len(body), 0)
	wantEq(t, resp.Header.Get("total-count"), itoa(len(all)))
	wantEq(t, resp.Header.Get("X-Total-Count"), itoa(len(all)))

	var kw []map[string]any
	get(t, "/api/studies?keyword=study_es_0", &kw)
	resp, _ = rawDo(t, "GET", "/api/studies/meta?keyword=study_es_0&sortBy=name&direction=DESC", "")
	wantEq(t, resp.Header.Get("X-Total-Count"), itoa(len(kw)))

	resp, _ = rawDo(t, "GET", "/api/studies?projection=META", "")
	wantEq(t, resp.Header.Get("X-Total-Count"), itoa(len(all)))
	wantEq(t, resp.Header.Get("total-count"), itoa(len(all)))
}

func TestContractNotReadyYetAliases(t *testing.T) {
	requireAPI(t)
	body := `{"studyIds":["study_es_0"],"attributeIds":["CANCER_TYPE"]}`
	_, want := rawDo(t, "POST", "/api/clinical-data/fetch?clinicalDataType=SAMPLE", body)
	resp, got := rawDo(t, "POST", "/api/not-ready-yet/clinical-data/fetch?clinicalDataType=SAMPLE", body)
	wantEq(t, resp.StatusCode, 200)
	wantEq(t, string(got), string(want))

	mut := `{"sampleMolecularIdentifiers":[{"molecularProfileId":"study_es_0_mutations","sampleId":"TCGA-A1-A0SB-01"}]}`
	_, want = rawDo(t, "POST", "/api/mutations/fetch", mut)
	resp, got = rawDo(t, "POST", "/api/not-ready-yet/mutations/fetch", mut)
	wantEq(t, resp.StatusCode, 200)
	wantEq(t, string(got), string(want))
}

func TestContractLegacyAliases(t *testing.T) {
	requireAPI(t)
	for _, p := range []string{"/studies/study_es_0", "/studies/study_es_0/samples", "/samples?keyword=TCGA-A1-A0SB"} {
		_, want := rawDo(t, "GET", "/api"+p, "")
		resp, got := rawDo(t, "GET", "/api/legacy"+p, "")
		wantEq(t, resp.StatusCode, 200)
		wantEq(t, string(got), string(want))
	}
	resp, _ := rawDo(t, "GET", "/api/legacy/studies?projection=META", "")
	if resp.Header.Get("total-count") == "" {
		t.Errorf("legacy studies META: no total-count header")
	}

	ga := `{"molecularProfileIds":["study_es_0_treatment_ic50"]}`
	_, want := rawDo(t, "POST", "/api/generic-assay-meta/fetch", ga)
	resp, got := rawDo(t, "POST", "/api/legacy/generic_assay_meta/fetch", ga)
	wantEq(t, resp.StatusCode, 200)
	wantEq(t, string(got), string(want))
}

func TestContractCacheStatsDisabled(t *testing.T) {
	requireAPI(t)
	for _, p := range []string{"/api/cacheStatistics", "/api/GeneralRepositoryCache/keysInCache"} {
		resp, body := rawDo(t, "GET", p, "")
		wantEq(t, resp.StatusCode, 404)
		var e map[string]any
		if err := json.Unmarshal(body, &e); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		wantEq(t, e["path"], p)
		wantEq(t, e["error"], "Not Found")
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
