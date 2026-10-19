package contract

import (
	"encoding/base64"
	"io"
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
	get(t, "/api/studies/study_es_0/samples", &all)
	get(t, "/api/studies/study_es_0/samples?pageSize=2&pageNumber=1", &page)
	wantEq(t, ids(page, "sampleId"), ids(all, "sampleId")[2:4])

	// ColumnStoreStudyController numbers pages from 1; 0 is the first page too.
	var studies, limited, first, second []map[string]any
	get(t, "/api/studies?projection=SUMMARY", &studies)
	get(t, "/api/studies?projection=SUMMARY&pageSize=2&pageNumber=1", &limited)
	wantEq(t, ids(limited, "studyId"), ids(studies, "studyId")[:2])
	get(t, "/api/studies?projection=SUMMARY&pageSize=2&pageNumber=0", &first)
	wantEq(t, ids(first, "studyId"), ids(studies, "studyId")[:2])
	get(t, "/api/studies?projection=SUMMARY&pageSize=1&pageNumber=2", &second)
	wantEq(t, ids(second, "studyId"), ids(studies, "studyId")[1:2])

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

// ------------------------------------------------------ filter forms ----

func TestContractSamplesFetchByUniqueSampleKeys(t *testing.T) {
	requireAPI(t)
	key := base64.RawStdEncoding.EncodeToString([]byte("TCGA-A1-A0SB-01:study_es_0"))
	var d []map[string]any
	post(t, "/api/samples/fetch", map[string]any{"uniqueSampleKeys": []string{key}}, &d)
	wantEq(t, ids(d, "sampleId"), []string{"TCGA-A1-A0SB-01"})
}

func TestContractMutationsBySampleList(t *testing.T) {
	requireAPI(t)
	var get1, all, cnaseq []map[string]any
	get(t, "/api/molecular-profiles/study_es_0_mutations/mutations?sampleListId=study_es_0_all&entrezGeneId=672&projection=DETAILED", &get1)
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

// A sample list of another study selects none of a mutation profile's rows,
// even where its sample ids are the profile study's too: Java joins the list
// by sample internal id. Molecular data reads the list's sample ids instead
// (MolecularDataServiceImpl), so there the same ids do match.
// TCGA-A2-A04P-01 is in study_hg38_all and in study_es_0.
func TestContractSampleListOfAnotherStudy(t *testing.T) {
	requireAPI(t)
	var own, other, fetched, values []map[string]any
	get(t, "/api/molecular-profiles/study_es_0_mutations/mutations?sampleListId=study_es_0_all&entrezGeneId=675", &own)
	if len(own) == 0 {
		t.Fatal("no BRCA2 mutations in study_es_0_all")
	}
	get(t, "/api/molecular-profiles/study_es_0_mutations/mutations?sampleListId=study_hg38_all&entrezGeneId=675", &other)
	wantEq(t, len(other), 0)
	post(t, "/api/molecular-profiles/study_es_0_mutations/mutations/fetch",
		map[string]any{"sampleListId": "study_hg38_all"}, &fetched)
	wantEq(t, len(fetched), 0)
	get(t, "/api/molecular-profiles/study_es_0_mrna/molecular-data?sampleListId=study_hg38_all&entrezGeneId=4313", &values)
	wantEq(t, ids(values, "sampleId"), []string{"TCGA-A2-A04P-01"})
}

func TestContractCopyNumberSegmentsBareList(t *testing.T) {
	requireAPI(t)
	body := []map[string]string{{"studyId": "study_es_0", "sampleId": "TCGA-A2-A04P-01"}}
	var all, chr1, chr2 []map[string]any
	post(t, "/api/copy-number-segments/fetch", body, &all)
	post(t, "/api/copy-number-segments/fetch?chromosome=1", body, &chr1)
	post(t, "/api/copy-number-segments/fetch?chromosome=2", body, &chr2)
	if len(all) == 0 || len(chr1) != len(all) || len(chr2) != 0 {
		t.Fatalf("segments: all %d, chr1 %d, chr2 %d", len(all), len(chr1), len(chr2))
	}
	var one []map[string]any
	get(t, "/api/studies/study_es_0/samples/TCGA-A2-A04P-01/copy-number-segments?chromosome=1", &one)
	wantEq(t, len(one), len(chr1))
	for _, s := range one {
		wantEq(t, s["chromosome"], "1")
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
	// A present but empty sampleMolecularIdentifiers wins over the profiles
	// and selects nothing, as in Java's controller.
	var none []map[string]any
	post(t, "/api/structural-variant/fetch", map[string]any{"molecularProfileIds": profiles, "sampleMolecularIdentifiers": []any{}}, &none)
	wantEq(t, len(none), 0)
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

// The geneset hierarchy takes its samples from sampleListId or a bare
// sample-id body; with the gene set reference loaded, a profile that is not
// GSVA-SCORE is Java's unhandled IllegalArgumentException (500).
func TestContractGenesetHierarchyBody(t *testing.T) {
	requireAPI(t)
	var bySampleList, byStudy, bySamples []map[string]any
	post(t, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_gsva_scores&sampleListId=study_es_0_all", nil, &bySampleList)
	post(t, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_gsva_scores", nil, &byStudy)
	post(t, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_gsva_scores", []string{"TCGA-A1-A0SB-01"}, &bySamples)
	if len(bySampleList) == 0 || len(bySampleList) != len(byStudy) {
		t.Fatalf("hierarchy by sample list %v, by study %v", bySampleList, byStudy)
	}
	if bySamples == nil {
		t.Fatalf("hierarchy by bare sample-id list: null")
	}
	var v string
	get(t, "/api/genesets/version", &v)
	if v == "msigdb_7.5.1" {
		resp, _ := rawDo(t, http.MethodPost, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_mrna", "[]")
		wantEq(t, resp.StatusCode, http.StatusInternalServerError)
	}
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
	path := "/api/clinical-data-density-plot/fetch?xAxisAttributeId=MUTATION_COUNT&yAxisAttributeId=MUTATION_COUNT&xAxisBinCount=5&yAxisBinCount=5"
	body := map[string]any{"studyIds": []string{"lgg_ucsf_2014_test_generic_assay"}}
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
		t.Fatalf("study_es_0 has %d samples, want at least 60", len(samples))
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
	resp, _ := rawDo(t, http.MethodDelete, "/api/data-access-tokens", "")
	wantEq(t, resp.StatusCode, http.StatusOK)
}

// CacheController: the X-API-KEY header is required (Spring's 400 without
// it), and with cache.endpoint.enabled unset the endpoints answer 404 with a
// plain-text reason. There is no GET.
func TestContractCacheEndpointDisabled(t *testing.T) {
	requireAPI(t)
	for _, path := range []string{"/api/cache", "/api/cache/study_es_0"} {
		resp, _ := rawDo(t, http.MethodDelete, path, "")
		wantEq(t, resp.StatusCode, http.StatusBadRequest)

		req, _ := http.NewRequest(http.MethodDelete, apiBase()+path, nil)
		req.Header.Set("X-API-KEY", "anything")
		r2, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("DELETE %s: %v", path, err)
		}
		body, _ := io.ReadAll(r2.Body)
		r2.Body.Close()
		wantEq(t, r2.StatusCode, http.StatusNotFound)
		wantEq(t, string(body), "Cache endpoint is disabled for this instance.")

		resp, _ = rawDo(t, http.MethodGet, path, "")
		wantEq(t, resp.StatusCode, http.StatusMethodNotAllowed)
		wantEq(t, resp.Header.Get("Allow"), "DELETE")
	}
}
