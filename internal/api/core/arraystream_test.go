package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestArraySplitter feeds arrays in pieces of every size and checks the
// elements against encoding/json's.
func TestArraySplitter(t *testing.T) {
	for _, body := range []string{
		`[]`, ` [ ] `, `[1]`, `[1,2 , 3]`, `["a","b\"]","c\\"]`, `[{"a":[1,{"b":"}"}]},{"c":null}]` + "\n",
		`[true,false,null,-1.5e3,{},[]]`, `[ {"x" : "y" } , "z" ]`,
	} {
		var want []json.RawMessage
		if err := json.Unmarshal([]byte(body), &want); err != nil {
			t.Fatal(err)
		}
		for size := 1; size <= len(body); size++ {
			var a arraySplitter
			var got []string
			for b := body; b != ""; {
				n := min(size, len(b))
				if err := a.feed([]byte(b[:n]), func(el []byte) error { got = append(got, string(el)); return nil }); err != nil {
					t.Fatalf("%q/%d: %v", body, size, err)
				}
				b = b[n:]
			}
			if !a.complete() || len(got) != len(want) {
				t.Fatalf("%q/%d: complete %v, got %q", body, size, a.complete(), got)
			}
			for i := range got {
				if got[i] != string(want[i]) {
					t.Errorf("%q/%d: element %d = %q, want %q", body, size, i, got[i], want[i])
				}
			}
		}
	}
	for _, bad := range []string{`{}`, `x`, `[1]x`, `[1 2]`} {
		var a arraySplitter
		if err := a.feed([]byte(bad), func([]byte) error { return nil }); err == nil && a.complete() {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestSortedPageStreams checks pageList's sorted pages, kept as the best
// items while the handler writes them, against sorting the whole list.
func TestSortedPageStreams(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var items []string
	for i := range 700 {
		var v string
		switch rng.Intn(5) {
		case 0:
			v = `"v":null`
		case 1:
			v = `"w":1`
		case 2:
			v = fmt.Sprintf(`"v":%d`, rng.Intn(50))
		default:
			v = fmt.Sprintf(`"v":"s%d"`, rng.Intn(50))
		}
		items = append(items, fmt.Sprintf(`{"i":%d,%s}`, i, v))
	}
	array := "[" + strings.Join(items, ",") + "]\n"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for b := array; b != ""; {
			n := min(len(b), 97)
			w.Write([]byte(b[:n]))
			b = b[n:]
		}
	})
	for _, query := range []string{
		"sortBy=v", "sortBy=v&direction=DESC", "sortBy=v&pageSize=10&pageNumber=3",
		"sortBy=v&direction=DESC&pageSize=25&pageNumber=0", "sortBy=v&pageSize=50&pageNumber=40", "sortBy=i&pageSize=1",
	} {
		got := httptest.NewRecorder()
		PageList(handler).ServeHTTP(got, httptest.NewRequest("GET", "/api/studies/s/clinical-data?"+query, nil))
		q, _ := url.ParseQuery(query)
		var raw []json.RawMessage
		json.Unmarshal([]byte(array), &raw)
		sortItems(raw, q.Get("sortBy"), q.Get("direction") == "DESC")
		if q.Has("pageSize") {
			var size, number int
			fmt.Sscan(q.Get("pageSize"), &size)
			fmt.Sscan(q.Get("pageNumber"), &number)
			raw = raw[min(size*number, len(raw)):min(size*(number+1), len(raw))]
		}
		want, _ := json.Marshal(raw)
		if got.Body.String() != string(want)+"\n" {
			t.Errorf("%s: got %.200s, want %.200s", query, got.Body.String(), want)
		}
	}
}

// TestSortNullsLast checks that items without the field sort last in either
// direction, ties in list order.
func TestSortNullsLast(t *testing.T) {
	raw := []json.RawMessage{[]byte(`{"a":2}`), []byte(`{"b":1}`), []byte(`{"a":1}`), []byte(`{"a":null}`), []byte(`{"a":2,"x":1}`)}
	sortItems(raw, "a", false)
	if got, _ := json.Marshal(raw); string(got) != `[{"a":1},{"a":2},{"a":2,"x":1},{"b":1},{"a":null}]` {
		t.Errorf("ASC: %s", got)
	}
	sortItems(raw, "a", true)
	if got, _ := json.Marshal(raw); string(got) != `[{"a":2},{"a":2,"x":1},{"a":1},{"b":1},{"a":null}]` {
		t.Errorf("DESC: %s", got)
	}
}

// TestSQLPageOrder checks the clause a handler claiming its sorted page
// reads with, and that pageList passes its response on as it is.
func TestSQLPageOrder(t *testing.T) {
	sortable := map[string]string{"value": "v", "none": ""}
	r := httptest.NewRequest("GET", "/", nil)
	if got := SQLPageOrder(r, sortable, "id"); got != " ORDER BY id" {
		t.Errorf("no page: %q", got)
	}
	for _, c := range []struct {
		pg   page
		want string
	}{
		{page{SortBy: "value", Size: -1}, " ORDER BY v ASC NULLS LAST, id"},
		{page{SortBy: "value", Desc: true, Start: 20, Size: 10}, " ORDER BY v DESC NULLS LAST, id LIMIT 10 OFFSET 20"},
		{page{SortBy: "none", Start: 5, Size: 5}, " ORDER BY id LIMIT 5 OFFSET 5"},
		{page{SortBy: "other", Size: 5}, " ORDER BY id"},
	} {
		sp := &SQLPage{page: c.pg}
		r := r.WithContext(context.WithValue(r.Context(), sqlPageKey{}, sp))
		if got := SQLPageOrder(r, sortable, "id"); got != c.want || sp.claimed != (c.pg.SortBy != "other") {
			t.Errorf("%+v: %q claimed %v", c.pg, got, sp.claimed)
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("pageSize") {
			t.Error("handler saw pageSize")
		}
		clause := SQLPageOrder(r, sortable, "id")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "[%q]\n", clause)
	})
	got := httptest.NewRecorder()
	PageList(handler).ServeHTTP(got, httptest.NewRequest("GET", "/api/studies/s/clinical-data?sortBy=value&direction=DESC&pageSize=3&pageNumber=2", nil))
	if want := `[" ORDER BY v DESC NULLS LAST, id LIMIT 3 OFFSET 6"]` + "\n"; got.Body.String() != want {
		t.Errorf("claimed page: %q", got.Body.String())
	}
}

// TestSortIgnored pins that pageList asks handlers for a sorted page also
// for keyword searches and projection=ID, and drops sortBy only where Java
// declares none.
func TestSortIgnored(t *testing.T) {
	for target, want := range map[string]string{
		"GET /api/studies/s/clinical-data?projection=ID&sortBy=value":         "value",
		"GET /api/studies/s/clinical-data?projection=SUMMARY&sortBy=value":    "value",
		"GET /api/samples?projection=ID&sortBy=sampleType":                    "sampleType",
		"GET /api/patients?keyword=x&sortBy=patientId":                        "patientId",
		"GET /api/genesets?sortBy=name&pageSize=1":                            "",
		"POST /api/generic-assay-meta/fetch?sortBy=x&pageSize=1&pageNumber=0": "",
	} {
		method, path, _ := strings.Cut(target, " ")
		seen := "unset"
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.Query().Get("sortBy")
			if sp, _ := r.Context().Value(sqlPageKey{}).(*SQLPage); (sp != nil && sp.SortBy != "") != (want != "") {
				t.Errorf("%s: sorted page %v", target, sp)
			}
			WriteJSON(w, []int{})
		})
		PageList(handler).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
		if seen != want {
			t.Errorf("%s: handler saw sortBy %q, want %q", target, seen, want)
		}
	}
}

// TestKeyedPage pins the page a handler sorts itself for ID rows: sorted by
// the key objects' field as pageList sorts (nulls last either way, ties in
// list order), then cut.
func TestKeyedPage(t *testing.T) {
	type key struct {
		K *int `json:"k"`
	}
	n := func(v int) *int { return &v }
	keys := []key{{n(2)}, {nil}, {n(1)}, {n(2)}, {n(3)}}
	for _, c := range []struct {
		query, want string
	}{
		{"sortBy=k", "[2,0,3,4,1]"},
		{"sortBy=k&direction=DESC", "[4,0,3,2,1]"},
		{"sortBy=k&pageSize=2&pageNumber=1", "[3,4]"},
	} {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kp := ClaimKeyedPage(r)
			if kp == nil {
				t.Fatalf("%s: no page to claim", c.query)
			}
			for i, k := range keys {
				kp.Add(k, i)
			}
			WriteJSON(w, kp.Rows())
		})
		got := httptest.NewRecorder()
		PageList(handler).ServeHTTP(got, httptest.NewRequest("GET", "/api/cancer-types?"+c.query, nil))
		if strings.TrimSpace(got.Body.String()) != c.want {
			t.Errorf("%s: %s, want %s", c.query, got.Body.String(), c.want)
		}
	}
}

// TestPageListLegacyStudies pins page numbering: /api/studies from 1, its
// /api/legacy/ alias from 0 as the legacy StudyController.
func TestPageListLegacyStudies(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, []int{0, 1, 2, 3, 4})
	})
	h := ExtraEndpoints(nil, PageList(handler))
	for path, want := range map[string]string{
		"/api/studies?pageSize=2&pageNumber=1":        "[0,1]",
		"/api/studies?pageSize=2&pageNumber=2":        "[2,3]",
		"/api/legacy/studies?pageSize=2&pageNumber=0": "[0,1]",
		"/api/legacy/studies?pageSize=2&pageNumber=1": "[2,3]",
	} {
		got := httptest.NewRecorder()
		h.ServeHTTP(got, httptest.NewRequest("GET", path, nil))
		if strings.TrimSpace(got.Body.String()) != want {
			t.Errorf("%s: %s, want %s", path, got.Body.String(), want)
		}
	}
}

// TestPageListCounted pins ColumnStoreGenericAssayController's paging:
// pageSize and pageNumber together or 400, a page carries the total-count
// of all items, and the legacy alias (no paging params) is not paged.
func TestPageListCounted(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, []int{0, 1, 2, 3, 4})
	})
	h := ExtraEndpoints(nil, PageList(handler))
	for _, c := range []struct {
		path        string
		status      int
		body, total string
	}{
		{"/api/generic-assay-meta/fetch?pageSize=2&pageNumber=1", 200, "[2,3]", "5"},
		{"/api/generic-assay-meta/fetch", 200, "[0,1,2,3,4]", ""},
		{"/api/generic-assay-meta/fetch?pageSize=2", 400, "", ""},
		{"/api/generic-assay-meta/fetch?pageNumber=1", 400, "", ""},
		{"/api/legacy/generic_assay_meta/fetch?pageSize=2&pageNumber=1", 200, "[0,1,2,3,4]", ""},
	} {
		got := httptest.NewRecorder()
		req := httptest.NewRequest("POST", c.path, strings.NewReader(`{"molecularProfileIds":["p"]}`))
		req.Header.Set("Accept", "application/json")
		h.ServeHTTP(got, req)
		if got.Code != c.status || got.Header().Get("total-count") != c.total ||
			(c.body != "" && strings.TrimSpace(got.Body.String()) != c.body) {
			t.Errorf("%s: %d %q total-count %q", c.path, got.Code, got.Body.String(), got.Header().Get("total-count"))
		}
	}
}

// TestStreamedReshape checks javaContract's element-by-element reshaping
// against reshaping the whole body.
func TestStreamedReshape(t *testing.T) {
	var items []string
	for i := range 300 {
		items = append(items, fmt.Sprintf(`{"sampleId":"s%d","patientId":"p","studyId":"st","sampleType":"x","uniqueSampleKey":"k","uniquePatientKey":"u","extra":[1,{"a":"]"}]}`, i))
	}
	body := "[" + strings.Join(items, ",") + "]\n"
	s := &State{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for b := body; b != ""; {
			n := min(len(b), 61)
			w.Write([]byte(b[:n]))
			b = b[n:]
		}
	})
	for _, proj := range []string{"ID", "SUMMARY", "DETAILED"} {
		got := httptest.NewRecorder()
		s.JavaContract(handler).ServeHTTP(got, httptest.NewRequest("POST", "/api/filtered-samples/fetch?projection="+proj, nil))
		want, _ := s.reshape([]byte(body), sampleShape["ID"], true, "", "")
		if got.Body.String() != string(want) || got.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: got %.200s", proj, got.Body.String())
		}
	}
}
