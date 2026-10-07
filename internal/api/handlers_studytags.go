package api

import (
	"encoding/json"
	"net/http"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// routeStudyTags registers the study-tags endpoints (StudyController), served
// from the cancer_study_tags view (the study's tags_file YAML stored as JSON).
func (s *State) routeStudyTags(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/studies/{studyId}/tags", s.studyTagsGet)
	mux.HandleFunc("POST /api/studies/tags/fetch", s.studyTagsFetch)
}

// CancerStudyTags is the CancerStudyTags model: tags is the JSON document as a
// string. cancerStudyId is the study's internal id, absent where the database
// carries none.
type CancerStudyTags struct {
	CancerStudyID *uint64 `json:"cancerStudyId,omitempty"`
	StudyID       string  `json:"studyId"`
	Tags          string  `json:"tags"`
}

type studyTagsRow struct {
	StudyID string `json:"studyId"`
	Tags    string `json:"tags"`
}

// studyTagsGet — GET /api/studies/{studyId}/tags: the tags as a JSON object,
// or {} when the study has none.
func (s *State) studyTagsGet(w http.ResponseWriter, r *http.Request) {
	rows, err := queryView[studyTagsRow](s.CH,
		"SELECT study_id AS studyId, tags FROM cancer_study_tags WHERE study_id = {study:String} LIMIT 1",
		ch.Params{"study": r.PathValue("studyId")})
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(rows) == 0 || !json.Valid([]byte(rows[0].Tags)) {
		writeJSON(w, map[string]any{})
		return
	}
	writeJSON(w, json.RawMessage(rows[0].Tags))
}

// studyTagsFetch — POST /api/studies/tags/fetch (body: study ids; an empty
// list returns every study's tags). Studies without tags are absent.
func (s *State) studyTagsFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	where := ""
	p := ch.Params{}
	if len(ids) > 0 {
		where = " WHERE study_id IN {ids:Array(String)}"
		p["ids"] = ids
	}
	rows, err := queryView[studyTagsRow](s.CH,
		"SELECT study_id AS studyId, tags FROM cancer_study_tags"+where+" ORDER BY study_id", p)
	if err != nil {
		fail(w, r, err)
		return
	}
	ranks := s.studyRanks()
	out := make([]CancerStudyTags, len(rows))
	for i, row := range rows {
		out[i] = CancerStudyTags{StudyID: row.StudyID, Tags: row.Tags}
		if id, ok := ranks[row.StudyID]; ok {
			out[i].CancerStudyID = &id
		}
	}
	writeJSON(w, out)
}
