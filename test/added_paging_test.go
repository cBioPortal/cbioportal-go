package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// addedList is an endpoint that go pages and sorts although Java declares
// no paging parameters, with fixture data and the sortBy values to check.
// numeric names fields that are JSON strings holding numbers.
type addedList struct {
	method, path, body string
	sortBy             []string
	numeric            []string
}

var es0Samples = `[{"sampleId":"FAKE_P001_S2","studyId":"ascn_test_study"},{"sampleId":"FAKE_P001_S3","studyId":"ascn_test_study"},` +
	`{"sampleId":"FAKE_P001_S5","studyId":"ascn_test_study"}]`

// addedLists cover every endpoint group of the added paging.
var addedLists = []addedList{
	// Clinical data, samples, patients.
	{"POST", "/api/clinical-data/fetch?clinicalDataType=SAMPLE",
		`{"identifiers":[{"entityId":"P01_Pri","studyId":"lgg_ucsf_2014_test_generic_assay"},` +
			`{"entityId":"P02_Pri","studyId":"lgg_ucsf_2014_test_generic_assay"},{"entityId":"TCGA-A1-A0SB-01","studyId":"study_es_0"}]}`,
		[]string{"clinicalAttributeId", "value"}, nil},
	{"POST", "/api/clinical-data/fetch?clinicalDataType=PATIENT&projection=ID",
		`{"identifiers":[{"entityId":"P01","studyId":"lgg_ucsf_2014_test_generic_assay"},{"entityId":"TCGA-A1-A0SB","studyId":"study_es_0"}]}`,
		[]string{"clinicalAttributeId"}, nil},
	{"POST", "/api/studies/lgg_ucsf_2014_test_generic_assay/clinical-data/fetch?clinicalDataType=SAMPLE",
		`{"attributeIds":["CANCER_TYPE","MUTATION_COUNT"]}`, []string{"clinicalAttributeId", "value"}, nil},
	{"POST", "/api/samples/fetch", `{"sampleListIds":["study_es_0_all"]}`, []string{"sampleId", "sampleType"}, nil},
	{"POST", "/api/patients/fetch", `{"patientIdentifiers":[{"patientId":"TCGA-A2-A04P","studyId":"study_es_0"},` +
		`{"patientId":"TCGA-A1-A0SK","studyId":"study_es_0"},{"patientId":"TCGA-A2-A0CM","studyId":"study_es_0"},` +
		`{"patientId":"P01","studyId":"lgg_ucsf_2014_test_generic_assay"}]}`, []string{"patientId"}, nil},
	{"POST", "/api/filtered-samples/fetch", `{"studyIds":["study_es_0","lgg_ucsf_2014_test_generic_assay"]}`,
		[]string{"sampleId", "patientId", "studyId"}, nil},
	{"GET", "/api/sample-lists/study_es_0_all/sample-ids", "", nil, nil},
	{"POST", "/api/clinical-attributes/fetch", `["study_es_0","teststudy_genepanels"]`,
		[]string{"clinicalAttributeId", "displayName", "datatype", "patientAttribute", "studyId"}, nil},

	// Molecular data.
	{"POST", "/api/molecular-profiles/study_es_0_gistic/molecular-data/fetch",
		`{"entrezGeneIds":[7157,672,675,2064,3845,4609,238,673,2068,116983,5728,1956],"sampleListId":"study_es_0_all"}`,
		[]string{"entrezGeneId", "sampleId", "patientId", "value"}, nil},
	{"GET", "/api/molecular-profiles/study_es_0_gistic/molecular-data?sampleListId=study_es_0_all&entrezGeneId=116983", "",
		[]string{"sampleId", "value"}, nil},
	{"POST", "/api/molecular-data/fetch",
		`{"entrezGeneIds":[116983,90993,4313,1956,2064],"molecularProfileIds":["study_es_0_gistic","study_es_0_mrna"]}`,
		[]string{"entrezGeneId", "sampleId", "molecularProfileId", "value"}, nil},
	// ALL reads the profile's values, the default event types the CNA events.
	{"POST", "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch?discreteCopyNumberEventType=ALL",
		`{"entrezGeneIds":[7157,672,675,2064,3845,4609,238,673,2068,116983,5728,1956],"sampleListId":"study_es_0_all"}`,
		[]string{"entrezGeneId", "sampleId", "alteration"}, nil},
	{"GET", "/api/molecular-profiles/ascn_test_study_cna/discrete-copy-number?sampleListId=ascn_test_study_all", "",
		[]string{"entrezGeneId", "sampleId", "patientId", "alteration"}, nil},
	{"POST", "/api/generic_assay_data/fetch",
		`{"molecularProfileIds":["lgg_ucsf_2014_test_generic_assay_mutational_signature_contribution_SBS"]}`,
		[]string{"stableId", "sampleId", "patientId", "value"}, []string{"value"}},
	// HLA alleles are categories: value sorts them as text.
	{"POST", "/api/generic_assay_data/lgg_ucsf_2014_test_generic_assay_hla/fetch",
		`{"genericAssayStableIds":["HLA-A"],"sampleListId":"lgg_ucsf_2014_test_generic_assay_all"}`,
		[]string{"sampleId", "value"}, nil},
	{"GET", "/api/generic-assay-data/lgg_ucsf_2014_test_generic_assay_hla/generic-assay/HLA-A", "",
		[]string{"sampleId", "patientId"}, nil},
	{"POST", "/api/gene-panel-data/fetch", `{"molecularProfileIds":["study_es_0_mutations","study_es_0_gistic"]}`,
		[]string{"molecularProfileId", "sampleId", "genePanelId", "profiled"}, nil},
	{"POST", "/api/molecular-profiles/study_es_0_mutations/gene-panel-data/fetch", `{"sampleListId":"study_es_0_all"}`,
		[]string{"sampleId", "genePanelId"}, nil},
	{"POST", "/api/copy-number-segments/fetch", es0Samples,
		[]string{"chromosome", "start", "end", "numberOfProbes", "segmentMean"}, nil},
	{"POST", "/api/structural-variant/fetch", `{"molecularProfileIds":["study_es_0_structural_variants"]}`,
		[]string{"sampleId", "site1HugoSymbol", "site2EntrezGeneId", "site1Position", "variantClass", "tumorReadCount"}, nil},
	{"POST", "/api/genetic-profiles/study_es_0_gsva_scores/geneset-genetic-data/fetch",
		`{"genesetIds":["GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS","GO_ACYLGLYCEROL_HOMEOSTASIS"],"sampleListId":"study_es_0_all"}`,
		[]string{"genesetId", "sampleId", "value"}, []string{"value"}},

	// Genes, gene sets, generic assay entities.
	{"POST", "/api/genes/fetch?geneIdType=HUGO_GENE_SYMBOL", `["TP53","BRCA1","ERCC5","KRAS","EGFR","PTEN"]`,
		[]string{"entrezGeneId", "hugoGeneSymbol"}, nil},
	{"GET", "/api/generic-assay-meta/lgg_ucsf_2014_test_generic_assay_mutational_signature_counts_SBS", "",
		[]string{"stableId"}, nil},
	{"POST", "/api/reference-genome-genes/hg19/fetch", `["7157","672","675","3845","1956","5728"]`,
		[]string{"entrezGeneId", "hugoGeneSymbol", "chromosome", "start"}, nil},

	// Study view and group comparison lists.
	// treatments/sample lists each row's samples in no set order, so its
	// pages are checked by the rows' own fields only (TestAddedPagingRows).
	{"POST", "/api/treatments/patient?tier=Agent", `{"studyIds":["lgg_ucsf_2014_test_generic_assay"]}`,
		[]string{"treatment", "count"}, nil},
	{"POST", "/api/molecular-profiles/co-expressions/fetch?molecularProfileIdA=study_es_0_mrna&molecularProfileIdB=study_es_0_mrna&threshold=0",
		`{"entrezGeneId":90993,"sampleListId":"study_es_0_all"}`, []string{"geneticEntityId", "spearmansCorrelation", "pValue"},
		[]string{"geneticEntityId"}},
	{"POST", "/api/alteration-enrichments/fetch", `{"molecularProfileCasesGroupFilter":[` +
		`{"name":"a","molecularProfileCaseIdentifiers":[{"caseId":"TCGA-A2-A04P-01","molecularProfileId":"study_es_0_mutations"},` +
		`{"caseId":"TCGA-A1-A0SB-01","molecularProfileId":"study_es_0_mutations"}]},` +
		`{"name":"b","molecularProfileCaseIdentifiers":[{"caseId":"TCGA-A1-A0SK-01","molecularProfileId":"study_es_0_mutations"}]}]}`,
		[]string{"hugoGeneSymbol", "pValue"}, nil},
}

// rawList requests a list and returns its items as raw JSON.
func rawList(t *testing.T, method, path, body string) []json.RawMessage {
	t.Helper()
	resp, b := rawDo(t, method, path, body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s %s -> %d: %s", method, path, resp.StatusCode, b)
	}
	var out []json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return out
}

// sameItems reports whether a and b hold the same items in the same order.
func sameItems(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// sortKey is an item's sortBy value as the API compares it: numbers
// (numeric fields' strings too) as numbers, other values as text; ok is
// false for an absent or null value, or a numeric field's non-number.
func sortKey(item json.RawMessage, field string, numeric bool) (num float64, text string, isNum, ok bool) {
	var m map[string]any
	json.Unmarshal(item, &m)
	switch v := m[field].(type) {
	case nil:
		return 0, "", false, false
	case float64:
		return v, "", true, true
	case string:
		if numeric {
			f, err := strconv.ParseFloat(v, 64)
			return f, "", true, err == nil
		}
		return 0, v, false, true
	default:
		return 0, fmt.Sprint(v), false, true
	}
}

// TestAddedPaging pins the paging go adds to list endpoints whose Java
// controllers declare none: a whole-list page is the unpaged list, pages
// 0 and 1 are the first two pages of the list in the same order (sorted or
// not), a sort orders by the field in either direction with missing values
// last, and every page is cut from the same order.
func TestAddedPaging(t *testing.T) {
	requireAPI(t)
	for _, l := range addedLists {
		what := l.method + " " + l.path
		all := rawList(t, l.method, l.path, l.body)
		if len(all) < 2 {
			t.Errorf("%s: %d items, too few to page", what, len(all))
			continue
		}
		if whole := rawList(t, l.method, withQuery(l.path, "pageSize", "10000000", "pageNumber", "0"), l.body); !sameItems(whole, all) {
			t.Errorf("%s: the whole-list page differs from the unpaged list", what)
		}
		n := max(1, len(all)/5)
		pages := func(kv ...string) []json.RawMessage {
			p0 := rawList(t, l.method, withQuery(l.path, append(kv, "pageSize", itoa(n), "pageNumber", "0")...), l.body)
			p1 := rawList(t, l.method, withQuery(l.path, append(kv, "pageSize", itoa(n), "pageNumber", "1")...), l.body)
			return append(p0, p1...)
		}
		if !sameItems(pages(), all[:min(2*n, len(all))]) {
			t.Errorf("%s: pages 0 and 1 are not the first %d items", what, 2*n)
		}
		for _, f := range l.sortBy {
			numeric := false
			for _, x := range l.numeric {
				numeric = numeric || x == f
			}
			for _, dir := range []string{"ASC", "DESC"} {
				w := fmt.Sprintf("%s sortBy=%s %s", what, f, dir)
				sorted := rawList(t, l.method, withQuery(l.path, "sortBy", f, "direction", dir), l.body)
				a, b := sortedStrings(all), sortedStrings(sorted)
				if fmt.Sprint(a) != fmt.Sprint(b) {
					t.Errorf("%s: not the unpaged items", w)
					continue
				}
				missing := false
				for i, it := range sorted {
					num, text, isNum, ok := sortKey(it, f, numeric)
					if !ok {
						missing = true
						continue
					}
					if missing {
						t.Errorf("%s: item %d has a value after a missing one", w, i)
						break
					}
					if i == 0 {
						continue
					}
					pnum, ptext, pIsNum, pok := sortKey(sorted[i-1], f, numeric)
					if !pok {
						continue
					}
					c := 0
					switch {
					case isNum && pIsNum:
						c = cmpFloat(pnum, num)
					case !isNum && !pIsNum:
						c = cmpString(ptext, text)
					}
					if (dir == "ASC" && c > 0) || (dir == "DESC" && c < 0) {
						t.Errorf("%s: items %d and %d out of order: %s, %s", w, i-1, i, sorted[i-1], it)
						break
					}
				}
				if !sameItems(pages("sortBy", f, "direction", dir), sorted[:min(2*n, len(sorted))]) {
					t.Errorf("%s: pages 0 and 1 are not the first %d sorted items", w, 2*n)
				}
			}
		}
	}
}

func sortedStrings(items []json.RawMessage) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = string(it)
	}
	sort.Strings(out)
	return out
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// TestAddedPagingParams pins the Java-style 400s of the added paging
// parameters, and that endpoints without sortBy leave it unchecked.
func TestAddedPagingParams(t *testing.T) {
	requireAPI(t)
	const mismatch = "Request parameter type mismatch: "
	for _, c := range []struct{ method, path, body, query, msg string }{
		{"POST", "/api/clinical-data/fetch", `{"identifiers":[{"entityId":"P01","studyId":"lgg_ucsf_2014_test_generic_assay"}]}`,
			"sortBy=bogus", mismatch + "java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.web.parameter.sort.ClinicalDataSortBy.bogus"},
		{"POST", "/api/molecular-data/fetch", `{"entrezGeneIds":[116983],"molecularProfileIds":["study_es_0_gistic"]}`,
			"sortBy=hugoGeneSymbol", mismatch + "java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.web.parameter.sort.MolecularDataSortBy.hugoGeneSymbol"},
		{"POST", "/api/samples/fetch", `{"sampleListIds":["study_es_0_all"]}`,
			"direction=UP", mismatch + "java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.web.parameter.Direction.UP"},
		{"POST", "/api/structural-variant/fetch", `{"molecularProfileIds":["study_es_0_structural_variants"]}`,
			"pageSize=0", "pageSize must be greater than or equal to 1"},
		{"POST", "/api/genes/fetch", `["TP53"]`, "pageSize=10000001", "pageSize must be less than or equal to 10000000"},
		{"GET", "/api/reference-genome-genes/hg19", "", "pageNumber=-1", "pageNumber must be greater than or equal to 0"},
		{"GET", "/api/sample-lists/study_es_0_all/sample-ids", "", "pageSize=x", mismatch + `java.lang.NumberFormatException: For input string: "x"`},
	} {
		wantError(t, c.method, c.path+"?"+c.query, c.body, 400, c.msg)
	}
	// sample-ids pages only: its items are ids, with no field to sort by.
	ids := rawList(t, "GET", "/api/sample-lists/study_es_0_all/sample-ids?sortBy=bogus&pageSize=3&pageNumber=1", "")
	all := rawList(t, "GET", "/api/sample-lists/study_es_0_all/sample-ids", "")
	if !sameItems(ids, all[3:6]) {
		t.Errorf("sample-ids page 1: %s", ids)
	}
}

// TestAddedPagingMeta pins that projection=META counts the whole list
// whatever the paging parameters say.
func TestAddedPagingMeta(t *testing.T) {
	requireAPI(t)
	path := "/api/molecular-profiles/study_es_0_gistic/molecular-data/fetch"
	body := `{"entrezGeneIds":[116983],"sampleListId":"study_es_0_all"}`
	all := rawList(t, "POST", path, body)
	resp, _ := rawDo(t, "POST", path+"?projection=META&pageSize=2&pageNumber=1&sortBy=value", body)
	wantEq(t, resp.Header.Get("total-count"), itoa(len(all)))
}

// TestAddedPagingRows pins treatments/sample pages by the rows' treatment
// and time: the samples within a row come in no set order.
func TestAddedPagingRows(t *testing.T) {
	requireAPI(t)
	path, body := "/api/treatments/sample?tier=Agent", `{"studyIds":["lgg_ucsf_2014_test_generic_assay"]}`
	rows := func(query string) []string {
		var out []string
		for _, it := range listOf(t, "POST", path+query, body) {
			out = append(out, fmt.Sprint(it["treatment"], "/", it["time"], "/", it["count"]))
		}
		return out
	}
	all := rows("")
	if len(all) < 4 {
		t.Fatalf("%d rows", len(all))
	}
	wantEq(t, append(rows("&pageSize=2&pageNumber=0"), rows("&pageSize=2&pageNumber=1")...), all[:4])
	desc := rows("&sortBy=treatment&direction=DESC")
	wantEq(t, rows("&sortBy=treatment&direction=DESC&pageSize=2&pageNumber=1"), desc[2:4])
	treatment := func(row string) string { s, _, _ := strings.Cut(row, "/"); return s }
	if !sort.SliceIsSorted(desc, func(i, j int) bool { return treatment(desc[i]) > treatment(desc[j]) }) {
		t.Errorf("not by treatment DESC: %v", desc)
	}
}
