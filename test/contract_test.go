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
	"encoding/json"
	"fmt"
	"maps"
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

type geneCount struct {
	HugoGeneSymbol       string   `json:"hugoGeneSymbol"`
	NumberOfAlteredCases int      `json:"numberOfAlteredCases"`
	TotalCount           int      `json:"totalCount"`
	Alteration           *int     `json:"alteration"`
	HugoGeneSymbols      []string `json:"hugoGeneSymbols"`
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
	maps.Copy(m, over)
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
	key := javaKey("TCGA-A1-A0SB", "study_es_0")
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
		// Records without Zygosity values carry an empty object.
		z, ok := m.NamespaceColumns["Zygosity"]
		if !ok || len(z) == 0 {
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
	post(t, "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch?discreteCopyNumberEventType=HOMDEL_AND_AMP",
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
	key := javaKey("TCGA-A1-A0SB", "study_es_0")
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
	post(t, "/api/gene-panels/fetch?projection=DETAILED", []string{"TESTPANEL1", "TESTPANEL2"}, &d)
	got := map[string]int{}
	for _, p := range d {
		got[p.GenePanelID] = len(p.Genes)
	}
	if got["TESTPANEL1"] == 0 || got["TESTPANEL2"] == 0 {
		t.Fatalf("gene-panels/fetch: %v", got)
	}
}

// Profiled = member of the profile (sample_profile), as in Java: for
// study_es_0_mutations that is the 31 samples of its _sequenced list.
func TestContractGenePanelDataProfiledMembership(t *testing.T) {
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
	wantEq(t, []int{profiled, len(d)}, []int{31, 845})
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
	wantEq(t, totals(germOff)["BRAF"], 37)
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

// Session service: a main_session posted by the results view is served back
// under the same id with the session envelope.
func TestContractMainSessionRoundTrip(t *testing.T) {
	requireAPI(t)
	data := map[string]any{"cancer_study_list": "study_es_0", "gene_list": "TP53", "nonce": time.Now().UnixNano()}
	var created struct {
		ID string `json:"id"`
	}
	post(t, "/api/session/main_session", data, &created)
	if len(created.ID) != 24 {
		t.Fatalf("want 24-char id, got %q", created.ID)
	}
	var got struct {
		ID     string         `json:"id"`
		Source string         `json:"source"`
		Type   string         `json:"type"`
		Data   map[string]any `json:"data"`
	}
	get(t, "/api/session/main_session/"+created.ID, &got)
	wantEq(t, []any{got.ID, got.Type, got.Data["gene_list"]},
		[]any{created.ID, "main_session", "TP53"})
	// The source is the store's: the session service's portal instance, or
	// "cbioportal" for the built-in store.
	if got.Source == "" {
		t.Errorf("session without source")
	}
}

// A created (not saved) virtual study gets the requester as owner ("anonymous"
// without a login) and no users.
func TestContractVirtualStudyRoundTrip(t *testing.T) {
	requireAPI(t)
	vs := map[string]any{
		"name": fmt.Sprintf("contract vs %d", time.Now().UnixNano()), "description": "d",
		"studies": []any{map[string]any{"id": "study_es_0", "samples": []string{"TCGA-A1-A0SB-01"}}},
		"origin":  []string{"study_es_0"},
	}
	var created struct {
		ID string `json:"id"`
	}
	post(t, "/api/session/virtual_study", vs, &created)
	var got struct {
		ID   string `json:"id"`
		Data struct {
			Name    string   `json:"name"`
			Owner   string   `json:"owner"`
			Users   []string `json:"users"`
			Origin  []string `json:"origin"`
			Studies []struct {
				ID      string   `json:"id"`
				Samples []string `json:"samples"`
			} `json:"studies"`
		} `json:"data"`
	}
	get(t, "/api/session/virtual_study/"+created.ID, &got)
	wantEq(t, []any{got.ID, got.Data.Name, len(got.Data.Users), got.Data.Origin},
		[]any{created.ID, vs["name"], 0, []string{"study_es_0"}})
	if got.Data.Owner == "" {
		t.Errorf("virtual study has no owner")
	}
	if len(got.Data.Studies) != 1 || got.Data.Studies[0].ID != "study_es_0" {
		t.Errorf("studies not round-tripped: %+v", got.Data.Studies)
	}
}

func TestContractUserVirtualStudiesAnonymousEmpty(t *testing.T) {
	requireAPI(t)
	var cfg map[string]any
	get(t, "/config_service", &cfg)
	if u, _ := cfg["user_display_name"].(string); u != "" && u != "anonymousUser" {
		t.Skipf("server runs with a logged-in user (%s)", u)
	}
	var got []any
	get(t, "/api/session/virtual_study", &got)
	if got == nil || len(got) != 0 {
		t.Errorf("want [], got %v", got)
	}
}

func TestContractPublicVirtualStudiesArray(t *testing.T) {
	requireAPI(t)
	var got []map[string]any
	get(t, "/api/public_virtual_studies", &got)
	if got == nil {
		t.Errorf("want a JSON array, got null")
	}
}

// TestContractAlterationEnrichmentsProfiledPerGene pins that a group's
// profiled count is per gene: the samples whose gene panel has the gene.
// Java uses the whole group's count when no sample of the group has the
// gene altered, so a gene off the group's panel counts every sample as
// profiled.
func TestContractAlterationEnrichmentsProfiledPerGene(t *testing.T) {
	requireAPI(t)
	group := func(name string, ids ...string) map[string]any {
		var cs []map[string]any
		for _, id := range ids {
			cs = append(cs, map[string]any{"caseId": id, "molecularProfileId": "study_es_0_mutations"})
		}
		return map[string]any{"name": name, "molecularProfileCaseIdentifiers": cs}
	}
	// TESTPANEL2 has no BRCA1 or BRCA2; the whole-exome samples are profiled
	// for every gene.
	body := map[string]any{
		"molecularProfileCasesGroupFilter": []map[string]any{
			group("panel", "TCGA-A1-A0SB-02", "TEST_SAMPLE_14", "TEST_SAMPLE_15"),
			group("wes", "TCGA-A1-A0SK-01", "TCGA-A2-A04P-01", "TCGA-A2-A0CM-01", "TCGA-AR-A1AR-01", "TCGA-B6-A0I6-01",
				"TCGA-B6-A0WX-01", "TCGA-BH-A0E0-01", "TCGA-BH-A0HL-01", "TCGA-BH-A18K-01", "TCGA-BH-A18V-01", "TCGA-BH-A1F0-01"),
		},
		"alterationEventTypes": map[string]any{"mutationEventTypes": map[string]any{"any": true}},
	}
	for _, path := range []string{"/api/alteration-enrichments/fetch", "/api/legacy/alteration-enrichments/fetch"} {
		var d []struct {
			HugoGeneSymbol string  `json:"hugoGeneSymbol"`
			PValue         float64 `json:"pValue"`
			Counts         []struct {
				Name          string `json:"name"`
				AlteredCount  int    `json:"alteredCount"`
				ProfiledCount int    `json:"profiledCount"`
			} `json:"counts"`
		}
		post(t, path+"?enrichmentType=SAMPLE", body, &d)
		found := false
		for _, r := range d {
			if r.HugoGeneSymbol != "BRCA2" {
				continue
			}
			found = true
			for _, c := range r.Counts {
				if c.Name == "panel" {
					wantEq(t, []any{path, c.AlteredCount, c.ProfiledCount, r.PValue}, []any{path, 0, 0, 0.0})
				}
			}
		}
		if !found {
			t.Errorf("%s: no BRCA2 row", path)
		}
	}
}

// TestContractClinicalTableWithoutPageSize pins that the clinical table
// without pageSize lists every sample. Java's default page size 0 means no
// paging, but its paging cuts the list to 0 samples.
func TestContractClinicalTableWithoutPageSize(t *testing.T) {
	requireAPI(t)
	var d struct {
		ByUniqueSampleKey map[string]json.RawMessage `json:"byUniqueSampleKey"`
	}
	post(t, "/api/clinical-data-table/fetch", map[string]any{"studyIds": []string{"study_es_0"}}, &d)
	var samples []json.RawMessage
	post(t, "/api/samples/fetch?projection=ID", map[string]any{"sampleListIds": []string{"study_es_0_all"}}, &samples)
	wantEq(t, len(d.ByUniqueSampleKey), len(samples))
}

// TestContractClinicalFilterOnMissingAttribute pins that a clinical data
// filter on an attribute none of the studies has matches no sample. Java's
// legacy endpoints drop such a filter and answer every sample; its newer
// endpoints and go apply it.
func TestContractClinicalFilterOnMissingAttribute(t *testing.T) {
	requireAPI(t)
	filter := map[string]any{
		"studyIds":            []string{"study_es_0"},
		"clinicalDataFilters": []map[string]any{{"attributeId": "CANCER_STUDY", "values": []map[string]any{{"value": "study_es_0"}}}},
	}
	for _, path := range []string{"/api/filtered-samples/fetch", "/api/legacy/filtered-samples/fetch"} {
		var d []json.RawMessage
		post(t, path, filter, &d)
		wantEq(t, []any{path, len(d)}, []any{path, 0})
	}
}

// TestContractLegacyClinicalDataCounts pins the legacy clinical data counts:
// one item per distinct attribute, also when no filtered case has a value
// and several studies define the attribute (the column-store endpoint adds
// an NA item per study), and an item for an attribute no study defines,
// with NA for every filtered patient when a patient attribute is also
// asked for.
func TestContractLegacyClinicalDataCounts(t *testing.T) {
	requireAPI(t)
	filter := map[string]any{"sampleIdentifiers": []map[string]string{
		{"studyId": "study_es_0", "sampleId": "TCGA-A2-A04P-01"},
		{"studyId": "lgg_ucsf_2014_test_generic_assay", "sampleId": "P02_Pri"},
	}}
	counts := func(attrs ...string) map[string]string {
		var as []map[string]string
		for _, a := range attrs {
			as = append(as, map[string]string{"attributeId": a})
		}
		var d []struct {
			AttributeID string            `json:"attributeId"`
			Counts      []json.RawMessage `json:"counts"`
		}
		post(t, "/api/legacy/clinical-data-counts/fetch", map[string]any{"attributes": as, "studyViewFilter": filter}, &d)
		out := map[string]string{}
		for _, item := range d {
			if _, dup := out[item.AttributeID]; dup {
				t.Errorf("%v: %s twice", attrs, item.AttributeID)
			}
			b, _ := json.Marshal(item.Counts)
			out[item.AttributeID] = string(b)
		}
		return out
	}
	wantEq(t, counts("OS_STATUS"), map[string]string{"OS_STATUS": `[{"value":"NA","count":2}]`})
	wantEq(t, counts("NO_SUCH", "OS_STATUS")["NO_SUCH"], `[{"value":"NA","count":2}]`)
	wantEq(t, counts("NO_SUCH", "MUTATION_COUNT")["NO_SUCH"], `[]`)
	// NA comes after the values, as Java adds it last.
	filter["sampleIdentifiers"] = append(filter["sampleIdentifiers"].([]map[string]string),
		map[string]string{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SB-01"})
	wantEq(t, counts("OS_STATUS")["OS_STATUS"], `[{"value":"0:LIVING","count":1},{"value":"NA","count":2}]`)
}
