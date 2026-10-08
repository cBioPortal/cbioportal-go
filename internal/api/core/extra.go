package core

import (
	"context"
	"net/http"
	"strings"
)

// apiAliasPrefixes are the path prefixes under which the Java backend serves
// the same endpoints as /api/ (the legacy controllers and the column-store
// controllers still marked "not ready yet").
var apiAliasPrefixes = []string{"/api/legacy/", "/api/not-ready-yet/"}

// apiAliasRenames maps alias paths whose /api/ counterpart is spelled
// differently.
var apiAliasRenames = map[string]string{
	"/api/legacy/generic_assay_meta/fetch": "/api/generic-assay-meta/fetch",
}

// ExtraEndpoints serves the Java endpoints that sit outside the mux's route
// table: the /api/legacy/ and /api/not-ready-yet/ aliases, GET
// /api/studies/meta, the X-Total-Count header of the column-store studies
// endpoint, and the cache-statistics endpoints (absent in Java unless a cache
// is configured). studiesMeta serves GET /api/studies/meta.
func ExtraEndpoints(studiesMeta http.HandlerFunc, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if to, ok := APIAliasPath(path); ok {
			r2 := r.Clone(context.WithValue(r.Context(), aliasKey{}, path))
			r2.URL.Path = to
			r2.URL.RawPath = ""
			next.ServeHTTP(w, r2)
			return
		}
		if r.Method == http.MethodGet {
			switch {
			case path == "/api/studies/meta":
				studiesMeta(w, r)
				return
			case path == "/api/studies" && strings.EqualFold(r.URL.Query().Get("projection"), "META"):
				next.ServeHTTP(&xTotalCountWriter{ResponseWriter: w}, r)
				return
			case IsCacheStatsPath(path):
				SpringNotFound(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type aliasKey struct{}

// aliasOf returns the alias path (/api/legacy/X, /api/not-ready-yet/X) the
// request came in on, or "" for a request to its /api/ path.
func aliasOf(r *http.Request) string {
	p, _ := r.Context().Value(aliasKey{}).(string)
	return p
}

// APIAliasPath maps an alias path (/api/legacy/X, /api/not-ready-yet/X) to the
// /api/X path that serves it.
func APIAliasPath(path string) (string, bool) {
	if to, ok := apiAliasRenames[path]; ok {
		return to, true
	}
	for _, p := range apiAliasPrefixes {
		if rest, ok := strings.CutPrefix(path, p); ok {
			return "/api/" + rest, true
		}
	}
	return "", false
}

// xTotalCountWriter mirrors the total-count header into X-Total-Count, which
// the column-store studies endpoint sets alongside it for projection=META.
type xTotalCountWriter struct {
	http.ResponseWriter
	wrote bool
}

func (x *xTotalCountWriter) WriteHeader(code int) {
	if !x.wrote {
		x.wrote = true
		h := x.Header()
		if n := h.Get("total-count"); n != "" && h.Get("X-Total-Count") == "" {
			h.Set("X-Total-Count", n)
		}
	}
	x.ResponseWriter.WriteHeader(code)
}

func (x *xTotalCountWriter) Write(p []byte) (int, error) {
	if !x.wrote {
		x.WriteHeader(http.StatusOK)
	}
	return x.ResponseWriter.Write(p)
}

// IsCacheStatsPath matches CacheStatsController's routes: /api/cacheStatistics,
// /api/{cache}/keysInCache and /api/{cache}/keyCountsPerClass.
func IsCacheStatsPath(path string) bool {
	if path == "/api/cacheStatistics" {
		return true
	}
	rest, ok := strings.CutPrefix(path, "/api/")
	if !ok {
		return false
	}
	seg := strings.Split(rest, "/")
	return len(seg) == 2 && seg[0] != "" && (seg[1] == "keysInCache" || seg[1] == "keyCountsPerClass")
}

// SpringNotFound answers a path no Java controller serves with Spring Boot's
// default 404 error body. The cache-statistics routes get it too, as Java
// with persistence.cache_type=no-cache does not register CacheStatsController.
func SpringNotFound(w http.ResponseWriter, r *http.Request) {
	SpringError(w, r, http.StatusNotFound)
}
