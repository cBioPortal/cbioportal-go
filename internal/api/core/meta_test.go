package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetaProjection(t *testing.T) {
	big := "[" + strings.Repeat(`{"uniqueSampleKey":"a","v":"<&>"},{"uniqueSampleKey":"b"},`, 5000) + `{"x":1}]` + "\n"
	for _, c := range []struct {
		name, path, body   string
		status             int
		preset             string // a total-count the handler sets itself
		wantStatus         int
		wantTotal, wantSam string
		wantBody           string
	}{
		{name: "empty", path: "/api/x", body: "[]\n", wantStatus: 200, wantTotal: "0"},
		{name: "objects", path: "/api/x/mutations/fetch", body: `[{"uniqueSampleKey":"a"},{"uniqueSampleKey":"a"},{"uniqueSampleKey":""}]`,
			wantStatus: 200, wantTotal: "3", wantSam: "1"},
		{name: "big, in pieces", path: "/api/mutations/fetch", body: big, wantStatus: 200, wantTotal: "10001", wantSam: "2"},
		{name: "no sample count elsewhere", path: "/api/x", body: `[{"uniqueSampleKey":"a"}]`, wantStatus: 200, wantTotal: "1"},
		{name: "strings pass through", path: "/api/x", body: `["a","b"]` + "\n", wantStatus: 200, wantBody: `["a","b"]` + "\n"},
		{name: "null counts as empty", path: "/api/x", body: "null\n", wantStatus: 200, wantTotal: "0"},
		{name: "object passes through", path: "/api/x", body: `{"a":1}`, wantStatus: 200, wantBody: `{"a":1}`},
		{name: "error passes through", path: "/api/x", body: "boom\n", status: 500, wantStatus: 500, wantBody: "boom\n"},
		{name: "error array passes through", path: "/api/x", body: "[]", status: 400, wantStatus: 400, wantBody: "[]"},
		{name: "handler's own count", path: "/api/x", body: "", preset: "7", wantStatus: 200, wantTotal: "7"},
	} {
		h := MetaProjection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if c.preset != "" {
				w.Header().Set("total-count", c.preset)
			}
			if c.status != 0 {
				w.WriteHeader(c.status)
			}
			for b := c.body; b != ""; {
				n := min(len(b), 7)
				w.Write([]byte(b[:n]))
				b = b[n:]
			}
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path+"?projection=META", nil))
		if rec.Code != c.wantStatus || rec.Header().Get("total-count") != c.wantTotal ||
			rec.Header().Get("sample-count") != c.wantSam || rec.Body.String() != c.wantBody {
			t.Errorf("%s: %d total=%q samples=%q body=%q", c.name, rec.Code, rec.Header().Get("total-count"),
				rec.Header().Get("sample-count"), rec.Body.String())
		}
		if c.wantTotal != "" && c.preset == "" && rec.Header().Get("Content-Type") != "" {
			t.Errorf("%s: Content-Type %q kept", c.name, rec.Header().Get("Content-Type"))
		}
	}
}

// TestMetaProjectionAborted checks that a handler aborting mid-array does not
// leave the counter waiting.
func TestMetaProjectionAborted(t *testing.T) {
	h := MetaProjection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"uniqueSampleKey":"a"},`))
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Errorf("recovered %v", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/x?projection=META", nil))
}
