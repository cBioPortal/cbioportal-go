package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestAddedPagingWired pins that every endpoint with added paging is paged
// by pageList and validates its parameters as the Java list endpoints do.
func TestAddedPagingWired(t *testing.T) {
	for _, a := range AddedPagedEndpoints {
		if !slices.Contains(pagedEndpoints, a.Endpoint()) {
			t.Errorf("%s: not in pagedEndpoints", a.Endpoint())
		}
		rt, _ := matchContract(a.Method, strings.ReplaceAll(templatePath(a.Path), "{}", "x"))
		if rt == nil {
			t.Errorf("%s: no contract route", a.Endpoint())
			continue
		}
		names := map[string]int{}
		for _, p := range rt.params {
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
		if !a.Sorted() && (names["sortBy"] > 0 || !sortIgnored(a.Endpoint())) {
			t.Errorf("%s: pages only, but takes sortBy", a.Endpoint())
		}
		if a.defaultSort != "" && !slices.Contains(a.Sort.Values, a.defaultSort) {
			t.Errorf("%s: default sort %s is no sortBy value", a.Endpoint(), a.defaultSort)
		}
	}
}

// TestAddedParamsWired pins that every parameter go adds is checked once,
// as Java checks a parameter of its type, on its endpoint's contract route.
func TestAddedParamsWired(t *testing.T) {
	for _, a := range AddedParams {
		rt, _ := matchContract(a.Method, strings.ReplaceAll(templatePath(a.Path), "{}", "x"))
		if rt == nil {
			t.Errorf("%s %s: no contract route", a.Method, a.Path)
			continue
		}
		n := 0
		for _, p := range rt.params {
			if p.Name == a.Param.Name {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s %s: %d %s params", a.Method, a.Path, n, a.Param.Name)
		}
		q := map[string][]string{"sampleListId": {"l"}, a.Param.Name: {"TP53"}}
		if got := CheckParams(rt.params, q); got != MsgIntMismatch("TP53") {
			t.Errorf("%s %s: %s=TP53 gives %q", a.Method, a.Path, a.Param.Name, got)
		}
		q[a.Param.Name] = []string{"7157"}
		if got := CheckParams(rt.params, q); got != "" {
			t.Errorf("%s %s: %s=7157 gives %q", a.Method, a.Path, a.Param.Name, got)
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
	if got := SQLListOrder(r, sortable, "id"); got != " ORDER BY id" {
		t.Errorf("no page: %q", got)
	}
	for _, c := range []struct {
		pg   page
		want string
	}{
		{page{Size: 3, Start: 6}, " ORDER BY id LIMIT 3 OFFSET 6"},
		{page{SortBy: "v", Desc: true, Size: 2}, " ORDER BY toFloat64OrNull(v) DESC NULLS LAST, id LIMIT 2 OFFSET 0"},
		{page{SortBy: "v", Size: -1}, " ORDER BY toFloat64OrNull(v) ASC NULLS LAST, id"},
		{page{SortBy: "same", Size: 1}, " ORDER BY id LIMIT 1 OFFSET 0"},
		{page{SortBy: "other", Size: 1}, " ORDER BY id"},
	} {
		sp := &SQLPage{page: c.pg}
		r := r.WithContext(context.WithValue(r.Context(), sqlPageKey{}, sp))
		if got := SQLListOrder(r, sortable, "id"); got != c.want || sp.claimed != (c.pg.SortBy != "other") {
			t.Errorf("%+v: %q claimed %v, want %q", c.pg, got, sp.claimed, c.want)
		}
	}
	// A keyed page is for sorted pages only.
	sp := &SQLPage{page: page{Size: 1}}
	if ClaimKeyedPage(r.WithContext(context.WithValue(r.Context(), sqlPageKey{}, sp))) != nil {
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
		PageList(handler).ServeHTTP(got, httptest.NewRequest("POST", path, nil))
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
