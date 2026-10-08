package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTrailingSlashMatch checks that /api/ paths with a trailing slash are
// served by the route without it, path parameters included, as Java's
// setUseTrailingSlashMatch does.
func TestTrailingSlashMatch(t *testing.T) {
	mux := http.NewServeMux()
	echo := func(s string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(s + r.PathValue("id"))) }
	}
	mux.HandleFunc("/", echo("catch-all"))
	mux.HandleFunc("GET /api/{$}", echo("docs"))
	mux.HandleFunc("GET /api/studies", echo("studies"))
	mux.HandleFunc("GET /api/studies/{id}", echo("study "))
	mux.HandleFunc("GET /api/studies/{id}/samples", echo("samples of "))
	s := &State{mux: mux}
	h := s.TrailingSlash(mux)
	for path, want := range map[string]string{
		"/api/studies/x/":           "study x",
		"/api/studies/a%20b/":       "study a b",
		"/api/studies/x/samples/":   "samples of x",
		"/api/legacy/studies/x/":    "catch-all", // the alias is resolved after this middleware
		"/api/studies/":             "studies",
		"/api/studies":              "studies",
		"/api/":                     "docs",
		"/api/no-such-endpoint/":    "catch-all",
		"/studies/x/":               "catch-all",
		"/api/studies/x/samples/y/": "catch-all",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Body.String() != want {
			t.Errorf("GET %s: %q, want %q", path, w.Body.String(), want)
		}
	}
}
