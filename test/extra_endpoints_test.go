package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
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

// The legacy aliases answer as their /api/ endpoints, except the legacy
// studies, which have a model of their own (TestContractLegacyStudies).
func TestContractLegacyAliases(t *testing.T) {
	requireAPI(t)
	for _, p := range []string{"/studies/study_es_0/samples", "/samples?keyword=TCGA-A1-A0SB"} {
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

// The legacy study endpoints write the legacy model: the nested cancer type
// keyed cancerTypeId and placed before allSampleCount. The list's ID
// projection lists the studies by id, META answers only the total-count
// header, and a keyword matches case-sensitively, with names that start with
// it first.
func TestContractLegacyStudies(t *testing.T) {
	requireAPI(t)
	resp, body := rawDo(t, "GET", "/api/legacy/studies/study_es_0", "")
	wantEq(t, resp.StatusCode, 200)
	var st struct {
		CancerType map[string]any `json:"cancerType"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	wantEq(t, st.CancerType["cancerTypeId"], "brca-es0")
	if _, ok := st.CancerType["id"]; ok {
		t.Errorf("legacy cancer type keyed id: %v", st.CancerType)
	}
	if i, j := bytes.Index(body, []byte(`"cancerType"`)), bytes.Index(body, []byte(`"allSampleCount"`)); i < 0 || j < i {
		t.Errorf("cancerType not before allSampleCount: %s", body)
	}

	ids := listOf(t, "GET", "/api/legacy/studies?projection=ID", "")
	studies := fieldOf(ids, "studyId")
	if len(studies) < 3 || !slices.IsSorted(studies) {
		t.Errorf("ID projection: %v", studies)
	}
	for _, s := range ids {
		wantEq(t, len(s), 2)
	}

	resp, body = rawDo(t, "GET", "/api/legacy/studies?projection=META&keyword=es", "")
	wantEq(t, resp.StatusCode, 200)
	wantEq(t, len(body), 0)
	if resp.Header.Get("total-count") == "" {
		t.Errorf("META: no total-count")
	}

	upper := fieldOf(listOf(t, "GET", "/api/legacy/studies?keyword=Study", ""), "studyId")
	lower := fieldOf(listOf(t, "GET", "/api/legacy/studies?keyword=study", ""), "studyId")
	wantEq(t, upper, []string{"study_hg38", "ascn_test_study"})
	if slices.Equal(upper, lower) {
		t.Errorf("keyword matched case-insensitively: %v", lower)
	}
}

// TestContractLegacyStudiesPrimarySite pins StudyServiceImpl's
// findPrimarySiteMatchingStudies: after the keyword matches come the studies
// whose cancer type's primary site has an id or name containing the
// keyword, ignoring case, in the SUMMARY projection whatever the request's,
// and META counts them.
func TestContractLegacyStudiesPrimarySite(t *testing.T) {
	requireAPI(t)
	// No study name, id or cancer type contains "breast" case-sensitively,
	// but three studies' cancer types sit under the Breast primary site.
	added := listOf(t, "GET", "/api/legacy/studies?keyword=breast&projection=ID", "")
	ids := fieldOf(added, "studyId")
	slices.Sort(ids)
	wantEq(t, ids, []string{"study_es_0", "study_hg38", "teststudy_genepanels"})
	for _, s := range added {
		if s["name"] == nil || s["cancerType"] != nil {
			t.Errorf("added study not in the SUMMARY projection: %v", s)
		}
	}
	resp, _ := rawDo(t, "GET", "/api/legacy/studies?keyword=breast&projection=META", "")
	wantEq(t, resp.Header.Get("total-count"), "3")
	// Pages cut one list (Java starts the added studies again on every
	// short page, so its pages repeat them).
	var paged []string
	for n := range 3 {
		paged = append(paged, fieldOf(listOf(t, "GET", fmt.Sprintf("/api/legacy/studies?keyword=breast&pageSize=2&pageNumber=%d", n), ""), "studyId")...)
	}
	slices.Sort(paged)
	wantEq(t, paged, ids)
	// Matches keep their projection and are not listed twice.
	matched := listOf(t, "GET", "/api/legacy/studies?keyword=Breast&projection=ID", "")
	wantEq(t, len(matched), 3)
	for _, s := range matched {
		wantEq(t, len(s), 2)
	}
	wantEq(t, len(listOf(t, "GET", "/api/legacy/studies?keyword=Melanoma", "")), 1)
	wantEq(t, fieldOf(listOf(t, "GET", "/api/legacy/studies?keyword=SKIN", ""), "studyId"), []string{"ascn_test_study"})
}
