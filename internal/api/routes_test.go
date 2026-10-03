package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// frontendAPIDocs are the Swagger documents the frontend's generated API
// clients are built from; every operation in them must reach a route.
var frontendAPIDocs = []string{
	"../../frontend/packages/cbioportal-ts-api-client/src/generated/CBioPortalAPI-docs.json",
	"../../frontend/packages/cbioportal-ts-api-client/src/generated/CBioPortalAPIInternal-docs.json",
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// conditionalOperations are registered by Java only under configuration this
// backend does not offer (dat.method=oauth2); Java answers 404 without it.
var conditionalOperations = map[string]bool{
	"GET /api/data-access-token/oauth2": true,
}

// servedOutsideMux reports whether the wrapping middleware answers a request
// before it reaches the mux.
func servedOutsideMux(method, path string) bool {
	if _, ok := apiAliasPath(path); ok {
		return true
	}
	return method == http.MethodGet && (path == "/api/studies/meta" || isCacheStatsPath(path))
}

// TestFrontendOperationsRouted checks that no operation of the frontend's
// generated API clients falls through to the catch-all, which answers 404
// for unknown /api/ paths.
func TestFrontendOperationsRouted(t *testing.T) {
	mux := http.NewServeMux()
	(&State{}).routes(mux)
	var missing []string
	checked := 0
	for _, doc := range frontendAPIDocs {
		raw, err := os.ReadFile(filepath.FromSlash(doc))
		if err != nil {
			t.Skipf("frontend API docs not available: %v", err)
		}
		var spec struct {
			Paths map[string]map[string]json.RawMessage `json:"paths"`
		}
		if err := json.Unmarshal(raw, &spec); err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		for p, ops := range spec.Paths {
			path := pathParam.ReplaceAllString(p, "x")
			for m := range ops {
				method := strings.ToUpper(m)
				checked++
				if servedOutsideMux(method, path) || conditionalOperations[method+" "+p] {
					continue
				}
				req := httptest.NewRequest(method, path, nil)
				if _, pattern := mux.Handler(req); pattern == "/" {
					missing = append(missing, method+" "+p)
				}
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d of %d frontend operations fall through to the catch-all:\n%s",
			len(missing), checked, strings.Join(missing, "\n"))
	}
}

// TestUnknownAPIPathNotFound checks that an unmatched /api/ path answers
// Spring's default 404 body.
func TestUnknownAPIPathNotFound(t *testing.T) {
	s := &State{}
	mux := http.NewServeMux()
	s.routes(mux)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/no-such-endpoint", strings.NewReader("{}")))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", method, w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: body %q: %v", method, w.Body.String(), err)
		}
		if body["status"] != float64(404) || body["error"] != "Not Found" || body["path"] != "/api/no-such-endpoint" || body["timestamp"] == nil {
			t.Errorf("%s: body %v", method, body)
		}
	}
}

// TestKnownAPIPathWrongMethod checks that a routed /api/ path requested with
// a method it is not served for answers Spring's 405.
func TestKnownAPIPathWrongMethod(t *testing.T) {
	s := &State{}
	mux := http.NewServeMux()
	s.routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/mutation-counts-by-position/fetch", nil))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
		t.Fatalf("status %d, Allow %q; want 405, POST", w.Code, w.Header().Get("Allow"))
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != "Method Not Allowed" || body["status"] != float64(405) {
		t.Errorf("body %q (%v)", w.Body.String(), err)
	}
}

// TestTrailingSlashMatch checks that /api/ paths with a trailing slash are
// served by the route without it, as Java's setUseTrailingSlashMatch does.
func TestTrailingSlashMatch(t *testing.T) {
	s := &State{}
	mux := http.NewServeMux()
	s.routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hello/", nil))
	if w.Code != http.StatusOK || w.Body.String() != "Greetings from Spring Boot!" {
		t.Fatalf("GET /api/hello/: status %d body %q, want 200 greeting", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/no-such-endpoint/", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/no-such-endpoint/: status %d, want 404", w.Code)
	}
}
