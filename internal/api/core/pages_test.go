package core

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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
		{"GET", "/images/increaseSort copy.svg", file},
		{"GET", "/images/plots_na.psd", notFound},
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
	if !strings.Contains(page, fontAwesome4+"</head>") {
		t.Error("Font Awesome 4.7 is not the head's last stylesheet, as in the Java portal's page")
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
// Java portal's page on the portal's routes and Spring's 404 elsewhere.
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

// TestJavaPortalPage checks the page served without a local frontend build:
// the Java portal's index.html, loading the frontend from frontend.url (or
// this origin) with the configuration embedded and escaped.
func TestJavaPortalPage(t *testing.T) {
	s := &State{}
	s.PageConfig = func(*http.Request) map[string]any {
		return map[string]any{"frontendUrl": "https://frontend.example.org/", "app_version": "v1", "skin_title": "</script>"}
	}
	w := httptest.NewRecorder()
	s.CatchAll(w, httptest.NewRequest("GET", "/study/summary?id=x", nil))
	page := w.Body.String()
	for _, want := range []string{
		`<html class="cbioportal-frontend" lang="en">`,
		`window.frontendConfig.frontendUrl = "https://frontend.example.org/";`,
		`appVersion: "v1",`,
		`<script src="/js/load-frontend.js?v1"></script>`,
		`loadAppStyles(window.frontendConfig);`,
		`loadReactApp(window.frontendConfig);`,
		`"skin_title":"` + escapedEnd + `"`,
		fontAwesome4 + "\n</head>",
		`<div id="reactRoot"></div>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(page, "googletagmanager") {
		t.Error("Google Tag Manager without google_tag_manager_id")
	}

	// Without frontend.url the page loads the frontend from this origin;
	// Google Tag Manager is added when its id is set.
	s.PageConfig = func(*http.Request) map[string]any { return map[string]any{"google_tag_manager_id": "GTM-X"} }
	w = httptest.NewRecorder()
	s.CatchAll(w, httptest.NewRequest("GET", "/", nil))
	page = w.Body.String()
	if !strings.Contains(page, `} else if ("") {`) || !strings.Contains(page, `window.frontendConfig.frontendUrl = "//" + host + "/";`) {
		t.Errorf("frontend URL fallback missing:\n%s", page)
	}
	if !strings.Contains(page, `'dataLayer',"GTM-X")`) || !strings.Contains(page, `ns.html?id=GTM-X"`) {
		t.Error("Google Tag Manager missing")
	}

	// frontend.url wins over a local build, as the property alone decides
	// in Java; the build's files stay served.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><head></head><body>local build</body></html>"), 0o644)
	built := frontendState(dir)
	built.Props = map[string]string{"frontend.url": "https://frontend.example.org/"}
	built.PageConfig = func(*http.Request) map[string]any {
		return map[string]any{"frontendUrl": "https://frontend.example.org/"}
	}
	w = httptest.NewRecorder()
	built.CatchAll(w, httptest.NewRequest("GET", "/", nil))
	if b := w.Body.String(); strings.Contains(b, "local build") || !strings.Contains(b, `frontendUrl = "https://frontend.example.org/"`) {
		t.Errorf("frontend.url with a local build:\n%s", b)
	}

	// A form POST is answered with the page and the posted parameters.
	w = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/results", strings.NewReader("gene_list=TP53"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.CatchAll(w, r)
	if !strings.Contains(w.Body.String(), `<head><script>var postData = {"gene_list":"TP53"};</script>`) {
		t.Errorf("posted page:\n%s", w.Body.String())
	}
}

// The build's scripts and stylesheet load from the netlify deploy preview
// that localStorage.netlify names, else from this origin.
func TestWithNetlifyBundle(t *testing.T) {
	page := string(withNetlifyBundle([]byte(`<head><script defer src="/reactapp/main.app.js"></script>` +
		`<link href="/reactapp/styles.css" rel="stylesheet"></head>`)))
	for _, want := range []string{
		`localStorage.netlify ? "//" + localStorage.netlify + ".netlify.app/" : "/"`,
		`'reactapp/main.app.js"></scr' + 'ipt>'`,
		`'reactapp/styles.css" rel="stylesheet">'`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q in %s", want, page)
		}
	}
	if strings.Contains(page, `src="/reactapp/`) || strings.Contains(page, `href="/reactapp/`) {
		t.Errorf("a bundle file still loads from this origin only: %s", page)
	}
}

// TestPortalLocaldev runs the portal page's configuration script in node:
// localdev is "true" (port 3000) or a 4-digit port, from the URL or
// localStorage, and other values are neither stored nor dev mode.
func TestPortalLocaldev(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	page := string(javaPortalPage("h", nil))
	start := strings.Index(page, "<script>\nvar host")
	end := strings.Index(page[start:], "</script>")
	if start < 0 || end < 0 {
		t.Fatalf("no configuration script:\n%s", page)
	}
	script := page[start+len("<script>") : start+end]
	for _, c := range []struct{ href, stored, want string }{
		{"https://h/?localdev=true", "", `"true" true https://localhost:3000/`},
		{"https://h/?localdev=3001", "", `"3001" true https://localhost:3001/`},
		{"https://h/", "4000", `"4000" true https://localhost:4000/`},
		{"https://h/?localdev=30011", "", `undefined false //h/`},
		{"https://h/?localdev=yes", "", `undefined false //h/`},
		{"https://h/", "", `undefined false //h/`},
	} {
		stored := "{}"
		if c.stored != "" {
			stored = `{localdev: "` + c.stored + `"}`
		}
		prog := `var localStorage = ` + stored + `; localStorage.setItem = function (k, v) { this[k] = String(v); };
var window = {location: {href: "` + c.href + `"}};
` + script + `
console.log(JSON.stringify(localStorage.localdev), window.localdev, window.frontendConfig.frontendUrl);`
		out, err := exec.Command(node, "-e", prog).CombinedOutput()
		if got := strings.TrimSpace(string(out)); err != nil || got != c.want {
			t.Errorf("%s (stored %q): got %q (%v), want %q", c.href, c.stored, got, err, c.want)
		}
	}
}
