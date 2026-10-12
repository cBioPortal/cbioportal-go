package core

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// webapp holds the Java portal's own static resources, served as its
// WebAppConfig.addResourceHandlers does: /images/** from
// src/main/resources/webapp/images and /js/** from src/main/resources/js.
//
//go:embed all:webapp
var webapp embed.FS

// javaRedirects are WebAppConfig's redirect view controllers.
var javaRedirects = map[string]string{
	"/installations": "https://installationmap.netlify.app/",
	"/tutorials":     "https://docs.cbioportal.org/user-guide/overview/#tutorial-slides",
	"/oql":           "https://docs.cbioportal.org/user-guide/oql/",
	"/donate":        "https://docs.cbioportal.org/donate/",
}

// portalPages are IndexPageController's paths, served for every method and,
// with Spring's trailing-slash matching, with a trailing slash too.
var portalPages = map[string]bool{
	"/": true, "/index": true, "/index.html": true, "/study/summary": true, "/results": true,
	"/index/": true, "/index.html/": true, "/study/summary/": true, "/results/": true,
}

// The view-controller routes WebAppConfig forwards to the portal page, for
// GET and HEAD, by pattern shape:
var (
	// "/x": exact paths.
	viewExact = map[string]bool{
		"/study": true, "/mutation_mapper": true, "/comparison": true, "/restore": true, "/encodedRedirect": true,
	}
	// "/x**": one segment starting with x ("/results", "/resultsfoo").
	viewSegmentPrefixes = []string{
		"results", "patient", "index.do", "oncoprinter", "datasets", "ln", "webAPI", "news", "visualize", "oncotree2genes",
	}
	// "/x/*": x and one more segment, possibly empty ("/patient/", "/study/summary").
	viewOneLevel = map[string]bool{
		"results": true, "patient": true, "study": true, "mutation_mapper": true, "index.do": true,
		"case.do": true, "loading": true, "comparison": true,
	}
	// "/x/y/*".
	viewTwoLevel = map[[2]string]bool{{"results", "comparison"}: true, {"results", "pathways"}: true}
)

// isViewRoute reports whether p matches one of WebAppConfig's view-controller
// patterns (Spring's PathPattern semantics: case-sensitive, matched on the
// decoded segments, no trailing-slash matching).
func isViewRoute(p string) bool {
	if viewExact[p] {
		return true
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	switch len(segs) {
	case 1:
		for _, pre := range viewSegmentPrefixes {
			if strings.HasPrefix(segs[0], pre) {
				return true
			}
		}
	case 2:
		return viewOneLevel[segs[0]]
	case 3:
		return viewTwoLevel[[2]string{segs[0], segs[1]}]
	}
	return false
}

// getOrHead answers 405 with Spring's error body unless r is a GET or HEAD,
// the methods Spring's view controllers and resource handlers serve.
func getOrHead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	ReadBody(r, nil)
	w.Header().Set("Allow", "GET, HEAD")
	SpringError(w, r, http.StatusMethodNotAllowed)
	return false
}

// servePortal routes the paths outside /api/ as the Java portal does: its
// redirects, the portal page on IndexPageController's paths and
// WebAppConfig's view-controller routes, its static resources (and the
// frontend build's), and Spring's 404 for everything else. A frontend dev
// server (FRONTEND_URL) gets the pages and the paths nothing else serves.
func (s *State) servePortal(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if target, ok := javaRedirects[p]; ok {
		if getOrHead(w, r) {
			w.Header().Set("Location", target)
			w.WriteHeader(http.StatusFound)
		}
		return
	}
	if portalPages[p] {
		if r.Method == http.MethodPost {
			s.postedPage(w, r)
			return
		}
		s.page(w, r)
		return
	}
	if isViewRoute(p) {
		if getOrHead(w, r) {
			s.page(w, r)
		}
		return
	}
	if s.serveStatic(w, r) {
		return
	}
	if s.frontendProxy != nil {
		s.frontendProxy.ServeHTTP(w, r)
		return
	}
	ReadBody(r, nil)
	SpringNotFound(w, r)
}

// page serves the portal page: the dev server's; the Java portal's page,
// which loads the frontend from the frontend.url property, as Java does
// whenever that property is set (portal); the local frontend build's index
// page; or, without any of them, the Java portal's page loading the frontend
// from this origin.
func (s *State) page(w http.ResponseWriter, r *http.Request) {
	switch {
	case s.frontendProxy != nil:
		s.frontendProxy.ServeHTTP(w, r)
	case s.frontendDir != "" && strings.TrimSpace(s.Props["frontend.url"]) == "":
		s.indexPage(w, r)
	default:
		ReadBody(r, nil)
		s.portal(w, r)
	}
}

// serveStatic serves a static resource if r names one, and reports whether
// it did: /js/** from the Java portal's resources, /images/** from them or
// else the frontend build, /reactapp/** from the frontend build. A resource
// requested with another method than GET or HEAD gets a 405.
func (s *State) serveStatic(w http.ResponseWriter, r *http.Request) bool {
	p := path.Clean(r.URL.Path)
	if p != r.URL.Path && p+"/" != r.URL.Path {
		return false
	}
	switch {
	case strings.HasPrefix(p, "/js/"), strings.HasPrefix(p, "/images/"):
		name := "webapp" + p
		if st, err := fs.Stat(webapp, name); err == nil && st.Mode().IsRegular() && !strings.HasSuffix(r.URL.Path, "/") {
			if getOrHead(w, r) {
				http.ServeFileFS(w, r, webapp, name)
			}
			return true
		}
		if strings.HasPrefix(p, "/js/") {
			return false
		}
	case strings.HasPrefix(p, "/reactapp/"):
	default:
		return false
	}
	if s.frontendDir == "" || strings.HasSuffix(r.URL.Path, "/") {
		return false
	}
	file := filepath.Join(s.frontendDir, filepath.FromSlash(p))
	st, err := os.Stat(file)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	if getOrHead(w, r) {
		s.frontendFiles.serve(w, r, file, st)
	}
	return true
}
