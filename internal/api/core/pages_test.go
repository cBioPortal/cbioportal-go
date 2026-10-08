package core

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPortalRoutes checks the paths outside /api/ against the Java portal's
// answers (www.cbioportal.org): WebAppConfig's view-controller routes and
// IndexPageController's paths serve the page, its redirects redirect, its
// static resources are served, and everything else is Spring's 404.
func TestPortalRoutes(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"index.html":           "<html><head></head><body><div id=\"reactRoot\"></div></body></html>",
		"reactapp/main.app.js": "console.log(1)",
		"images/logo.png":      "\x89PNG",
		"common/nothing.json":  "{}",
	} {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	s := frontendState(dir)
	s.PageConfig = func(*http.Request) map[string]any { return map[string]any{"skin_title": "</script>x"} }
	h := http.HandlerFunc(s.CatchAll)

	const page, notFound, redirect, file, notAllowed = "page", "404", "302", "file", "405"
	cases := []struct{ method, path, want string }{
		{"GET", "/", page},
		{"GET", "/index", page},
		{"GET", "/index/", page},
		{"GET", "/index.html", page},
		{"GET", "/index.html/", page},
		{"GET", "/study/summary", page},
		{"GET", "/study/summary/", page},
		{"GET", "/results", page},
		{"GET", "/results/", page},
		{"GET", "/results/oncoprint", page},
		{"GET", "/results/comparison/overlap", page},
		{"GET", "/results/comparison/", page},
		{"GET", "/results/pathways/x", page},
		{"GET", "/results/x/y", notFound},
		{"GET", "/resultsfoo", page},
		{"GET", "/resultsfoo/", notFound},
		{"GET", "/Results", notFound},
		{"GET", "/patient", page},
		{"GET", "/patient/", page},
		{"GET", "/patient/summary", page},
		{"GET", "/patient/x/", notFound},
		{"GET", "/patient x", page},
		{"GET", "/study", page},
		{"GET", "/study/", page},
		{"GET", "/study/x", page},
		{"GET", "/study/summary/x", notFound},
		{"GET", "/comparison", page},
		{"GET", "/comparison/overlap", page},
		{"GET", "/comparison/overlap/x", notFound},
		{"GET", "/mutation_mapper", page},
		{"GET", "/mutation_mapper/x", page},
		{"GET", "/mutation_mapper/x/", notFound},
		{"GET", "/loading", notFound},
		{"GET", "/loading/", page},
		{"GET", "/loading/comparison", page},
		{"GET", "/restore", page},
		{"GET", "/restore/", notFound},
		{"GET", "/encodedRedirect", page},
		{"GET", "/encodedRedirect/", notFound},
		{"GET", "/index.do", page},
		{"GET", "/index.do/", page},
		{"GET", "/index.do/x", page},
		{"GET", "/case.do", notFound},
		{"GET", "/case.do/", page},
		{"GET", "/case.do/x/y", notFound},
		{"GET", "/oncoprinter", page},
		{"GET", "/oncoprinter/", notFound},
		{"GET", "/datasets", page},
		{"GET", "/datasetsfoo", page},
		{"GET", "/ln", page},
		{"GET", "/lnx", page},
		{"GET", "/ln/", notFound},
		{"GET", "/webAPI", page},
		{"GET", "/webAPI/", notFound},
		{"GET", "/news", page},
		{"GET", "/visualize", page},
		{"GET", "/visualize/x", notFound},
		{"GET", "/oncotree2genes", page},
		{"HEAD", "/patient/x", page},
		{"POST", "/patient/x", notAllowed},
		{"PUT", "/study", notAllowed},
		{"DELETE", "/index", page},
		{"PUT", "/results/", page},
		{"GET", "/installations", redirect},
		{"HEAD", "/tutorials", redirect},
		{"GET", "/oql", redirect},
		{"GET", "/donate", redirect},
		{"GET", "/installations/", notFound},
		{"POST", "/installations", notAllowed},
		{"GET", "/about", notFound},
		{"GET", "/faq", notFound},
		{"GET", "/tutorials/", notFound},
		{"GET", "/foo/bar", notFound},
		{"GET", "/favicon.ico", notFound},
		{"GET", "/login.jsp", notFound},
		{"GET", "/swagger-ui.html", notFound},
		{"GET", "/common/nothing.json", notFound},
		{"GET", "/images/cbioportal_logo.png", file},
		{"GET", "/images/dashboard/pie.svg", file},
		{"GET", "/images/Sorting icons.psd", file},
		{"GET", "/images/logo.png", file},
		{"HEAD", "/images/cbioportal_logo.png", file},
		{"POST", "/images/cbioportal_logo.png", notAllowed},
		{"POST", "/images/nope.png", notFound},
		{"GET", "/images/nope.png", notFound},
		{"GET", "/images/", notFound},
		{"GET", "/images", notFound},
		{"GET", "/js/load-frontend.js", file},
		{"POST", "/js/load-frontend.js", notAllowed},
		{"GET", "/js/", notFound},
		{"GET", "/reactapp/main.app.js", file},
		{"POST", "/reactapp/main.app.js", notAllowed},
		{"GET", "/reactapp/", notFound},
		{"GET", "/reactapp", notFound},
		{"GET", "/reactapp/index.html", notFound},
		{"POST", "/api", notFound},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "/", nil)
		r.URL, r.RequestURI = &url.URL{Path: c.path}, (&url.URL{Path: c.path}).EscapedPath()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		body := w.Body.String()
		var ok bool
		switch c.want {
		case page:
			ok = w.Code == 200 && strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") &&
				(c.method == "HEAD" || strings.Contains(body, "window.rawServerConfig"))
		case file:
			ok = w.Code == 200 && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") &&
				(c.method == "HEAD" || len(body) > 0)
		case redirect:
			ok = w.Code == 302 && strings.HasPrefix(w.Header().Get("Location"), "https://") && body == ""
		case notFound:
			ok = w.Code == 404 && strings.Contains(body, `"status":404,"error":"Not Found","path":"`+c.path+`"`)
		case notAllowed:
			ok = w.Code == 405 && w.Header().Get("Allow") == "GET, HEAD" && strings.Contains(body, `"status":405`)
		}
		if !ok {
			t.Errorf("%s %s: %d %q %.80q, want %s", c.method, c.path, w.Code, w.Header().Get("Content-Type"), body, c.want)
		}
	}

	r := httptest.NewRequest("GET", "/oql", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("Location"); got != "https://docs.cbioportal.org/user-guide/oql/" {
		t.Errorf("/oql Location %q", got)
	}

	r = httptest.NewRequest("GET", "/about", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "Error 400: Invalid Request") {
		t.Errorf("browser 404: %d %q", w.Code, w.Body.String())
	}

	r = httptest.NewRequest("POST", "/results", strings.NewReader("cancer_study_list=acc_tcga&gene_list=TP53"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `var postData = {"cancer_study_list":"acc_tcga","gene_list":"TP53"}`) {
		t.Errorf("posted query: %d %q", w.Code, w.Body.String())
	}
}

// escapedEnd is "</script>" as json.Marshal writes it in a string.
var escapedEnd = strings.NewReplacer("<", `\`+"u003c", ">", `\`+"u003e").Replace("</script>")

// TestRawServerConfig checks the page embeds the configuration as
// window.rawServerConfig, escaped so that it cannot end the script element.
func TestRawServerConfig(t *testing.T) {
	page := string(withFrontendConfig([]byte("<html><head></head><body></body></html>"), "example.org",
		[]byte(`{"skin_footer":"`+escapedEnd+`"}`)))
	if !strings.Contains(page, `window.rawServerConfig = {"skin_footer":"`+escapedEnd+`"};`) {
		t.Errorf("page %q", page)
	}
	if strings.Contains(string(withFrontendConfig([]byte("<html><head></head></html>"), "example.org", nil)), "rawServerConfig") {
		t.Error("rawServerConfig without a configuration")
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><head></head><body></body></html>"), 0o644)
	s := frontendState(dir)
	s.PageConfig = func(*http.Request) map[string]any { return map[string]any{"skin_title": "</script>"} }
	w := httptest.NewRecorder()
	s.CatchAll(w, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(w.Body.String(), `"</script>`) || !strings.Contains(w.Body.String(), `"skin_title":"`+escapedEnd+`"`) {
		t.Errorf("page %q", w.Body.String())
	}
}

// TestPortalWithoutFrontend checks a server without a frontend serves its
// built-in page on the portal's routes and Spring's 404 elsewhere.
func TestPortalWithoutFrontend(t *testing.T) {
	s := &State{}
	for path, want := range map[string]int{"/": 200, "/patient": 200, "/foo": 404, "/images/cbioportal_logo.png": 200, "/reactapp/main.app.js": 404} {
		w := httptest.NewRecorder()
		s.CatchAll(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
	}
}
