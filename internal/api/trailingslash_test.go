package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// TestTrailingSlashRouter checks the router's trailing-slash answers against
// Java's: errors name the requested path, a method the trimmed route does
// not serve is a 405, and a POST to a legacy endpoint whose filter Java's
// interceptor parses answers as without the slash.
func TestTrailingSlashRouter(t *testing.T) {
	h := NewRouter(newState(&core.State{}))
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
	} {
		w := serveSlash(h, c.method, c.path, c.body)
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s %s %s: status %d body %q, want %d with %q", c.method, c.path, c.body, w.Code, w.Body.String(), c.status, c.want)
		}
	}
	for _, c := range []struct{ path, body string }{
		{"/api/clinical-data/fetch", `{"attributeIds":["AGE"]}`},
		{"/api/clinical-data/fetch", ""},
		{"/api/clinical-data/fetch", "{bad"},
		{"/api/clinical-data/fetch", "[]"},
		{"/api/legacy/patients/fetch", "{}"},
		{"/api/copy-number-segments/fetch", "[]"},
		{"/api/copy-number-segments/fetch", "{}"},
		{"/api/generic_assay_data/fetch", "{}"},
	} {
		want := serveSlash(h, "POST", c.path, c.body)
		got := serveSlash(h, "POST", c.path+"/", c.body)
		wantBody := strings.ReplaceAll(want.Body.String(), `"path":"`+c.path+`"`, `"path":"`+c.path+`/"`)
		if got.Code != want.Code || got.Body.String() != wantBody {
			t.Errorf("POST %s/ %s: status %d body %q, want %d %q", c.path, c.body, got.Code, got.Body.String(), want.Code, wantBody)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/swagger-ui/", nil))
	if loc := w.Header().Get("Location"); !strings.HasSuffix(loc, "/api/swagger-ui/index.html") {
		t.Errorf("GET /api/swagger-ui/: Location %q", loc)
	}
}

func serveSlash(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
