package core

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
)

//go:embed portal.html
var portalHTML []byte

func (s *State) portal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(portalHTML)
}

// CatchAll serves every path no route matches. An /api/ path gets Spring's
// default error body, as Java answers it: 405 when the path is served for
// other methods, else 404. Other paths go to the frontend, or, without one,
// the portal at "/" and an empty list elsewhere.
func (s *State) CatchAll(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		ReadBody(r, nil)
		if allow := s.allowedMethods(r); len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			SpringError(w, r, http.StatusMethodNotAllowed)
			return
		}
		SpringNotFound(w, r)
		return
	}
	if s.frontend != nil {
		if r.Method == http.MethodPost && portalPages[r.URL.Path] {
			s.postedPage(w, r)
			return
		}
		s.frontend.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		s.portal(w, r)
		return
	}
	ReadBody(r, nil)
	WriteJSON(w, []any{})
}

// allowedMethods lists the methods a route serves r's path for.
func (s *State) allowedMethods(r *http.Request) []string {
	var out []string
	if s.mux == nil {
		return out
	}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, pattern := s.mux.Handler(probe); pattern != "/" {
			out = append(out, m)
		}
	}
	return out
}

// portalPages are the pages that accept a posted query (IndexPageController).
var portalPages = map[string]bool{"/": true, "/index": true, "/index.html": true, "/study/summary": true, "/results": true}

// postedPage serves the portal page for a form POST with the posted parameters
// exposed as window.postData, for queries too long for a URL.
func (s *State) postedPage(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	params := map[string]string{}
	for k, v := range r.Form {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}
	get := r.Clone(r.Context())
	get.Method = http.MethodGet
	get.Body = http.NoBody
	get.ContentLength = 0
	get.Header.Del("Content-Type")
	get.Header.Del("Accept-Encoding")
	rec := NewBufferedResponse()
	s.frontend.ServeHTTP(rec, get)
	data, _ := json.Marshal(params)
	script := "<script>var postData = " + string(data) + ";</script>"
	page := rec.Body.String()
	if i := strings.Index(page, "<head>"); i >= 0 {
		page = page[:i+len("<head>")] + script + page[i+len("<head>"):]
	} else {
		page = script + page
	}
	for k, v := range rec.header {
		if k != "Content-Length" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(rec.Status)
	w.Write([]byte(page))
}
