package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIAliasPath(t *testing.T) {
	cases := map[string]string{
		"/api/legacy/studies":                                 "/api/studies",
		"/api/legacy/studies/s1/samples":                      "/api/studies/s1/samples",
		"/api/legacy/generic_assay_meta/fetch":                "/api/generic-assay-meta/fetch",
		"/api/legacy/molecular-profiles/co-expressions/fetch": "/api/molecular-profiles/co-expressions/fetch",
		"/api/not-ready-yet/mutations/fetch":                  "/api/mutations/fetch",
		"/api/not-ready-yet/clinical-data/fetch":              "/api/clinical-data/fetch",
	}
	for in, want := range cases {
		got, ok := APIAliasPath(in)
		if !ok || got != want {
			t.Errorf("apiAliasPath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"/api/studies", "/api/legacyx/studies", "/legacy/studies"} {
		if got, ok := APIAliasPath(in); ok {
			t.Errorf("apiAliasPath(%q) = %q; want no alias", in, got)
		}
	}
}

func TestIsCacheStatsPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/cacheStatistics":                          true,
		"/api/GeneralRepositoryCache/keysInCache":       true,
		"/api/GeneralRepositoryCache/keyCountsPerClass": true,
		"/api/keysInCache":                              false,
		"/api//keysInCache":                             false,
		"/api/studies/s1/keysInCache":                   false,
		"/api/studies":                                  false,
	} {
		if got := IsCacheStatsPath(path); got != want {
			t.Errorf("isCacheStatsPath(%q) = %v; want %v", path, got, want)
		}
	}
}

func TestExtraEndpointsRewritesAliases(t *testing.T) {
	var seen string
	h := ExtraEndpoints(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery
	}))
	for in, want := range map[string]string{
		"POST /api/legacy/generic_assay_meta/fetch?projection=ID": "POST /api/generic-assay-meta/fetch?projection=ID",
		"POST /api/not-ready-yet/mutations/fetch":                 "POST /api/mutations/fetch?",
		"GET /api/legacy/studies?keyword=x":                       "GET /api/studies?keyword=x",
		"GET /api/studies/s1":                                     "GET /api/studies/s1?",
	} {
		var method, target string
		for i := range in {
			if in[i] == ' ' {
				method, target = in[:i], in[i+1:]
				break
			}
		}
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, target, nil))
		if seen != want {
			t.Errorf("%s: handler saw %q; want %q", in, seen, want)
		}
	}
}

func TestExtraEndpointsCacheStatsNotFound(t *testing.T) {
	h := ExtraEndpoints(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("next handler reached for %s", r.URL.Path)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/GeneralRepositoryCache/keysInCache", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d; want 404", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != float64(404) || body["error"] != "Not Found" ||
		body["path"] != "/api/GeneralRepositoryCache/keysInCache" || body["timestamp"] == nil {
		t.Errorf("unexpected body %v", body)
	}
}

func TestExtraEndpointsStudiesMetaXTotalCount(t *testing.T) {
	h := ExtraEndpoints(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("total-count", "7")
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/studies?projection=META", nil))
	if got := rec.Header().Get("X-Total-Count"); got != "7" {
		t.Errorf("X-Total-Count = %q; want 7", got)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/studies/s1/samples?projection=META", nil))
	if got := rec.Header().Get("X-Total-Count"); got != "" {
		t.Errorf("X-Total-Count = %q on a non-studies endpoint; want unset", got)
	}
}
