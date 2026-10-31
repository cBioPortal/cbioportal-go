package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestPageListStreams checks that an unsorted page streamed from the
// handler's array is what slicing the whole response gives.
func TestPageListStreams(t *testing.T) {
	var items []string
	for i := range 2500 {
		items = append(items, fmt.Sprintf(`{"uniqueSampleKey":"k%d","v":[1, {"a":"<&>"}]}`, i))
	}
	array := "[" + strings.Join(items, ",") + "]\n"
	for _, c := range []struct {
		body   string
		status int
		query  string
	}{
		{array, 0, "pageSize=100&pageNumber=3"},
		{array, 0, "pageSize=1000&pageNumber=2"},
		{array, 0, "pageSize=7&pageNumber=999"},
		{array, 0, "pageSize=0"},
		{array, 0, "pageNumber=1"},
		{array, 0, "pageSize=x"},
		{"[]\n", 0, "pageSize=10"},
		{" [ ]", 0, "pageSize=10"},
		{"null\n", 0, "pageSize=10"},
		{`{"a":1}`, 0, "pageSize=10"},
		{"boom\n", 500, "pageSize=10"},
		{"", 0, "pageSize=10"},
	} {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Has("pageSize") || r.URL.Query().Has("pageNumber") {
				t.Errorf("handler saw %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			if c.status != 0 {
				w.WriteHeader(c.status)
			}
			for b := c.body; b != ""; {
				n := min(len(b), 13)
				w.Write([]byte(b[:n]))
				b = b[n:]
			}
		})
		got := httptest.NewRecorder()
		PageList(handler).ServeHTTP(got, httptest.NewRequest("GET", "/api/studies/s/clinical-data?"+c.query, nil))
		want := httptest.NewRecorder()
		status := c.status
		if status == 0 {
			status = http.StatusOK
		}
		pg := page{Size: -1}
		var size, number int
		if _, err := fmt.Sscanf(c.query, "pageSize=%d&pageNumber=%d", &size, &number); err == nil || size > 0 || strings.HasPrefix(c.query, "pageSize=0") {
			pg.Start, pg.Size = number*size, size
		}
		writePage(want, http.Header{"Content-Type": {"application/json"}}, status, []byte(c.body), pg)
		if got.Code != want.Code || got.Body.String() != want.Body.String() ||
			got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
			t.Errorf("%q %s: got %d %q, want %d %q", c.body[:min(len(c.body), 20)], c.query,
				got.Code, got.Body.String()[:min(got.Body.Len(), 80)], want.Code, want.Body.String()[:min(want.Body.Len(), 80)])
		}
	}
}

func TestPagedEndpoint(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/studies", true},
		{"GET", "/api/studies/acc_tcga/samples", true},
		{"GET", "/api/gene-panels/", true},
		{"POST", "/api/samples/fetch", true},
		{"POST", "/api/mutated-genes/fetch", false},
		{"POST", "/api/molecular-profiles/x_mutations/mutations/fetch", true},
		{"GET", "/api/molecular-profiles/x_mutations/mutations/fetch", false},
	} {
		if got := pagedEndpointOf(c.method, c.path) != ""; got != c.want {
			t.Errorf("%s %s: %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

// TestStudyClinicalEventsPageSize checks the study's clinical-events bounds
// (ClinicalEventController): pageSize defaults to 10000 and may not exceed it.
func TestStudyClinicalEventsPageSize(t *testing.T) {
	var items []string
	for i := range 10001 {
		items = append(items, fmt.Sprintf(`{"i":%d}`, i))
	}
	array := "[" + strings.Join(items, ",") + "]"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(array))
	})
	study, patient := "/api/studies/s/clinical-events", "/api/studies/s/patients/p/clinical-events"
	for _, c := range []struct {
		url  string
		want int
	}{
		{study, 10000},
		{study + "?pageSize=", 10000},
		{study + "?pageNumber=1", 1},
		{study + "?pageSize=5000&pageNumber=2", 1},
		{study + "?pageSize=3", 3},
		{patient, 10001},
		{patient + "?pageNumber=1", 10001},
		{patient + "?pageSize=3", 3},
	} {
		got := httptest.NewRecorder()
		PageList(handler).ServeHTTP(got, httptest.NewRequest("GET", c.url, nil))
		var page []json.RawMessage
		if err := json.Unmarshal(got.Body.Bytes(), &page); err != nil || len(page) != c.want {
			t.Errorf("%s: got %d items (%v), want %d", c.url, len(page), err, c.want)
		}
	}
	rt, _ := matchContract("GET", "/api/studies/s/clinical-events")
	for query, want := range map[string]string{
		"pageSize=10000": "",
		"pageSize=10001": "pageSize must be less than or equal to 10000",
	} {
		q, _ := url.ParseQuery(query)
		if got := CheckParams(rt.params, q); got != want {
			t.Errorf("%q: got %q, want %q", query, got, want)
		}
	}
}
