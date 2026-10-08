package portal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// robots.txt points to /llms.txt only when llms_txt.location is set.
func TestRobotsLlmsTxtPointer(t *testing.T) {
	const pointer = "# Guidance for AI agents and bulk data access: http://portal.example/llms.txt\n\n"
	for _, location := range []string{"", "file:/cbioportal/llms.txt"} {
		s := New(&core.State{Props: map[string]string{"sitemaps": "true", core.LLMsTxtProperty: location}})
		rec := httptest.NewRecorder()
		s.robots(rec, httptest.NewRequest(http.MethodGet, "http://portal.example/robots.txt", nil))
		body := rec.Body.String()
		if location == "" && strings.Contains(body, "llms.txt") {
			t.Errorf("unset: %q", body)
		}
		if location != "" && !strings.HasPrefix(body, pointer+"User-agent: *\n") {
			t.Errorf("set: %q", body)
		}
	}
}

// robots.disallow_all turns robots.txt into a disallow-all and hides the
// sitemaps, with or without the sitemaps flag.
func TestRobotsDisallowAll(t *testing.T) {
	for _, sitemaps := range []string{"", "true"} {
		s := New(&core.State{Props: map[string]string{"sitemaps": sitemaps, robotsDisallowAll: "true"}})
		rec := httptest.NewRecorder()
		s.robots(rec, httptest.NewRequest(http.MethodGet, "http://portal.example/robots.txt", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "User-agent: *\nDisallow: /\n" {
			t.Errorf("sitemaps=%q: robots %d %q", sitemaps, rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		s.sitemapIndex(rec, httptest.NewRequest(http.MethodGet, "http://portal.example/sitemap_index.xml", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("sitemaps=%q: sitemap index %d", sitemaps, rec.Code)
		}
	}
}
