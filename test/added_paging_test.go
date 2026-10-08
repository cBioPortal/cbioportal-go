package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
