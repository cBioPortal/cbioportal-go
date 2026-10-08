package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// TestTrailingSlashRouter checks the router's trailing-slash answers against
// Java's: errors name the requested path, a method the trimmed route does
// not serve is a 405, and a POST to a legacy endpoint whose filter the
// interceptor parses fails as its controller does without the filter.
func TestTrailingSlashRouter(t *testing.T) {
	h := NewRouter(&State{State: &core.State{}})
	for _, c := range []struct {
		method, path, body string
		status             int
		want               string // a substring of the body
	}{
		{"GET", "/api/hello/", "", 200, "Greetings from Spring Boot!"},
		{"GET", "/api/legacy/hello/", "", 200, "Greetings from Spring Boot!"},
		{"GET", "/api/no-such-endpoint/", "", 404, `"path":"/api/no-such-endpoint/"`},
		{"GET", "/api/hello//", "", 400, `"path":"/api/hello//"`},
		{"POST", "/api/hello/", "{}", 405, `"path":"/api/hello/"`},
		{"GET", "/api/", "", 302, ""},
		{"GET", "/api/swagger-ui/", "", 302, ""},
		{"POST", "/api/clinical-data/fetch/", `{"attributeIds":["AGE"]}`, 500, `"path":"/api/clinical-data/fetch/"`},
		{"POST", "/api/clinical-data/fetch/", "", 500, `"error":"Internal Server Error"`},
		{"POST", "/api/clinical-data/fetch/", "{bad", 400, core.MsgBadJSON},
		{"POST", "/api/clinical-data/fetch/", "[]", 400, core.MsgBadJSON},
		{"POST", "/api/legacy/patients/fetch/", "{}", 500, `"path":"/api/legacy/patients/fetch/"`},
		{"POST", "/api/copy-number-segments/fetch/", "[]", 500, ""},
		{"POST", "/api/copy-number-segments/fetch/", "{}", 400, core.MsgBadJSON},
		{"POST", "/api/generic_assay_data/fetch/", "{}", 400, ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s %s %s: status %d body %q, want %d with %q", c.method, c.path, c.body, w.Code, w.Body.String(), c.status, c.want)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/swagger-ui/", nil))
	if loc := w.Header().Get("Location"); !strings.HasSuffix(loc, "/api/swagger-ui/index.html") {
		t.Errorf("GET /api/swagger-ui/: Location %q", loc)
	}
}
