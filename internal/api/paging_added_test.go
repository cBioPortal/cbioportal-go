package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// TestAddedPagingWired pins that every endpoint with added paging is paged
// by pageList and validates its parameters as the Java list endpoints do.
func TestAddedPagingWired(t *testing.T) {
	for _, a := range core.AddedPagedEndpoints {
		if !slices.Contains(core.PagedEndpoints, a.Endpoint()) {
			t.Errorf("%s: not in pagedEndpoints", a.Endpoint())
		}
		rt, _ := core.MatchContract(a.Method, strings.ReplaceAll(core.TemplatePath(a.Path), "{}", "x"))
		if rt == nil {
			t.Errorf("%s: no contract route", a.Endpoint())
			continue
		}
		names := map[string]int{}
		for _, p := range rt.Params {
			names[p.Name]++
		}
		want := []string{"pageSize", "pageNumber"}
		if a.Sorted() {
			want = append(want, "sortBy", "direction")
		}
		for _, n := range want {
			if names[n] != 1 {
				t.Errorf("%s: %d %s params", a.Endpoint(), names[n], n)
			}
		}
		if !a.Sorted() && (names["sortBy"] > 0 || !core.SortIgnored(a.Endpoint())) {
			t.Errorf("%s: pages only, but takes sortBy", a.Endpoint())
		}
		if a.DefaultSort != "" && !slices.Contains(a.Sort.Values, a.DefaultSort) {
			t.Errorf("%s: default sort %s is no sortBy value", a.Endpoint(), a.DefaultSort)
		}
	}
}

// TestAddedSortables pins that the endpoints paging in SQL can sort by
// every sortBy value they accept.
func TestAddedSortables(t *testing.T) {
	for _, c := range []struct {
		sort     core.Param
		sortable map[string]string
	}{
		{core.MolecularDataSortParam, molecularDataSortable},
		{core.MolecularDataSortParam, molecularDataMultiSortable},
		{core.DiscreteCopyNumberSort, cnaEventSortable},
		{core.DiscreteCopyNumberSort, cnaValueSortable},
		{core.GenericAssayDataSort, genericAssayDataSortable},
		{core.FilteredSampleSort, filteredSampleSortable},
		{core.StructuralVariantSortParam, structuralVariantSortable},
		{core.CopyNumberSegmentSort, segmentSortable},
		{core.ClinicalDataSort, clinicalDataSortable},
		{core.SampleSort, sampleSortable},
		{core.PatientSort, patientSortable},
	} {
		for _, v := range c.sort.Values {
			if _, ok := c.sortable[v]; !ok {
				t.Errorf("%s: no SQL for %s", c.sort.Class, v)
			}
		}
		if len(c.sortable) != len(c.sort.Values) {
			t.Errorf("%s: %d SQL sorts for %d values", c.sort.Class, len(c.sortable), len(c.sort.Values))
		}
	}
}

// TestClaimListPage pins the ORDER BY / LIMIT a handler reads a page with:
// the stable order alone without a page, the page of an unsorted request
// in that order, a sorted one by the field first, and no claim for a field
// the handler cannot sort by in SQL.
func TestClaimListPage(t *testing.T) {
	sortable := map[string]string{"v": "toFloat64OrNull(v)", "same": ""}
	r := httptest.NewRequest("POST", "/api/x", nil)
	if got := core.SQLListOrder(r, sortable, "id"); got != " ORDER BY id" {
		t.Errorf("no page: %q", got)
	}
	for _, c := range []struct {
		pg   core.Page
		want string
	}{
		{core.Page{Size: 3, Start: 6}, " ORDER BY id LIMIT 3 OFFSET 6"},
		{core.Page{SortBy: "v", Desc: true, Size: 2}, " ORDER BY toFloat64OrNull(v) DESC NULLS LAST, id LIMIT 2 OFFSET 0"},
		{core.Page{SortBy: "v", Size: -1}, " ORDER BY toFloat64OrNull(v) ASC NULLS LAST, id"},
		{core.Page{SortBy: "same", Size: 1}, " ORDER BY id LIMIT 1 OFFSET 0"},
		{core.Page{SortBy: "other", Size: 1}, " ORDER BY id"},
	} {
		sp := &core.SQLPage{Page: c.pg}
		r := r.WithContext(context.WithValue(r.Context(), core.SQLPageKey{}, sp))
		if got := core.SQLListOrder(r, sortable, "id"); got != c.want || sp.Claimed != (c.pg.SortBy != "other") {
			t.Errorf("%+v: %q claimed %v, want %q", c.pg, got, sp.Claimed, c.want)
		}
	}
	// A keyed page is for sorted pages only.
	sp := &core.SQLPage{Page: core.Page{Size: 1}}
	if core.ClaimKeyedPage(r.WithContext(context.WithValue(r.Context(), core.SQLPageKey{}, sp))) != nil {
		t.Error("unsorted page claimed as keyed")
	}
}

// TestAddedPagingInMemory pins pageList over an in-memory list with added
// paging: no parameters leave the response as the handler wrote it, a page
// is cut, numeric strings sort as numbers (non-numbers last), and a list
// with a default sort is sorted by it when paged.
func TestAddedPagingInMemory(t *testing.T) {
	body := `[{"geneticEntityId":"10","pValue":0.5},{"geneticEntityId":"9","pValue":0.1},` +
		`{"geneticEntityId":"x","pValue":0.3},{"geneticEntityId":"100","pValue":null}]` + "\n"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	})
	serve := func(path string) string {
		got := httptest.NewRecorder()
		core.PageList(handler).ServeHTTP(got, httptest.NewRequest("POST", path, nil))
		return got.Body.String()
	}
	const coexp = "/api/molecular-profiles/co-expressions/fetch"
	if got := serve(coexp + "?molecularProfileIdA=a"); got != body {
		t.Errorf("unpaged: %q", got)
	}
	for q, want := range map[string]string{
		"pageSize=2&pageNumber=1":               `[{"geneticEntityId":"x","pValue":0.3},{"geneticEntityId":"100","pValue":null}]`,
		"sortBy=geneticEntityId":                `[{"geneticEntityId":"9","pValue":0.1},{"geneticEntityId":"10","pValue":0.5},{"geneticEntityId":"100","pValue":null},{"geneticEntityId":"x","pValue":0.3}]`,
		"sortBy=pValue&direction=DESC":          `[{"geneticEntityId":"10","pValue":0.5},{"geneticEntityId":"x","pValue":0.3},{"geneticEntityId":"9","pValue":0.1},{"geneticEntityId":"100","pValue":null}]`,
		"sortBy=pValue&pageSize=1&pageNumber=2": `[{"geneticEntityId":"10","pValue":0.5}]`,
	} {
		if got := strings.TrimSpace(serve(coexp + "?" + q)); got != want {
			t.Errorf("%s: %s, want %s", q, got, want)
		}
	}
	genes := `[{"entrezGeneId":672},{"entrezGeneId":7157},{"entrezGeneId":675}]` + "\n"
	body = genes
	if got := serve("/api/genes/fetch"); got != genes {
		t.Errorf("genes unpaged: %q", got)
	}
	if got := strings.TrimSpace(serve("/api/genes/fetch?pageSize=2")); got != `[{"entrezGeneId":672},{"entrezGeneId":675}]` {
		t.Errorf("genes page: %s", got)
	}
}
