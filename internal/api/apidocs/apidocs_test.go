package apidocs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

func TestLegacyAPIDocs(t *testing.T) {
	s := New(&core.State{})
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

func TestOpenAPIDocs(t *testing.T) {
	mux := http.NewServeMux()
	New(&core.State{}).Routes(mux)
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "go.example.org"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	for _, p := range []string{"/api/v3/api-docs", "/api/v3/api-docs/public", "/api/v3/api-docs/internal"} {
		w := get(p)
		var doc struct {
			OpenAPI string `json:"openapi"`
			Info    struct {
				License struct {
					Name string `json:"name"`
					URL  string `json:"url"`
				} `json:"license"`
			} `json:"info"`
			Servers []struct {
				URL string `json:"url"`
			} `json:"servers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if doc.OpenAPI == "" || len(doc.Servers) != 1 || doc.Servers[0].URL != "http://go.example.org" {
			t.Errorf("%s: openapi %q servers %+v", p, doc.OpenAPI, doc.Servers)
		}
		// Java's SwaggerConfig declares the backend's Apache-2.0 license.
		if l := doc.Info.License; l.Name != "Apache-2.0" || l.URL != "https://www.apache.org/licenses/LICENSE-2.0" {
			t.Errorf("%s: license %+v", p, l)
		}
	}
	for _, p := range []string{"/api/v3/api-docs.yaml", "/api/v3/api-docs.yaml/public", "/api/v3/api-docs.yaml/internal"} {
		if w := get(p); !strings.Contains(w.Body.String(), "  license:\n    name: Apache-2.0\n    url: https://www.apache.org/licenses/LICENSE-2.0\n") {
			t.Errorf("%s: license missing", p)
		}
	}
	if w := get("/api/v3/api-docs.yaml"); !strings.Contains(w.Body.String(), "- url: http://go.example.org\n") ||
		w.Header().Get("Content-Type") != "application/vnd.oai.openapi" {
		t.Errorf("yaml: server url or content type %q", w.Header().Get("Content-Type"))
	}
	if w := get("/api/v3/api-docs/swagger-config"); !strings.Contains(w.Body.String(), `"oauth2RedirectUrl":"http://go.example.org/api/swagger-ui/oauth2-redirect.html"`) {
		t.Errorf("swagger-config: %s", w.Body.String())
	}
	for _, path := range []string{"/api", "/api/"} {
		if w := get(path); w.Code != http.StatusFound || w.Header().Get("Location") != "http://go.example.org/api/swagger-ui" {
			t.Errorf("%s redirect: %d %q", path, w.Code, w.Header().Get("Location"))
		}
	}
	if w := get("/api/swagger-ui"); w.Code != http.StatusFound || w.Header().Get("Location") != "http://go.example.org/api/swagger-ui/index.html" {
		t.Errorf("swagger-ui redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := get("/api/swagger-ui/swagger-ui-bundle.js"); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/javascript" {
		t.Errorf("bundle: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get("/api/swagger-ui/nope.js"); w.Code != http.StatusNotFound {
		t.Errorf("missing file: %d", w.Code)
	}
}

// The descriptions answer 406 for an Accept header that rules out their
// media type, as springdoc's endpoints do on the Java portal.
func TestOpenAPIDocsAccept(t *testing.T) {
	mux := http.NewServeMux()
	New(&core.State{}).Routes(mux)
	for _, c := range []struct {
		path, accept string
		want         int
		body         string
	}{
		{"/api/v3/api-docs.yaml", "", http.StatusOK, ""},
		{"/api/v3/api-docs.yaml", "*/*", http.StatusOK, ""},
		{"/api/v3/api-docs.yaml", "application/json, text/plain, */*", http.StatusOK, ""},
		{"/api/v3/api-docs.yaml", "application/*", http.StatusOK, ""},
		{"/api/v3/api-docs.yaml", "application/vnd.oai.openapi;q=0.5", http.StatusOK, ""},
		{"/api/v3/api-docs.yaml", "application/json", http.StatusNotAcceptable, `"error":"Not Acceptable","path":"/api/v3/api-docs.yaml"}`},
		{"/api/v3/api-docs.yaml/public", "text/plain", http.StatusNotAcceptable, ""},
		{"/api/v3/api-docs", "application/json", http.StatusOK, ""},
		{"/api/v3/api-docs", "application/yaml", http.StatusNotAcceptable, ""},
	} {
		r := httptest.NewRequest(http.MethodGet, c.path, nil)
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != c.want || !strings.Contains(w.Body.String(), c.body) {
			t.Errorf("%s Accept %q: %d %.100s", c.path, c.accept, w.Code, w.Body.String())
		}
		if c.want == http.StatusNotAcceptable && c.accept == "text/plain" && w.Body.Len() != 0 {
			t.Errorf("%s Accept text/plain: body %q", c.path, w.Body.String())
		}
	}
}
