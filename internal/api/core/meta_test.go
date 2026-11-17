package core

import (
	"fmt"
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

// TestMetaProjectionStudyFilter checks that a projection=META count leaves
// out the records of studies the caller may not read, so the count says the
// same as the list the same request without META would answer.
func TestMetaProjectionStudyFilter(t *testing.T) {
	records := func(n int, study string) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, `{"studyId":%q,"uniqueSampleKey":"k%d"},`, study, i)
		}
		return b.String()
	}
	for _, c := range []struct {
		name, body string
		readable   []string
		want       string
	}{
		{name: "one study left out", body: `[{"studyId":"a"},{"studyId":"b"},{"studyId":"a"}]`,
			readable: []string{"a"}, want: "2"},
		{name: "no readable study", body: `[{"studyId":"a"},{"studyId":"b"}]`,
			readable: []string{}, want: "0"},
		{name: "record without a study counts", body: `[{"studyId":"b"},{"name":"no study"}]`,
			readable: []string{"a"}, want: "1"},
		{name: "streamed in pieces", body: "[" + records(4000, "a") + records(4000, "b") + `{"studyId":"a"}]`,
			readable: []string{"a"}, want: "4001"},
	} {
		h := MetaProjection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			for b := c.body; b != ""; {
				n := min(len(b), 97)
				w.Write([]byte(b[:n]))
				b = b[n:]
			}
		}))
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/samples?projection=META", nil)
		h.ServeHTTP(rec, WithMetaStudyFilter(r, NewMetaStudyFilter(c.readable)))
		if got := rec.Header().Get("total-count"); got != c.want {
			t.Errorf("%s: total-count %q, want %q", c.name, got, c.want)
		}
	}
	// Without a filter every record counts.
	h := MetaProjection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"studyId":"a"},{"studyId":"b"}]`))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/samples?projection=META", nil))
	if got := rec.Header().Get("total-count"); got != "2" {
		t.Errorf("no filter: total-count %q, want 2", got)
	}
}

// TestMetaCountSQL checks the count query a filtered META answer runs.
func TestMetaCountSQL(t *testing.T) {
	sel := "SELECT study_id AS studyId FROM sample"
	plain := MetaCountSQL(httptest.NewRequest("GET", "/api/samples?projection=META", nil), NewArgs(), sel)
	if plain != "SELECT count() AS n FROM ("+sel+")" {
		t.Errorf("without a filter: %s", plain)
	}
	args := NewArgs()
	r := WithMetaStudyFilter(httptest.NewRequest("GET", "/api/samples?projection=META", nil),
		NewMetaStudyFilter([]string{"a", "b"}))
	sql := MetaCountSQL(r, args, sel)
	if !strings.Contains(sql, ") WHERE studyId IN {a0:Array(String)}") {
		t.Errorf("with a filter: %s", sql)
	}
	if p := args.Params(sql); len(p) != 1 {
		t.Errorf("params %v", p)
	}
}
