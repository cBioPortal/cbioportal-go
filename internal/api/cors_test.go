package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/auth"
	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// loginRouter is the router of a private portal (authenticate=oauth2). The
// provider is never reached: every request here is answered before the login
// sends a browser to it.
func loginRouter(t *testing.T) http.Handler {
	t.Helper()
	l, err := auth.Configure(map[string]string{
		"authenticate": "oauth2",
		"spring.security.oauth2.client.registration.keycloak.client-id": "portal",
		"spring.security.oauth2.client.provider.keycloak.issuer-uri":    "https://provider.example.org/realms/cbio",
	}, false)
	if err != nil || l == nil {
		t.Fatalf("Configure: %v %v", l, err)
	}
	s := newState(&core.State{})
	s.auth = l
	return NewRouter(s)
}

// TestCORSPreflightSkipsLogin pins that CORS runs in front of the login, so a
// preflight request to the API is answered instead of being sent to
// authentication, while a real request from the same origin still needs a
// login. Java needs ApiSecurityConfig to ask for CORS on the /api chain for
// this (cbioportal#12516).
func TestCORSPreflightSkipsLogin(t *testing.T) {
	h := loginRouter(t)
	const origin = "https://frontend.example.org"

	req := httptest.NewRequest(http.MethodOptions, "/api/studies", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight: %d, want %d", rec.Code, http.StatusNoContent)
	}
	for header, want := range map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "GET, POST, DELETE, OPTIONS",
		"Access-Control-Allow-Headers": "*",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("preflight %s = %q, want %q", header, got, want)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/studies", nil)
	req.Header.Set("Origin", origin)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET from an allowed origin without a login: %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("GET Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
}
