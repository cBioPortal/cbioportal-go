package api

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"

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
	writeJSON(w, cfg)
}

// customDriverAnnotationReport reports whether the given mutation profiles carry
// custom driver annotations (binary cbp_driver and/or tier classes), so the
// settings menu exposes the corresponding filter controls. Values come from the
// mutation_extended staging columns cbp_driver / cbp_driver_tiers.
func (s *State) customDriverAnnotationReport(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	studySet := map[string]bool{}
	for _, id := range ids {
		if m, ok := s.profileMeta(id); ok && m.StudyID != "" {
			studySet[m.StudyID] = true
		} else if st := molecularProfileStudy(id); st != "" {
			studySet[st] = true
		}
	}
	report := map[string]any{"hasBinary": false, "tiers": []string{}}
	if len(studySet) == 0 {
		writeJSON(w, report)
		return
	}
	studies := make([]string, 0, len(studySet))
	for st := range studySet {
		studies = append(studies, st)
	}

	type binRow struct {
		N uint64 `json:"n"`
	}
	bin, _ := ch.Query[binRow](s.CH, fmt.Sprintf(
		"SELECT count() AS n FROM cbioportal_raw.mutation_extended "+
			"WHERE study_id IN (%s) AND cbp_driver NOT IN ('', 'NA', 'Unknown')", inList(studies)))
	if len(bin) > 0 && bin[0].N > 0 {
		report["hasBinary"] = true
	}

	type tierRow struct {
		Tier string `json:"tier"`
	}
	tiers, _ := ch.Query[tierRow](s.CH, fmt.Sprintf(
		"SELECT DISTINCT cbp_driver_tiers AS tier FROM cbioportal_raw.mutation_extended "+
			"WHERE study_id IN (%s) AND cbp_driver_tiers NOT IN ('', 'NA') ORDER BY tier", inList(studies)))
	out := make([]string, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, t.Tier)
	}
	if len(out) > 0 {
		report["tiers"] = out
	}
	writeJSON(w, report)
}

func (s *State) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"portalVersion": "go-0.1.0", "gitBranch": "master", "gitCommitId": "", "dbVersion": "clickhouse",
	})
}

// sessionCreate stores the posted payload and returns its id.
func (s *State) sessionCreate(w http.ResponseWriter, r *http.Request) {
	var data json.RawMessage
	readBody(r, &data)
	n := s.counter.Add(1)
	id := fmt.Sprintf("%016x%08x", fnv1a(r.PathValue("type"), data), uint32(n))
	s.sessions.Store(id, data)
	writeJSON(w, map[string]any{"id": id})
}

func (s *State) sessionGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, ok := s.sessions.Load(id)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"id": id, "source": r.PathValue("source"), "type": r.PathValue("type"), "data": v,
	})
}

func fnv1a(t string, data []byte) uint64 {
	h := uint64(1469598103934665603)
	for i := 0; i < len(t); i++ {
		h ^= uint64(t[i])
		h *= 1099511628211
	}
	for _, b := range data {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return h
}

func (s *State) portal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(portalHTML)
}

// catchAll serves the portal at "/" and drains-and-empties any other unmatched
// path (Genome Nexus /proxy, features not yet implemented).
func (s *State) catchAll(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		s.portal(w, r)
		return
	}
	readBody(r, nil)
	writeJSON(w, []any{})
}
