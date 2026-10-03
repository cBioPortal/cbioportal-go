package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"runtime/debug"
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
	cfg := config.FrontendConfig("/", s.Props, s.PropsReplaceDefaults)
	cfg["authenticationMethod"] = "noauthsessionservice"
	if s.login != nil {
		cfg["authenticationMethod"] = "oauth2"
	}
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
// mutation, CNA and SV events of genomic_event_derived.
func (s *State) customDriverAnnotationReport(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	type row struct {
		HasBinary uint8    `json:"hasBinary"`
		Tiers     []string `json:"tiers"`
	}
	report := map[string]any{"hasBinary": false, "tiers": []string{}}
	// An empty list reports nothing, as the Java backend answers it.
	if len(ids) == 0 {
		writeJSON(w, report)
		return
	}
	rows, _ := ch.QueryP[row](s.CH,
		"SELECT toUInt8(countIf(driver_filter IN ('Putative_Driver', 'Putative_Passenger')) > 0) AS hasBinary, "+
			"arraySort(groupUniqArrayIf(driver_tiers_filter, driver_tiers_filter NOT IN ('', 'NA'))) AS tiers "+
			"FROM genomic_event_derived WHERE genetic_profile_stable_id IN {ids:Array(String)}", ch.Params{"ids": ids})
	if len(rows) > 0 {
		report["hasBinary"] = rows[0].HasBinary == 1
		if len(rows[0].Tiers) > 0 {
			report["tiers"] = rows[0].Tiers
		}
	}
	writeJSON(w, report)
}

// Info is InfoController's response. Versions name what this backend serves:
// the cBioPortal schema / derived-table versions its views reproduce and the
// gene / gene-set tables of the seed database the reference is loaded from.
// Git properties the build does not record are "not set", as in Java.
type Info struct {
	PortalVersion             string `json:"portalVersion"`
	DbVersion                 string `json:"dbVersion"`
	DerivedTableVersion       string `json:"derivedTableVersion"`
	GenesetVersion            string `json:"genesetVersion"`
	GeneTableVersion          string `json:"geneTableVersion"`
	GitBranch                 string `json:"gitBranch"`
	GitCommitID               string `json:"gitCommitId"`
	GitCommitIDDescribe       string `json:"gitCommitIdDescribe"`
	GitCommitIDDescribeShort  string `json:"gitCommitIdDescribeShort"`
	GitCommitMessageFull      string `json:"gitCommitMessageFull"`
	GitCommitMessageShort     string `json:"gitCommitMessageShort"`
	GitCommitMessageUserEmail string `json:"gitCommitMessageUserEmail"`
	GitCommitMessageUserName  string `json:"gitCommitMessageUserName"`
	GitDirty                  bool   `json:"gitDirty"`
}

const (
	portalVersion       = "go-0.1.0"
	dbSchemaVersion     = "2.14.5"
	derivedTableVersion = "1.0.11"
	seedGenesetVersion  = "msigdb_v2025.1.Hs"
	seedGeneTable       = "hgnc_v7_2025.10.7"
	gitNotSet           = "not set"
)

func (s *State) info(w http.ResponseWriter, r *http.Request) {
	info := Info{
		PortalVersion: portalVersion, DbVersion: dbSchemaVersion, DerivedTableVersion: derivedTableVersion,
		GenesetVersion: seedGenesetVersion, GeneTableVersion: seedGeneTable,
		GitBranch: gitNotSet, GitCommitID: gitNotSet, GitCommitIDDescribe: gitNotSet, GitCommitIDDescribeShort: gitNotSet,
		GitCommitMessageFull: gitNotSet, GitCommitMessageShort: gitNotSet,
		GitCommitMessageUserEmail: gitNotSet, GitCommitMessageUserName: gitNotSet,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				info.GitCommitID = kv.Value
				info.GitCommitIDDescribe = kv.Value[:min(7, len(kv.Value))]
				info.GitCommitIDDescribeShort = info.GitCommitIDDescribe
			case "vcs.modified":
				info.GitDirty = kv.Value == "true"
			}
		}
		if info.GitDirty && info.GitCommitID != gitNotSet {
			info.GitCommitIDDescribe += "-dirty"
			info.GitCommitIDDescribeShort = info.GitCommitIDDescribe
		}
	}
	writeJSON(w, info)
}

func (s *State) portal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(portalHTML)
}

// catchAll serves every path no route matches. An /api/ path gets Spring's
// default error body, as Java answers it: 405 when the path is served for
// other methods, else 404. Other paths go to the frontend, or, without one,
// the portal at "/" and an empty list elsewhere.
func (s *State) catchAll(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		// Java matches a trailing slash to the route without it
		// (WebAppConfig setUseTrailingSlashMatch), e.g. /api/gene-panels/.
		if trimmed := strings.TrimSuffix(r.URL.Path, "/"); trimmed != r.URL.Path && s.mux != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path, r2.URL.RawPath = trimmed, ""
			if h, pattern := s.mux.Handler(r2); pattern != "/" {
				h.ServeHTTP(w, r2)
				return
			}
		}
		readBody(r, nil)
		if allow := s.allowedMethods(r); len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			springError(w, r, http.StatusMethodNotAllowed)
			return
		}
		springNotFound(w, r)
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
	readBody(r, nil)
	writeJSON(w, []any{})
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
