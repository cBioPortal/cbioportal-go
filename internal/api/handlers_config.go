package api

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/config"
)

//go:embed portal.html
var portalHTML []byte

// configService returns the full frontend property set (as the Java
// FrontendPropertiesService does), so feature-gating flags like
// skin_show_settings_menu drive the UI correctly. Runtime bits (auth, session)
// are layered on top of the embedded defaults.
func (s *State) configService(w http.ResponseWriter, r *http.Request) {
	cfg := config.FrontendConfig("/", nil)
	cfg["authenticationMethod"] = "noauthsessionservice"
	cfg["sessionServiceEnabled"] = true
	cfg["clickhouse_mode"] = true
	if u, ok := GetUser(r.Context()); ok && u.Name != "" {
		cfg["user_display_name"] = u.Name
		cfg["user_email_address"] = u.Name
	}
	writeJSON(w, cfg)
}

// customDriverAnnotationReport reports whether the given mutation profiles carry
// custom driver annotations (binary cbp_driver and/or tier classes), so the
// settings menu exposes the corresponding filter controls. Values come from the
// mutation_extended staging columns cbp_driver / cbp_driver_tiers.
func (s *State) customDriverAnnotationReport(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	type row struct {
		HasBinary uint8    `json:"hasBinary"`
		Tiers     []string `json:"tiers"`
	}
	report := map[string]any{"hasBinary": false, "tiers": []string{}}
	if len(ids) == 0 {
		writeJSON(w, report)
		return
	}
	rows, _ := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT toUInt8(countIf(driver_filter IN ('Putative_Driver', 'Putative_Passenger')) > 0) AS hasBinary, "+
			"arraySort(groupUniqArrayIf(driver_tiers_filter, driver_tiers_filter NOT IN ('', 'NA'))) AS tiers "+
			"FROM genomic_event_derived WHERE genetic_profile_stable_id IN (%s)", inList(ids)))
	if len(rows) > 0 {
		report["hasBinary"] = rows[0].HasBinary == 1
		if len(rows[0].Tiers) > 0 {
			report["tiers"] = rows[0].Tiers
		}
	}
	writeJSON(w, report)
}

func (s *State) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"portalVersion": "go-0.1.0", "gitBranch": "master", "gitCommitId": "", "dbVersion": "clickhouse",
	})
}

func (s *State) portal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(portalHTML)
}

// catchAll serves the portal at "/" and drains-and-empties any other unmatched
// path (Genome Nexus /proxy, features not yet implemented).
func (s *State) catchAll(w http.ResponseWriter, r *http.Request) {
	if s.frontend != nil && !strings.HasPrefix(r.URL.Path, "/api/") {
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
	readBody(r, nil)
	writeJSON(w, []any{})
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
	rec := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
	s.frontend.ServeHTTP(rec, get)
	data, _ := json.Marshal(params)
	script := "<script>var postData = " + string(data) + ";</script>"
	page := rec.body.String()
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
	w.WriteHeader(rec.status)
	w.Write([]byte(page))
}
