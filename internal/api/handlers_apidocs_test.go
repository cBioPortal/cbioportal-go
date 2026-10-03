package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegacyAPIDocs(t *testing.T) {
	s := &State{}
	r := httptest.NewRequest(http.MethodGet, "/api/v2/api-docs", nil)
	r.Host = "api.example.org"
	w := httptest.NewRecorder()
	s.legacyAPIDocs(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, content type %q", w.Code, w.Header().Get("Content-Type"))
	}
	var doc struct {
		Swagger string                     `json:"swagger"`
		Host    string                     `json:"host"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Swagger != "2.0" || doc.Host != "api.example.org" || len(doc.Paths) == 0 {
		t.Errorf("swagger %q host %q paths %d", doc.Swagger, doc.Host, len(doc.Paths))
	}
}

func TestSplitTopLevelValue(t *testing.T) {
	for _, c := range []struct{ doc, want string }{
		{`{"a": {"host": "nested"}, "host": "x", "b": [1]}`, `{"a":{"host":"nested"},"host":"H","b":[1]}`},
		{`{"a": 1}`, `{"a":1,"host":"H"}`},
	} {
		before, after, err := splitTopLevelValue([]byte(c.doc), "host")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(before) + `"H"` + string(after); got != c.want {
			t.Errorf("%s: got %s, want %s", c.doc, got, c.want)
		}
	}
}
