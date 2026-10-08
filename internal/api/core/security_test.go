package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSecurityHeaders checks Spring Security's default headers as
// www.cbioportal.org sends them.
func TestSecurityHeaders(t *testing.T) {
	handler := func(f func(w http.ResponseWriter)) http.Handler {
		return SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f(w) }))
	}
	serve := func(h http.Handler, hdr map[string]string) http.Header {
		r := httptest.NewRequest("GET", "/api/studies", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Result().Header
	}
	always := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Xss-Protection":       "0",
		"X-Frame-Options":        "DENY",
	}
	noStore := map[string]string{
		"Cache-Control": "no-cache, no-store, max-age=0, must-revalidate",
		"Pragma":        "no-cache",
		"Expires":       "0",
	}
	check := func(name string, got http.Header, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if g := got.Get(k); g != v {
				t.Errorf("%s: %s = %q, want %q", name, k, g, v)
			}
		}
	}

	written := handler(func(w http.ResponseWriter) { w.Write([]byte("[]")) })
	h := serve(written, nil)
	check("plain", h, always)
	check("plain", h, noStore)
	if h.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS on a plain HTTP request")
	}
	h = serve(written, map[string]string{"X-Forwarded-Proto": "https"})
	check("https", h, map[string]string{"Strict-Transport-Security": "max-age=31536000 ; includeSubDomains"})

	check("no body", serve(handler(func(w http.ResponseWriter) {}), nil), noStore)
	check("status only", serve(handler(func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }), nil), always)

	h = serve(handler(func(w http.ResponseWriter) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write([]byte("x"))
	}), nil)
	if h.Get("Cache-Control") != "public, max-age=31536000, immutable" || h.Get("Pragma") != "" || h.Get("Expires") != "" {
		t.Errorf("handler's Cache-Control overridden: %v", h)
	}
	h = serve(handler(func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotModified) }), nil)
	if h.Get("Cache-Control") != "" {
		t.Errorf("304 got Cache-Control %q", h.Get("Cache-Control"))
	}
	h = serve(handler(func(w http.ResponseWriter) {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Write([]byte("x"))
	}), nil)
	if h.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("handler's X-Frame-Options overridden: %q", h.Get("X-Frame-Options"))
	}
}
