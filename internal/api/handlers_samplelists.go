package api

import (
	"net/http"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

type sampleListRow struct {
	SampleListID string   `json:"sampleListId"`
	Category     string   `json:"category"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	StudyID      string   `json:"studyId"`
	SampleIDs    []string `json:"sampleIds"`
}

// SampleList is the legacy SampleList model; sampleCount and sampleIds belong
// to the DETAILED projection (the others are cut by javaContract).
type SampleList struct {
	SampleListID string   `json:"sampleListId"`
	Category     string   `json:"category"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	StudyID      string   `json:"studyId"`
	SampleCount  int      `json:"sampleCount"`
	SampleIDs    []string `json:"sampleIds"`
}

const sampleListSelect = "SELECT list_id AS sampleListId, any(category) AS category, any(name) AS name, " +
	"any(description) AS description, study_id AS studyId, groupArray(sample_id) AS sampleIds " +
	"FROM sample_list"

func toSampleList(r sampleListRow) SampleList {
	return SampleList{
		SampleListID: r.SampleListID, Category: r.Category, Name: r.Name, Description: r.Description,
		StudyID: r.StudyID, SampleCount: len(r.SampleIDs), SampleIDs: r.SampleIDs,
	}
}

// sampleListsQuery writes the sample lists matching where, whose placeholders
// take their values from p.
func (s *State) sampleListsQuery(w http.ResponseWriter, where string, p ch.Params) {
	sql := sampleListSelect
	if where != "" {
		sql += " WHERE " + where
	}
	sql += " GROUP BY study_id, list_id ORDER BY list_id"
	rows, err := ch.QueryP[sampleListRow](s.CH, sql, p)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]SampleList, len(rows))
	for i, r := range rows {
		out[i] = toSampleList(r)
	}
	writeJSON(w, out)
}

func (s *State) studySampleLists(w http.ResponseWriter, r *http.Request) {
	s.sampleListsQuery(w, "study_id = {study:String}", ch.Params{"study": r.PathValue("studyId")})
}

// sampleList serves GET /api/sample-lists/{sampleListId}: the DETAILED list.
func (s *State) sampleList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rows, err := ch.QueryP[sampleListRow](s.CH, sampleListSelect+" WHERE list_id = {list:String} GROUP BY study_id, list_id",
		ch.Params{"list": id})
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusNotFound, msgSampleListNotFound(id))
		return
	}
	writeJSON(w, toSampleList(rows[0]))
}

func (s *State) sampleListsFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	if len(ids) == 0 {
		writeJSON(w, []SampleList{})
		return
	}
	s.sampleListsQuery(w, "list_id IN {ids:Array(String)}", ch.Params{"ids": ids})
}
