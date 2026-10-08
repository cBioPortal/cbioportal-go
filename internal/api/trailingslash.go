package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// trailingSlash serves an /api/ path ending in one slash as the path without
// it, as Java's request mappings match it (WebAppConfig
// setUseTrailingSlashMatch), for every method: GET /api/studies/x/ is GET
// /api/studies/x, POST /api/studies/x/ the 405 of that route. It applies
// only when the path as given has no route and the trimmed one has, so
// routes of their own such as /api/ keep their answer and a path parameter
// never matches an empty segment. Error bodies keep the requested path, as
// Spring's do.
//
// Java's InvolvedCancerStudyExtractorInterceptor matches the exact path, so
// on a legacy endpoint whose filter it parses (interceptedBodyPaths) a
// trailing slash leaves the controller without its filter: such requests
// answer as legacyUnintercepted, once authentication and authorization have
// seen them (uninterceptedFetch).
func (s *State) trailingSlash(next http.Handler) http.Handler {
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
		if r.Method == http.MethodPost {
			if rest, ok := interceptorPath(trimmed); ok && interceptedBodyPaths[rest] {
				r2 = r2.WithContext(context.WithValue(r2.Context(), uninterceptedKey{}, rest))
			}
		}
		next.ServeHTTP(w, r2)
	})
}

// uninterceptedKey marks a request trailingSlash leaves to
// legacyUnintercepted; its value is the interceptor's path.
type uninterceptedKey struct{}

// uninterceptedFetch answers the requests trailingSlash marked as
// legacyUnintercepted.
func uninterceptedFetch(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := r.Context().Value(uninterceptedKey{}).(string); ok {
			legacyUnintercepted(w, r, rest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// routed reports whether any method has a route for path, its /api/legacy/
// or /api/not-ready-yet/ alias resolved.
func (s *State) routed(path string) bool {
	if to, ok := apiAliasPath(path); ok {
		path = to
	}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		if _, pattern := s.mux.Handler(&http.Request{Method: m, URL: &url.URL{Path: path}}); pattern != "/" {
			return true
		}
	}
	return false
}

// interceptorPath is path as InvolvedCancerStudyExtractorInterceptor compares
// it: without the /api and /legacy prefixes.
func interceptorPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/api")
	if !ok {
		return "", false
	}
	return strings.TrimPrefix(rest, "/legacy"), true
}

// legacyUnintercepted answers a POST to a legacy intercepted endpoint
// (relative path rest) that the interceptor did not parse: a body that is
// not JSON of the filter's shape fails as Spring's body conversion does;
// otherwise /generic_assay_data/fetch rejects the missing filter with an
// empty 400 and every other controller fails with a 500 on it.
func legacyUnintercepted(w http.ResponseWriter, r *http.Request, rest string) {
	raw, _ := io.ReadAll(r.Body)
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			writeError(w, http.StatusBadRequest, msgBadJSON)
			return
		}
		_, isList := v.([]any)
		_, isObject := v.(map[string]any)
		wantList := rest == "/copy-number-segments/fetch"
		if v != nil && (wantList && !isList || !wantList && !isObject) {
			writeError(w, http.StatusBadRequest, msgBadJSON)
			return
		}
	}
	if rest == "/generic_assay_data/fetch" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	springError(w, r, http.StatusInternalServerError)
}
