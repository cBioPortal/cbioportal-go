// Package contract holds black-box API contract tests: they assert the exact
// endpoint behaviours the cBioPortal frontend / e2e suite depends on, against a
// running cbioportal-api. They give sub-second regression feedback during
// backend refactoring, instead of a ~45-minute Playwright run.
//
//	go test ./test/ -run Contract            # against http://localhost:18090
//	API=http://localhost:18091 go test ./test/ -run Contract
//
// The whole suite skips (not fails) when the API is not reachable, so a normal
// `go test ./...` stays green without a live server + ClickHouse. Seed a new
// case whenever a backend contract is nailed down.
package contract

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"
)

func apiBase() string {
	if v := os.Getenv("API"); v != "" {
		return v
	}
	return "http://localhost:18090"
}

// requireAPI skips the test unless the API answers /health.
func requireAPI(t *testing.T) {
	t.Helper()
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(apiBase() + "/health")
	if err != nil {
		t.Skipf("api not reachable at %s (%v) — skipping contract tests", apiBase(), err)
	}
	resp.Body.Close()
}

// do sends an optional JSON body (gzip-compressed when gz) and decodes the JSON
// response into out.
func do(t *testing.T, method, path string, body any, gz bool, out any) {
	t.Helper()
	var r io.Reader
	h := http.Header{"Content-Type": {"application/json"}}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		if gz {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			zw.Write(raw)
			zw.Close()
			raw = buf.Bytes()
			h.Set("Content-Encoding", "gzip")
		}
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, apiBase()+path, r)
	req.Header = h
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s -> %d: %s", method, path, resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
}

func post(t *testing.T, path string, body any, out any) { do(t, "POST", path, body, false, out) }
func get(t *testing.T, path string, out any)            { do(t, "GET", path, nil, false, out) }

type geneCount struct {
	HugoGeneSymbol        string   `json:"hugoGeneSymbol"`
	NumberOfAlteredCases  int      `json:"numberOfAlteredCases"`
	TotalCount            int      `json:"totalCount"`
	Alteration            *int     `json:"alteration"`
	HugoGeneSymbols       []string `json:"hugoGeneSymbols"`
}

func totals(rows []geneCount) map[string]int {
	m := map[string]int{}
	for _, r := range rows {
		m[r.HugoGeneSymbol] = r.TotalCount
	}
	return m
}

func wantEq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mismatch\n want: %v\n got:  %v", want, got)
	}
}

// allTiers mirrors the study view's default tier map; af builds an AlterationFilter.
var allTiers = map[string]bool{"Class 1": true, "Class 2": true, "Class 3": true, "Class 4": true}

func af(over map[string]any) map[string]any {
	m := map[string]any{
		"includeDriver": true, "includeVUS": true, "includeUnknownOncogenicity": true,
		"includeGermline": true, "includeSomatic": true, "includeUnknownStatus": true,
		"includeUnknownTier": true, "tiersBooleanMap": allTiers,
	}
	for k, v := range over {
		m[k] = v
	}
	return m
}

func studyFilter(alt map[string]any) map[string]any {
	f := map[string]any{"studyIds": []string{"study_es_0"}}
	if alt != nil {
		f["alterationFilter"] = alt
	}
	return f
}

// ---------------------------------------------------------------- config ----

func TestContractConfigServiceGatingFlags(t *testing.T) {
	requireAPI(t)
	var d map[string]any
	get(t, "/config_service", &d)
	if len(d) < 100 {
		t.Fatalf("config too small: %d keys", len(d))
	}
	for k, v := range map[string]bool{"skin_show_settings_menu": true, "show_oncokb": true, "show_hotspot": true, "skin_show_gsva": true} {
		if d[k] != v {
			t.Errorf("config %s = %v, want %v", k, d[k], v)
		}
	}
}

// ---------------------------------------------------- fetch request forms ----

func TestContractSamplesFetchBySampleList(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/samples/fetch?projection=DETAILED", map[string]any{"sampleListIds": []string{"study_es_0_all"}}, &d)
	wantEq(t, len(d), 845)
}

func TestContractPatientsFetchByUniqueKeys(t *testing.T) {
	requireAPI(t)
	key := base64.RawStdEncoding.EncodeToString([]byte("study_es_0:TCGA-A1-A0SB"))
	var d []map[string]any
	post(t, "/api/patients/fetch", map[string]any{"uniquePatientKeys": []string{key}}, &d)
	if len(d) != 1 || d[0]["patientId"] != "TCGA-A1-A0SB" {
		t.Fatalf("patients by uniquePatientKeys: %v", d)
	}
}

func TestContractMutationsFetchByProfileIds(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/mutations/fetch?projection=DETAILED",
		map[string]any{"molecularProfileIds": []string{"study_es_0_mutations"}, "entrezGeneIds": []int{3983, 7157}}, &d)
	if len(d) == 0 {
		t.Fatal("mutations/fetch molecularProfileIds returned []")
	}
	hasDriver := false
	for _, m := range d {
		if m["driverFilter"] != nil && m["driverFilter"] != "" {
			hasDriver = true
		}
	}
	if !hasDriver {
		t.Error("no driverFilter on mutations")
	}
}

func TestContractMutationNamespaceColumns(t *testing.T) {
	requireAPI(t)
	var d []struct {
		NamespaceColumns map[string]map[string]any `json:"namespaceColumns"`
	}
	post(t, "/api/mutations/fetch?projection=DETAILED",
		map[string]any{"molecularProfileIds": []string{"study_es_0_mutations"}}, &d)
	found := false
	for _, m := range d {
		z, ok := m.NamespaceColumns["Zygosity"]
		if !ok {
			continue
		}
		found = true
		if _, ok := z["name"]; !ok {
			t.Errorf("Zygosity missing 'name' subkey: %v", z)
		}
		if _, ok := z["code"]; !ok {
			t.Errorf("Zygosity missing 'code' subkey: %v", z)
		}
	}
	if !found {
		t.Error("no mutation carried a Zygosity namespaceColumns object")
	}
}

func TestContractCnaNamespaceColumns(t *testing.T) {
	requireAPI(t)
	var d []struct {
		HugoGeneSymbol   string                    `json:"hugoGeneSymbol"`
		NamespaceColumns map[string]map[string]any `json:"namespaceColumns"`
	}
	post(t, "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch?discreteCopyNumberEventType=ALL",
		map[string]any{"sampleIds": []string{"TCGA-A2-A04U-01"}, "entrezGeneIds": []int{116983}}, &d)
	if len(d) == 0 {
		t.Fatal("no ACAP3 CNA row")
	}
	ns := d[0].NamespaceColumns
	wantEq(t, ns["MyNamespace"]["column1"], "value1b")
	wantEq(t, ns["MyNamespace"]["column2"], "value2b")
	wantEq(t, ns["MyNamespace2"]["blarp"], "bluirp2")
}

func TestContractGzipRequestBody(t *testing.T) {
	requireAPI(t)
	key := base64.RawStdEncoding.EncodeToString([]byte("study_es_0:TCGA-A1-A0SB"))
	var d []map[string]any
	do(t, "POST", "/api/patients/fetch", map[string]any{"uniquePatientKeys": []string{key}}, true, &d)
	wantEq(t, len(d), 1)
}

// ---------------------------------------------------- molecular profiles ----

func TestContractMolecularProfilesShape(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	get(t, "/api/studies/study_es_0/molecular-profiles", &d)
	for _, p := range d {
		if p["molecularProfileId"] == "study_es_0_gistic" {
			wantEq(t, p["datatype"], "DISCRETE")
		}
		if at, _ := p["molecularAlterationType"].(string); at == "GENERIC_ASSAY" {
			if p["genericAssayType"] == nil || p["genericAssayType"] == "" {
				t.Errorf("generic-assay profile %v missing genericAssayType", p["molecularProfileId"])
			}
		}
	}
}

func TestContractStudyHasCancerTypeObject(t *testing.T) {
	requireAPI(t)
	var d map[string]any
	get(t, "/api/studies/study_es_0", &d)
	ct, _ := d["cancerType"].(map[string]any)
	if ct == nil || ct["name"] == nil || ct["name"] == "" {
		t.Fatalf("study missing nested cancerType.name: %v", d["cancerType"])
	}
}

func TestContractGenePanelsFetchPost(t *testing.T) {
	requireAPI(t)
	var d []struct {
		GenePanelID string `json:"genePanelId"`
		Genes       []any  `json:"genes"`
	}
	post(t, "/api/gene-panels/fetch", []string{"TESTPANEL1", "TESTPANEL2"}, &d)
	got := map[string]int{}
	for _, p := range d {
		got[p.GenePanelID] = len(p.Genes)
	}
	if got["TESTPANEL1"] == 0 || got["TESTPANEL2"] == 0 {
		t.Fatalf("gene-panels/fetch: %v", got)
	}
}

func TestContractGenePanelDataWholeGenomeProfiled(t *testing.T) {
	requireAPI(t)
	var d []struct {
		Profiled bool `json:"profiled"`
	}
	post(t, "/api/gene-panel-data/fetch", map[string]any{"molecularProfileIds": []string{"study_es_0_mutations"}}, &d)
	profiled := 0
	for _, x := range d {
		if x.Profiled {
			profiled++
		}
	}
	if profiled < 800 {
		t.Fatalf("expected most samples whole-genome profiled, got %d/%d", profiled, len(d))
	}
}

// ------------------------------------------- study-view alteration filter ----

func TestContractMutatedGenesExcludeSomatic(t *testing.T) {
	requireAPI(t)
	var d []geneCount
	post(t, "/api/mutated-genes/fetch", studyFilter(af(map[string]any{"includeSomatic": false})), &d)
	wantEq(t, totals(d), map[string]int{"BRCA2": 6, "TP53": 1, "BRCA1": 1, "ATM": 1})
}

func TestContractMutatedGenesExcludeDriver(t *testing.T) {
	requireAPI(t)
	var d []geneCount
	post(t, "/api/mutated-genes/fetch", studyFilter(af(map[string]any{"includeDriver": false})), &d)
	wantEq(t, totals(d), map[string]int{
		"BRCA2": 12, "ACP3": 5, "BRCA1": 3, "DTNB": 1, "TP53": 2, "TMEM247": 1,
		"ADAMTS20": 1, "MYB": 1, "MSH3": 1, "ATM": 2, "PIEZO1": 1,
	})
}

func TestContractCnaGenesDeepEventsOnly(t *testing.T) {
	requireAPI(t)
	var d []geneCount
	post(t, "/api/cna-genes/fetch", studyFilter(nil), &d)
	set := map[int]bool{}
	for _, r := range d {
		if r.Alteration != nil {
			set[*r.Alteration] = true
		}
	}
	var alts []int
	for a := range set {
		alts = append(alts, a)
	}
	sort.Ints(alts)
	wantEq(t, alts, []int{-2, 2})
}

func TestContractCnaGenesExcludeDriver(t *testing.T) {
	requireAPI(t)
	var d []geneCount
	post(t, "/api/cna-genes/fetch", studyFilter(af(map[string]any{"includeDriver": false})), &d)
	amp := map[string]int{}
	for _, r := range d {
		if r.Alteration != nil && *r.Alteration == 2 {
			amp[r.HugoGeneSymbol] = r.NumberOfAlteredCases
		}
	}
	for g, want := range map[string]int{"ERCC5": 6, "AGRN": 6, "ATAD3A": 7} {
		if amp[g] != want {
			t.Errorf("cna AMP %s excl-driver = %d, want %d", g, amp[g], want)
		}
	}
}

func TestContractSvGenesStatusFilter(t *testing.T) {
	requireAPI(t)
	var somaticOff []geneCount
	post(t, "/api/structuralvariant-genes/fetch", studyFilter(af(map[string]any{"includeSomatic": false})), &somaticOff)
	wantEq(t, len(somaticOff), 0)

	var germOff []geneCount
	post(t, "/api/structuralvariant-genes/fetch", studyFilter(af(map[string]any{"includeGermline": false})), &germOff)
	braf := 0
	for _, r := range germOff {
		for _, g := range r.HugoGeneSymbols {
			if g == "BRAF" {
				braf += r.TotalCount
			}
		}
	}
	wantEq(t, braf, 37)
}

func TestContractAlterationEnrichmentsProfileTyped(t *testing.T) {
	requireAPI(t)
	group := func(name string, ids ...string) map[string]any {
		var cs []map[string]any
		for _, id := range ids {
			cs = append(cs, map[string]any{"caseId": id, "molecularProfileId": "study_es_0_mutations"})
		}
		return map[string]any{"name": name, "molecularProfileCaseIdentifiers": cs}
	}
	body := map[string]any{"molecularProfileCasesGroupFilter": []map[string]any{
		group("a", "TCGA-A2-A04P-01", "TCGA-A1-A0SB-01"),
		group("b", "TCGA-A1-A0SK-01"),
	}}
	var d []struct {
		HugoGeneSymbol string `json:"hugoGeneSymbol"`
	}
	post(t, "/api/alteration-enrichments/fetch", body, &d)
	cnaGenes := map[string]bool{"ACAP3": true, "AGRN": true, "ATAD3A": true, "ATAD3B": true, "ATAD3C": true, "AURKAIP1": true, "ERCC5": true}
	for _, r := range d {
		if cnaGenes[r.HugoGeneSymbol] {
			t.Errorf("mutations-profile enrichment leaked CNA gene %s", r.HugoGeneSymbol)
		}
	}
}

var _ = fmt.Sprintf // keep fmt imported for future cases
