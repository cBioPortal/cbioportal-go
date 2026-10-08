package core

import (
	"net/http"
	"strings"
)

// springSecurityHeaders are the headers Spring Security's default header
// writers add to every response, unless the response already carries them.
var springSecurityHeaders = [][2]string{
	{"X-Content-Type-Options", "nosniff"},
	{"X-XSS-Protection", "0"},
	{"X-Frame-Options", "DENY"},
}

// hstsValue is Spring Security's default Strict-Transport-Security header,
// spacing included.
const hstsValue = "max-age=31536000 ; includeSubDomains"

// SecurityHeaders adds the response headers Spring Security writes by
// default, as the Java portal sends them with every security configuration:
// X-Content-Type-Options, X-XSS-Protection and X-Frame-Options always;
// Strict-Transport-Security on secure requests (TLS, or X-Forwarded-Proto
// https from the ingress); and Cache-Control, Pragma and Expires against
// caching unless the handler set any of the three, or answered 304.
// The headers are written when the response is committed, so handlers can
// still set their own.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &securityHeaderWriter{ResponseWriter: w, hsts: strings.HasPrefix(BaseURL(r), "https:")}
		next.ServeHTTP(sw, r)
		sw.commit(http.StatusOK)
	})
}

type securityHeaderWriter struct {
	http.ResponseWriter
	hsts      bool
	committed bool
}

func (w *securityHeaderWriter) commit(status int) {
	if w.committed {
		return
	}
	w.committed = true
	h := w.Header()
	for _, kv := range springSecurityHeaders {
		if _, ok := h[kv[0]]; !ok {
			h.Set(kv[0], kv[1])
		}
	}
	if _, ok := h["Strict-Transport-Security"]; w.hsts && !ok {
		h.Set("Strict-Transport-Security", hstsValue)
	}
	_, cc := h["Cache-Control"]
	_, ex := h["Expires"]
	_, pr := h["Pragma"]
	if !cc && !ex && !pr && status != http.StatusNotModified {
		h.Set("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
		h.Set("Pragma", "no-cache")
		h.Set("Expires", "0")
	}
}

func (w *securityHeaderWriter) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.commit(code)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *securityHeaderWriter) Write(p []byte) (int, error) {
	w.commit(http.StatusOK)
	return w.ResponseWriter.Write(p)
}

func (w *securityHeaderWriter) Flush() {
	w.commit(http.StatusOK)
	http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *securityHeaderWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
