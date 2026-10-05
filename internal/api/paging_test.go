package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
		pageList(handler).ServeHTTP(got, httptest.NewRequest("GET", "/api/studies/s/clinical-data?"+c.query, nil))
		want := httptest.NewRecorder()
		status := c.status
		if status == 0 {
			status = http.StatusOK
		}
		pg := page{size: -1}
		var size, number int
		if _, err := fmt.Sscanf(c.query, "pageSize=%d&pageNumber=%d", &size, &number); err == nil || size > 0 || strings.HasPrefix(c.query, "pageSize=0") {
			pg.start, pg.size = number*size, size
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
		{"GET", "/api/molecular-profiles/x_cna/discrete-copy-number", false},
		{"POST", "/api/molecular-profiles/x_mutations/mutations/fetch", true},
		{"GET", "/api/molecular-profiles/x_mutations/mutations/fetch", false},
		{"POST", "/api/genes/fetch", false},
	} {
		if got := pagedEndpoint(c.method, c.path); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
