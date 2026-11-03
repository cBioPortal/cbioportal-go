package core

import (
	"net/http"
	"net/url"
	"strings"
)

// TrailingSlash serves an /api/ path ending in one slash as the path without
// it, for every method: GET /api/studies/x/ is GET /api/studies/x, POST
// /api/studies/x/ the 405 of that route. Java up to v7.2.3 matched such
// paths the same way (WebAppConfig setUseTrailingSlashMatch); from v7.2.4
// (Spring Framework 7) it answers 404 (docs/rewrite/java-quirks.md). It applies
// only when the path as given has no route and the trimmed one has, so
// routes of their own such as /api/ keep their answer and a path parameter
// never matches an empty segment. Error bodies keep the requested path, as
// Spring's do.
//
// A POST to a legacy endpoint whose filter Java's
// InvolvedCancerStudyExtractorInterceptor parses answers as without the
// slash too: that interceptor matches the exact path, so up to v7.2.3
// Java's controller failed on such a request.
func (s *State) TrailingSlash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, "/api/") || !strings.HasSuffix(p, "/") || strings.HasSuffix(p, "//") ||
			s.mux == nil || s.routed(p) {
			next.ServeHTTP(w, r)
			return
		}
		trimmed := strings.TrimSuffix(p, "/")
		if !s.routed(trimmed) {
			next.ServeHTTP(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = trimmed
		r2.URL.RawPath = strings.TrimSuffix(r.URL.RawPath, "/")
		next.ServeHTTP(w, r2)
	})
}

// routed reports whether any method has a route for path, its /api/legacy/
// or /api/not-ready-yet/ alias resolved.
func (s *State) routed(path string) bool {
	if to, ok := APIAliasPath(path); ok {
		path = to
	}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		if _, pattern := s.mux.Handler(&http.Request{Method: m, URL: &url.URL{Path: path}}); pattern != "/" {
			return true
		}
	}
	return false
}
