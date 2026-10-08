package core

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObserve(t *testing.T) {
	s := &State{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/studies/{studyId}", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, map[string]string{"studyId": r.PathValue("studyId")})
	})
	mux.HandleFunc("GET /api/private", func(w http.ResponseWriter, r *http.Request) {
		r = WithUser(r, User{Name: "alice@example.org"})
		u, _ := GetUser(r.Context())
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(u.Name))
	})
	s.mux = mux

	var logBuf bytes.Buffer
	h := s.observeWith(mux, slog.New(slog.NewJSONHandler(&logBuf, nil)))

	for _, p := range []string{"/api/studies/acc_tcga", "/api/studies/brca_tcga", "/api/private", "/nope"} {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	w := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := w.Body.String()
	for _, want := range []string{
		`cbioportal_http_requests_total{route="GET /api/studies/{studyId}",code="200"} 2`,
		`cbioportal_http_requests_total{route="GET /api/private",code="403"} 1`,
		`cbioportal_http_requests_total{route="unmatched",code="404"} 1`,
		`cbioportal_http_request_duration_seconds_count{route="GET /api/studies/{studyId}"} 2`,
		"cbioportal_http_requests_in_flight 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q", want)
		}
	}

	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("%d access log lines, want 4:\n%s", len(lines), logBuf.String())
	}
	var private map[string]any
	json.Unmarshal([]byte(lines[2]), &private)
	if private["route"] != "GET /api/private" || private["status"] != float64(403) ||
		private["user"] != "alice@example.org" || private["remote"] != "203.0.113.9" {
		t.Errorf("access log line %v", private)
	}
	var first map[string]any
	json.Unmarshal([]byte(lines[0]), &first)
	if _, ok := first["user"]; ok {
		t.Errorf("anonymous request logged a user: %v", first)
	}
}
