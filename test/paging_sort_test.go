package contract

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
)

// sortedList is a list endpoint with the sortBy values to check.
type sortedList struct {
	method, path, body string
	sortBy             []string
}

// sortedLists are the paged endpoints that declare sortBy, with fixture
// data to sort.
var sortedLists = []sortedList{
	{"GET", "/api/samples?keyword=TCGA-A1", "", []string{"sampleId", "sampleType"}},
	{"GET", "/api/studies/study_es_0/samples", "", []string{"sampleId", "sampleType"}},
	{"GET", "/api/studies/ascn_test_study/patients/FAKE_P001/samples", "", []string{"sampleId", "sampleType"}},
	{"GET", "/api/studies/ascn_test_study/samples/FAKE_P001_S5/copy-number-segments", "",
		[]string{"chromosome", "start", "end", "numberOfProbes", "segmentMean"}},
	{"GET", "/api/studies/lgg_ucsf_2014_test_generic_assay/samples/P01_Pri/clinical-data", "", []string{"clinicalAttributeId", "value"}},
	{"GET", "/api/studies/study_es_0/patients/TCGA-A1-A0SB/clinical-data", "", []string{"clinicalAttributeId", "value"}},
	{"GET", "/api/studies/study_es_0/clinical-data?clinicalDataType=PATIENT", "", []string{"clinicalAttributeId", "value"}},
	{"GET", "/api/studies/study_es_0/sample-lists", "", []string{"sampleListId", "category", "studyId", "name", "description"}},
	{"GET", "/api/sample-lists", "", []string{"sampleListId", "category", "studyId", "name", "description"}},
	{"GET", "/api/studies/study_es_0/patients", "", []string{"patientId"}},
	{"GET", "/api/patients", "", []string{"patientId"}},
	{"GET", "/api/patients?keyword=TCGA-A1", "", []string{"patientId"}},
	{"GET", "/api/studies/study_es_0/molecular-profiles", "", []string{"molecularProfileId", "molecularAlterationType",
		"datatype", "name", "description", "showProfileInAnalysisTab"}},
	{"GET", "/api/molecular-profiles", "", []string{"molecularProfileId", "molecularAlterationType", "datatype", "name"}},
	{"GET", "/api/molecular-profiles/study_es_0_mutations/mutations?sampleListId=study_es_0_all&entrezGeneId=7157", "",
		[]string{"entrezGeneId", "startPosition", "proteinChange", "mutationType", "tumorAltCount", "keyword", "proteinPosStart"}},
	{"POST", "/api/molecular-profiles/study_es_0_mutations/mutations/fetch", `{"sampleListId":"study_es_0_all"}`,
		[]string{"entrezGeneId", "startPosition", "proteinChange", "variantType", "keyword", "proteinPosEnd"}},
	{"POST", "/api/mutations/fetch", `{"molecularProfileIds":["study_es_0_mutations"],"entrezGeneIds":[7157,672,675]}`,
		[]string{"entrezGeneId", "startPosition", "proteinChange", "mutationType", "keyword"}},
	{"GET", "/api/gene-panels", "", []string{"genePanelId", "description"}},
	{"GET", "/api/clinical-attributes", "", []string{"clinicalAttributeId", "displayName", "datatype", "patientAttribute", "priority", "studyId"}},
	{"GET", "/api/studies/study_es_0/clinical-attributes", "", []string{"clinicalAttributeId", "displayName", "description",
		"datatype", "patientAttribute", "priority"}},
	{"GET", "/api/cancer-types", "", []string{"cancerTypeId", "name", "dedicatedColor", "shortName", "parent"}},
	{"GET", "/api/studies/study_es_0/significant-copy-number-regions", "", []string{"chromosome", "cytoband", "widePeakStart",
		"widePeakEnd", "qValue", "amp"}},
	// The fixture study has one resource per level: resource-data-all
	// sorts the rows of every level as the per-level endpoints do.
	{"GET", "/api/studies/study_es_0/resource-data-all", "", []string{"ResourceId", "url"}},
	{"GET", "/api/studies/study_es_0/resource-definitions", "", []string{"resourceId", "displayName", "description",
		"resourceType", "priority", "openByDefault", "studyId"}},
	{"GET", "/api/studies/lgg_ucsf_2014_test_generic_assay/patients/P01/clinical-events", "",
		[]string{"eventType", "startNumberOfDaysSinceDiagnosis", "endNumberOfDaysSinceDiagnosis"}},
	{"GET", "/api/studies/lgg_ucsf_2014_test_generic_assay/clinical-events", "",
		[]string{"eventType", "startNumberOfDaysSinceDiagnosis", "endNumberOfDaysSinceDiagnosis"}},
	{"GET", "/api/genes?pageSize=40&pageNumber=2", "", []string{"entrezGeneId", "hugoGeneSymbol", "cytoband", "length"}},
}

// listOf requests a list and decodes it.
func listOf(t *testing.T, method, path, body string) []map[string]any {
	t.Helper()
	resp, b := rawDo(t, method, path, body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s %s -> %d: %s", method, path, resp.StatusCode, b)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return out
}

func withQuery(path string, kv ...string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return path + sep + v.Encode()
}

// projectOnto is the item reduced to the given keys, as a comparable string.
func projectOnto(item map[string]any, keys []string) string {
	sub := map[string]any{}
	for _, k := range keys {
		sub[k] = item[k]
	}
	b, _ := json.Marshal(sub)
	return string(b)
}

// TestSortedIDProjection pins that projection=ID honours sortBy and
// direction: its items come in the order of the SUMMARY items sorted the
// same way. Items whose sortBy value ties may come in any order among
// themselves.
func TestSortedIDProjection(t *testing.T) {
	requireAPI(t)
	for _, l := range sortedLists {
		for _, sortBy := range l.sortBy {
			for _, dir := range []string{"ASC", "DESC"} {
				summary := listOf(t, l.method, withQuery(l.path, "sortBy", sortBy, "direction", dir), l.body)
				ids := listOf(t, l.method, withQuery(l.path, "sortBy", sortBy, "direction", dir, "projection", "ID"), l.body)
				what := fmt.Sprintf("%s %s sortBy=%s %s", l.method, l.path, sortBy, dir)
				if len(summary) < 2 {
					t.Errorf("%s: %d items, too few to check an order", what, len(summary))
					continue
				}
				if len(ids) != len(summary) {
					t.Errorf("%s: %d ID items, %d SUMMARY items", what, len(ids), len(summary))
					continue
				}
				var keys []string
				for k := range ids[0] {
					keys = append(keys, k)
				}
				// The tie group of each item: consecutive SUMMARY items
				// with the same sortBy value share one.
				group := map[string][]int{}
				g := 0
				for i, it := range summary {
					v, has := it[sortBy]
					if i > 0 && (!has || fmt.Sprint(v) != fmt.Sprint(summary[i-1][sortBy])) {
						g++
					}
					k := projectOnto(it, keys)
					group[k] = append(group[k], g)
				}
				last := -1
				for i, it := range ids {
					gs := group[projectOnto(it, keys)]
					if len(gs) == 0 {
						t.Errorf("%s: ID item %d %v is not among the SUMMARY items", what, i, it)
						break
					}
					// The earliest group not before the previous item's.
					at := -1
					for _, x := range gs {
						if x >= last && (at < 0 || x < at) {
							at = x
						}
					}
					if at < 0 {
						t.Errorf("%s: ID item %d %v is out of order", what, i, it)
						break
					}
					last = at
				}
			}
		}
	}
}

// fieldOf lists a field of every item, as text.
func fieldOf(items []map[string]any, field string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = fmt.Sprint(it[field])
	}
	return out
}

// TestPatientKeywordSortBy pins that a patient keyword search honours
// sortBy and direction (PatientMapper ignores them).
func TestPatientKeywordSortBy(t *testing.T) {
	requireAPI(t)
	plain := listOf(t, "GET", "/api/patients?keyword=P0", "")
	desc := listOf(t, "GET", "/api/patients?keyword=P0&sortBy=patientId&direction=DESC", "")
	if len(plain) < 4 || len(desc) != len(plain) {
		t.Fatalf("keyword P0: %d patients, %d sorted", len(plain), len(desc))
	}
	ids := fieldOf(desc, "patientId")
	if !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] > ids[j] }) {
		t.Errorf("not in descending patientId order: %v", ids)
	}
	page := listOf(t, "GET", "/api/patients?keyword=P0&sortBy=patientId&direction=DESC&pageSize=2&pageNumber=1", "")
	wantEq(t, fieldOf(page, "patientId"), ids[2:4])
}

// TestGeneKeywordSortBy pins that a gene keyword search honours sortBy and
// direction: the page GeneServiceImpl completes (aliases, no symbols with
// several Entrez ids) comes in sortBy order instead of by symbol.
func TestGeneKeywordSortBy(t *testing.T) {
	requireAPI(t)
	asc := listOf(t, "GET", "/api/genes?keyword=TP&pageSize=1000", "")
	desc := listOf(t, "GET", "/api/genes?keyword=TP&pageSize=1000&sortBy=hugoGeneSymbol&direction=DESC", "")
	symbols := fieldOf(asc, "hugoGeneSymbol")
	slices.Reverse(symbols)
	wantEq(t, fieldOf(desc, "hugoGeneSymbol"), symbols)

	byID := listOf(t, "GET", "/api/genes?keyword=TP&pageSize=1000&sortBy=entrezGeneId&direction=DESC", "")
	var ids []float64
	for _, g := range byID {
		ids = append(ids, g["entrezGeneId"].(float64))
	}
	if len(ids) != len(asc) || !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] > ids[j] }) {
		t.Errorf("not %d genes in descending Entrez order: %v", len(asc), ids)
	}
}

// TestGeneSortByLocation pins GET /api/genes?sortBy=cytoband|length (Java:
// a 500): genes sorted by their hg19 cytoband or length (end - start), also
// with a keyword.
func TestGeneSortByLocation(t *testing.T) {
	requireAPI(t)
	type loc struct {
		cytoband string
		length   float64
	}
	locs := map[float64]loc{}
	var cytobands []string
	var lengths []float64
	for _, g := range listOf(t, "GET", "/api/reference-genome-genes/hg19", "") {
		l := loc{g["cytoband"].(string), g["end"].(float64) - g["start"].(float64)}
		locs[g["entrezGeneId"].(float64)] = l
		cytobands = append(cytobands, l.cytoband)
		lengths = append(lengths, l.length)
	}
	sort.Strings(cytobands)
	sort.Sort(sort.Reverse(sort.Float64Slice(lengths)))
	if len(locs) < 1000 {
		t.Fatalf("hg19: %d genes", len(locs))
	}
	page := func(query string) []loc {
		var out []loc
		for _, g := range listOf(t, "GET", "/api/genes?"+query, "") {
			l, ok := locs[g["entrezGeneId"].(float64)]
			if !ok {
				t.Fatalf("%s: gene %v has no hg19 location", query, g)
			}
			out = append(out, l)
		}
		return out
	}
	var gotBands []string
	for _, l := range page("sortBy=cytoband&pageSize=20&pageNumber=3") {
		gotBands = append(gotBands, l.cytoband)
	}
	wantEq(t, gotBands, cytobands[60:80])
	var gotLengths []float64
	for _, l := range page("sortBy=length&direction=DESC&pageSize=20") {
		gotLengths = append(gotLengths, l.length)
	}
	wantEq(t, gotLengths, lengths[:20])

	kw := page("keyword=BRCA&sortBy=length&direction=DESC")
	if len(kw) < 2 || !sort.SliceIsSorted(kw, func(i, j int) bool { return kw[i].length > kw[j].length }) {
		t.Errorf("keyword BRCA by length: %v", kw)
	}
}

// TestLegacyStudiesPaging pins /api/legacy/studies' 0-based page numbers
// (the legacy StudyController's), where /api/studies counts from 1, and that
// it sorts a keyword search by sortBy (StudyMapper ignores it).
func TestLegacyStudiesPaging(t *testing.T) {
	requireAPI(t)
	all := fieldOf(listOf(t, "GET", "/api/studies?sortBy=studyId", ""), "studyId")
	if len(all) < 3 {
		t.Fatalf("%d studies", len(all))
	}
	wantEq(t, fieldOf(listOf(t, "GET", "/api/legacy/studies?sortBy=studyId&pageSize=1&pageNumber=0", ""), "studyId"), all[:1])
	wantEq(t, fieldOf(listOf(t, "GET", "/api/legacy/studies?sortBy=studyId&pageSize=1&pageNumber=1", ""), "studyId"), all[1:2])
	wantEq(t, fieldOf(listOf(t, "GET", "/api/studies?sortBy=studyId&pageSize=1&pageNumber=1", ""), "studyId"), all[:1])

	names := fieldOf(listOf(t, "GET", "/api/legacy/studies?keyword=study&sortBy=name&direction=DESC", ""), "name")
	if len(names) < 2 || !sort.SliceIsSorted(names, func(i, j int) bool { return names[i] > names[j] }) {
		t.Errorf("legacy keyword search not by name DESC: %v", names)
	}
}

// TestGenericAssayMetaPaging pins ColumnStoreGenericAssayController's
// paging: pageSize and pageNumber only together, and a page carries the
// total-count of every match.
func TestGenericAssayMetaPaging(t *testing.T) {
	requireAPI(t)
	body := `{"molecularProfileIds":["study_es_0_treatment_ic50"]}`
	all := listOf(t, "POST", "/api/generic-assay-meta/fetch", body)
	if len(all) < 4 {
		t.Fatalf("%d entities", len(all))
	}
	resp, b := rawDo(t, "POST", "/api/generic-assay-meta/fetch?pageSize=2&pageNumber=1", body)
	wantEq(t, resp.Header.Get("total-count"), itoa(len(all)))
	var page []map[string]any
	json.Unmarshal(b, &page)
	wantEq(t, fieldOf(page, "stableId"), fieldOf(all, "stableId")[2:4])
	resp, _ = rawDo(t, "POST", "/api/generic-assay-meta/fetch", body)
	wantEq(t, resp.Header.Get("total-count"), "")
	for _, q := range []string{"pageSize=2", "pageNumber=1"} {
		resp, _ := rawDo(t, "POST", "/api/generic-assay-meta/fetch?"+q, body)
		wantEq(t, resp.StatusCode, 400)
	}
}
