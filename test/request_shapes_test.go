package contract

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// Request-shape contracts: every body form, query parameter and HTTP method
// the Java controllers accept is honoured (bare JSON lists, the alternative
// forms of the filter objects, paging/sorting params, endpoint-specific
// params).

func ids(rows []map[string]any, field string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i], _ = r[field].(string)
	}
	return out
}

// ------------------------------------------------------------- paging ----

func TestContractPagingSlicesGetLists(t *testing.T) {
	requireAPI(t)
	var all, page []map[string]any
	get(t, "/api/studies?projection=SUMMARY", &all)
	get(t, "/api/studies?projection=SUMMARY&pageSize=2&pageNumber=1", &page)
	if len(all) < 4 {
		t.Skipf("need >= 4 studies, have %d", len(all))
	}
	wantEq(t, ids(page, "studyId"), ids(all, "studyId")[2:4])

	var beyond []map[string]any
	get(t, "/api/studies/study_es_0/samples?pageSize=10&pageNumber=1000", &beyond)
	wantEq(t, len(beyond), 0)
}

func TestContractPagingSortByDirection(t *testing.T) {
	requireAPI(t)
	var asc, desc []map[string]any
	get(t, "/api/studies/study_es_0/patients?sortBy=patientId&direction=ASC&pageSize=5", &asc)
	get(t, "/api/studies/study_es_0/patients?sortBy=patientId&direction=DESC&pageSize=5", &desc)
	a, d := ids(asc, "patientId"), ids(desc, "patientId")
	if len(a) != 5 || len(d) != 5 {
		t.Fatalf("page sizes: %d / %d", len(a), len(d))
	}
	if !sort.StringsAreSorted(a) {
		t.Errorf("ASC not sorted: %v", a)
	}
	if !sort.SliceIsSorted(d, func(i, j int) bool { return d[i] > d[j] }) {
		t.Errorf("DESC not sorted: %v", d)
	}
	if a[0] >= d[0] {
		t.Errorf("first ASC %q should precede first DESC %q", a[0], d[0])
	}
}

func TestContractPatientsKeyword(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	get(t, "/api/patients?keyword=TCGA-A1-A0SB", &d)
	wantEq(t, ids(d, "patientId"), []string{"TCGA-A1-A0SB"})
}

// ------------------------------------------------------ filter forms ----

func TestContractSamplesFetchByUniqueSampleKeys(t *testing.T) {
	requireAPI(t)
	key := base64.RawStdEncoding.EncodeToString([]byte("study_es_0:TCGA-A1-A0SB-01"))
	var d []map[string]any
	post(t, "/api/samples/fetch", map[string]any{"uniqueSampleKeys": []string{key}}, &d)
	wantEq(t, ids(d, "sampleId"), []string{"TCGA-A1-A0SB-01"})
}

func TestContractMutationsBySampleList(t *testing.T) {
	requireAPI(t)
	var get1, all, cnaseq []map[string]any
	get(t, "/api/molecular-profiles/study_es_0_mutations/mutations?sampleListId=study_es_0_all&entrezGeneId=672", &get1)
	post(t, "/api/molecular-profiles/study_es_0_mutations/mutations/fetch",
		map[string]any{"sampleListId": "study_es_0_all", "entrezGeneIds": []int{672}}, &all)
	post(t, "/api/molecular-profiles/study_es_0_mutations/mutations/fetch",
		map[string]any{"sampleListId": "study_es_0_cnaseq", "entrezGeneIds": []int{672}}, &cnaseq)
	if len(all) == 0 {
		t.Fatal("no BRCA1 mutations in study_es_0_all")
	}
	wantEq(t, len(get1), len(all))
	for _, m := range get1 {
		if g := m["gene"].(map[string]any)["hugoGeneSymbol"]; g != "BRCA1" {
			t.Errorf("entrezGeneId=672 returned %v", g)
		}
	}
	if len(cnaseq) >= len(all) {
		t.Errorf("sampleListId not applied: cnaseq %d vs all %d", len(cnaseq), len(all))
	}
}

func TestContractCopyNumberSegmentsBareList(t *testing.T) {
	requireAPI(t)
	body := []map[string]string{{"studyId": "msk_impact_2017", "sampleId": "P-0004034-T01-IM5"}}
	var all, chr7 []map[string]any
	post(t, "/api/copy-number-segments/fetch", body, &all)
	post(t, "/api/copy-number-segments/fetch?chromosome=7", body, &chr7)
	if len(all) == 0 || len(chr7) == 0 || len(chr7) >= len(all) {
		t.Fatalf("segments: all %d, chr7 %d", len(all), len(chr7))
	}
	var one []map[string]any
	get(t, "/api/studies/msk_impact_2017/samples/P-0004034-T01-IM5/copy-number-segments?chromosome=7", &one)
	wantEq(t, len(one), len(chr7))
	for _, s := range one {
		wantEq(t, s["chromosome"], "7")
	}
}

func TestContractClinicalDataAttributeIdParam(t *testing.T) {
	requireAPI(t)
	var p []map[string]any
	get(t, "/api/studies/study_es_0/patients/TCGA-A1-A0SB/clinical-data?attributeId=OS_MONTHS", &p)
	wantEq(t, ids(p, "clinicalAttributeId"), []string{"OS_MONTHS"})
	var s []map[string]any
	get(t, "/api/studies/study_es_0/samples/TCGA-A1-A0SB-01/clinical-data?attributeId=NO_SUCH_ATTRIBUTE", &s)
	wantEq(t, len(s), 0)
}

func TestContractStructuralVariantQueries(t *testing.T) {
	requireAPI(t)
	profiles := []string{"study_es_0_structural_variants"}
	var all, alk, braf []map[string]any
	post(t, "/api/structural-variant/fetch", map[string]any{"molecularProfileIds": profiles}, &all)
	post(t, "/api/structural-variant/fetch", map[string]any{
		"molecularProfileIds": profiles, "entrezGeneIds": []int{},
		"structuralVariantQueries": []map[string]any{{
			"gene1": map[string]any{"entrezId": 238, "hugoSymbol": "ALK"},
			"gene2": map[string]any{"specialValue": "ANY_GENE"},
		}},
	}, &alk)
	post(t, "/api/structural-variant/fetch", map[string]any{"molecularProfileIds": profiles, "entrezGeneIds": []int{673}}, &braf)
	if len(alk) == 0 || len(alk) >= len(all) {
		t.Fatalf("ALK::* query: %d of %d", len(alk), len(all))
	}
	for _, sv := range alk {
		wantEq(t, sv["site1HugoSymbol"], "ALK")
		wantEq(t, sv["site1EntrezGeneId"], 238.0)
	}
	if len(braf) == 0 || len(braf) >= len(all) {
		t.Fatalf("entrezGeneIds [BRAF]: %d of %d", len(braf), len(all))
	}
}

func TestContractStructuralVariantCountsPairs(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/structuralvariant-counts/fetch", map[string]any{"studyIds": []string{"study_es_0"}}, &d)
	if len(d) < 2 {
		t.Fatalf("expected several gene pairs, got %d", len(d))
	}
	seen := map[string]bool{}
	for _, c := range d {
		for _, k := range []string{"gene1HugoGeneSymbol", "gene2HugoGeneSymbol", "numberOfAlteredCases", "totalCount", "numberOfProfiledCases", "matchingGenePanelIds"} {
			if _, ok := c[k]; !ok {
				t.Fatalf("missing %s in %v", k, c)
			}
		}
		key := c["gene1HugoGeneSymbol"].(string) + "::" + c["gene2HugoGeneSymbol"].(string)
		if seen[key] {
			t.Errorf("duplicate pair %s", key)
		}
		seen[key] = true
	}
	if !seen["SND1::BRAF"] {
		t.Errorf("SND1::BRAF pair missing: %v", seen)
	}
}

// ------------------------------------------------- endpoint params ----

func TestContractGenesetHierarchyBody(t *testing.T) {
	requireAPI(t)
	var bySampleList, bySamples, notGsva []map[string]any
	post(t, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_gsva_scores&sampleListId=study_es_0_all", nil, &bySampleList)
	post(t, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_gsva_scores", []string{"TCGA-A1-A0SB-01"}, &bySamples)
	post(t, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_mrna", []string{}, &notGsva)
	if len(bySampleList) != 1 || len(bySampleList[0]["genesets"].([]any)) == 0 {
		t.Fatalf("hierarchy by sample list: %v", bySampleList)
	}
	if len(bySamples) != 1 {
		t.Fatalf("hierarchy by bare sample-id list: %v", bySamples)
	}
	wantEq(t, len(notGsva), 0)
}

func TestContractTreatmentsDisplay(t *testing.T) {
	requireAPI(t)
	var p, sm, none, class bool
	post(t, "/api/treatments/display-patient", []string{"lgg_ucsf_2014_test_generic_assay"}, &p)
	post(t, "/api/treatments/display-sample", []string{"lgg_ucsf_2014_test_generic_assay"}, &sm)
	post(t, "/api/treatments/display-patient", []string{"study_es_0"}, &none)
	post(t, "/api/treatments/display-patient?tier=AgentClass", []string{"lgg_ucsf_2014_test_generic_assay"}, &class)
	wantEq(t, []bool{p, sm, none, class}, []bool{true, true, false, false})
}

func TestContractDensityPlotLogScale(t *testing.T) {
	requireAPI(t)
	path := "/api/clinical-data-density-plot/fetch?xAxisAttributeId=MUTATION_COUNT&yAxisAttributeId=FRACTION_GENOME_ALTERED&xAxisBinCount=5&yAxisBinCount=5"
	body := map[string]any{"studyIds": []string{"msk_impact_2017"}}
	var lin, lg struct {
		Bins []struct {
			BinX float64 `json:"binX"`
		} `json:"bins"`
	}
	post(t, path, body, &lin)
	post(t, path+"&xAxisLogScale=true", body, &lg)
	maxX := func(bins []struct {
		BinX float64 `json:"binX"`
	}) float64 {
		m := 0.0
		for _, b := range bins {
			m = max(m, b.BinX)
		}
		return m
	}
	if l, g := maxX(lin.Bins), maxX(lg.Bins); !(g > 0 && g < 10 && l > 100) {
		t.Errorf("log scale not applied: linear max binX %v, log max binX %v", l, g)
	}
	var vLin, vLog struct {
		AxisEnd float64 `json:"axisEnd"`
	}
	vpath := "/api/clinical-data-violin-plots/fetch?categoricalAttributeId=CANCER_TYPE&numericalAttributeId=MUTATION_COUNT"
	post(t, vpath, body, &vLin)
	post(t, vpath+"&logScale=true", body, &vLog)
	if !(vLog.AxisEnd > 0 && vLog.AxisEnd < 10 && vLin.AxisEnd > 100) {
		t.Errorf("violin logScale not applied: %v vs %v", vLin.AxisEnd, vLog.AxisEnd)
	}
}

func TestContractExpressionEnrichmentPatientType(t *testing.T) {
	requireAPI(t)
	var samples []map[string]any
	get(t, "/api/studies/study_es_0/samples", &samples)
	if len(samples) < 60 {
		t.Skip("need 60 samples")
	}
	groups := func(field string) []map[string]any {
		g := func(rows []map[string]any) []map[string]string {
			out := make([]map[string]string, len(rows))
			for i, s := range rows {
				out[i] = map[string]string{"molecularProfileId": "study_es_0_mrna", "caseId": s[field].(string)}
			}
			return out
		}
		return []map[string]any{
			{"name": "a", "molecularProfileCaseIdentifiers": g(samples[:30])},
			{"name": "b", "molecularProfileCaseIdentifiers": g(samples[30:60])},
		}
	}
	var bySample, byPatient []map[string]any
	post(t, "/api/expression-enrichments/fetch?enrichmentType=SAMPLE", groups("sampleId"), &bySample)
	post(t, "/api/expression-enrichments/fetch?enrichmentType=PATIENT", groups("patientId"), &byPatient)
	if len(bySample) == 0 {
		t.Fatal("no sample-level enrichments")
	}
	wantEq(t, len(byPatient), len(bySample))
}

// ------------------------------------------------------ events/survival ----

var lggPatients = []map[string]string{
	{"studyId": "lgg_ucsf_2014_test_generic_assay", "patientId": "P01"},
	{"studyId": "lgg_ucsf_2014_test_generic_assay", "patientId": "P02"},
}

func TestContractClinicalEventsMetaRequests(t *testing.T) {
	requireAPI(t)
	var all, treatment []map[string]any
	post(t, "/api/clinical-events-meta/fetch", map[string]any{"patientIdentifiers": lggPatients}, &all)
	post(t, "/api/clinical-events-meta/fetch", map[string]any{
		"patientIdentifiers":    lggPatients[:1],
		"clinicalEventRequests": []map[string]any{{"eventType": "TREATMENT", "attributes": []map[string]string{{"key": "AGENT", "value": ""}}}},
	}, &treatment)
	if len(all) < 2 {
		t.Fatalf("expected several event types: %v", all)
	}
	if len(treatment) != 1 || treatment[0]["eventType"] != "treatment" {
		t.Fatalf("treatment meta: %v", treatment)
	}
	for _, a := range treatment[0]["attributes"].([]any) {
		wantEq(t, a.(map[string]any)["key"], "agent")
	}
	if _, has := treatment[0]["studyId"]; has {
		t.Errorf("events meta carries only eventType + attributes: %v", treatment[0])
	}
}

func TestContractSurvivalFromEvents(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/survival-data/fetch", map[string]any{
		"attributeIdPrefix":              "CUSTOM",
		"patientIdentifiers":             lggPatients,
		"startEventRequestIdentifier":    map[string]any{"clinicalEventRequests": []map[string]any{{"eventType": "SPECIMEN"}}, "position": "FIRST"},
		"endEventRequestIdentifier":      map[string]any{"clinicalEventRequests": []map[string]any{{"eventType": "STATUS"}}, "position": "FIRST"},
		"censoredEventRequestIdentifier": map[string]any{"clinicalEventRequests": []map[string]any{}, "position": "LAST"},
	}, &d)
	if len(d) != 4 {
		t.Fatalf("want MONTHS+STATUS for 2 patients, got %v", d)
	}
	for _, c := range d {
		id := c["clinicalAttributeId"].(string)
		if !strings.HasPrefix(id, "CUSTOM_") {
			t.Errorf("attribute %s", id)
		}
		if id == "CUSTOM_STATUS" && c["value"] != "1:EVENT" && c["value"] != "0:CENSORED" {
			t.Errorf("status %v", c["value"])
		}
	}
	wantEq(t, d[0]["value"], "15.0")
}

// ------------------------------------------------------------ methods ----

func TestContractCacheAndTokenMethods(t *testing.T) {
	requireAPI(t)
	var tokens []any
	get(t, "/api/data-access-tokens", &tokens)
	var tok map[string]any
	post(t, "/api/data-access-tokens", nil, &tok)
	for _, path := range []string{"/api/cache", "/api/cache/study_es_0", "/api/data-access-tokens"} {
		req, _ := http.NewRequest(http.MethodDelete, apiBase()+path, nil)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("DELETE %s: %v", path, err)
		}
		resp.Body.Close()
		wantEq(t, resp.StatusCode, http.StatusOK)
	}
}
