package contract

import (
	"strings"
	"testing"
)

// Contracts for the paths outside /api/ (WebAppConfig, IndexPageController)
// and for Spring Security's default response headers, as
// www.cbioportal.org answers them.

func TestContractPortalRoutes(t *testing.T) {
	requireAPI(t)
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/", 200}, {"GET", "/index.html", 200}, {"GET", "/results/", 200},
		{"GET", "/results/oncoprint", 200}, {"GET", "/study/summary", 200}, {"GET", "/patient", 200},
		{"GET", "/comparison/overlap", 200}, {"GET", "/restore", 200}, {"GET", "/loading/comparison", 200},
		{"GET", "/webAPI", 200}, {"GET", "/datasets", 200}, {"GET", "/encodedRedirect", 200},
		{"GET", "/mutation_mapper", 200}, {"GET", "/oncoprinter", 200}, {"GET", "/case.do/x", 200},
		{"POST", "/patient/x", 405}, {"POST", "/installations", 405},
		{"GET", "/about", 404}, {"GET", "/foo/bar", 404}, {"GET", "/favicon.ico", 404},
		{"GET", "/patient/x/y", 404}, {"GET", "/restore/", 404}, {"GET", "/images/nope.png", 404},
		{"GET", "/js/load-frontend.js", 200}, {"GET", "/images/cbioportal_logo.png", 200},
		{"POST", "/images/cbioportal_logo.png", 405},
	} {
		resp, body := rawTarget(t, c.method, c.path, nil)
		if resp.StatusCode != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, resp.StatusCode, c.status)
			continue
		}
		switch c.status {
		case 200:
			if strings.HasPrefix(c.path, "/images/") || strings.HasPrefix(c.path, "/js/") {
				break
			}
			if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
				t.Errorf("%s %s: Content-Type %q, want the portal page", c.method, c.path, resp.Header.Get("Content-Type"))
			}
		case 404, 405:
			if resp.Header.Get("Content-Type") != "application/json" ||
				!strings.Contains(string(body), `"path":"`+c.path+`"`) {
				t.Errorf("%s %s: %s %s, want Spring's error body", c.method, c.path, resp.Header.Get("Content-Type"), body)
			}
			if c.status == 405 && resp.Header.Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s: Allow %q", c.method, c.path, resp.Header.Get("Allow"))
			}
		}
	}
	for path, target := range map[string]string{
		"/installations": "https://installationmap.netlify.app/",
		"/tutorials":     "https://docs.cbioportal.org/user-guide/overview/#tutorial-slides",
		"/oql":           "https://docs.cbioportal.org/user-guide/oql/",
		"/donate":        "https://docs.cbioportal.org/donate/",
	} {
		resp, _ := rawTarget(t, "GET", path, nil)
		if resp.StatusCode != 302 || resp.Header.Get("Location") != target {
			t.Errorf("GET %s: %d Location %q, want 302 to %s", path, resp.StatusCode, resp.Header.Get("Location"), target)
		}
	}
}

func TestContractSecurityHeaders(t *testing.T) {
	requireAPI(t)
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Xss-Protection":       "0",
		"X-Frame-Options":        "DENY",
		"Cache-Control":          "no-cache, no-store, max-age=0, must-revalidate",
		"Pragma":                 "no-cache",
		"Expires":                "0",
	}
	for _, path := range []string{"/api/studies", "/config_service", "/", "/foo", "/installations", "/images/cbioportal_logo.png"} {
		resp, _ := rawTarget(t, "GET", path, nil)
		for k, v := range want {
			if got := resp.Header.Get(k); got != v {
				t.Errorf("GET %s: %s = %q, want %q", path, k, got, v)
			}
		}
		if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != "" {
			t.Errorf("GET %s over HTTP: Strict-Transport-Security %q", path, hsts)
		}
		resp, _ = rawTarget(t, "GET", path, map[string]string{"X-Forwarded-Proto": "https"})
		if got := resp.Header.Get("Strict-Transport-Security"); got != "max-age=31536000 ; includeSubDomains" {
			t.Errorf("GET %s behind HTTPS: Strict-Transport-Security %q", path, got)
		}
	}
	// The firewall rejects a request before Spring Security's headers.
	resp, _ := rawTarget(t, "GET", "/api/genes//TP53", nil)
	if resp.Header.Get("X-Frame-Options") != "" {
		t.Error("firewall rejection carries the security headers")
	}
}
