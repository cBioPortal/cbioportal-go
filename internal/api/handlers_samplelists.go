package api

import (
	"fmt"
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

func (s *State) sampleListsQuery(w http.ResponseWriter, where string) {
	sql := sampleListSelect
	if where != "" {
		sql += " WHERE " + where
	}
	sql += " GROUP BY study_id, list_id ORDER BY list_id"
	rows, err := ch.Query[sampleListRow](s.CH, sql)
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
	s.sampleListsQuery(w, fmt.Sprintf("study_id = '%s'", sqlLit(r.PathValue("studyId"))))
}

// sampleList serves GET /api/sample-lists/{sampleListId}: the DETAILED list.
func (s *State) sampleList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sql := fmt.Sprintf("%s WHERE list_id = '%s' GROUP BY study_id, list_id", sampleListSelect, sqlLit(id))
	rows, err := ch.Query[sampleListRow](s.CH, sql)
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
	s.sampleListsQuery(w, fmt.Sprintf("list_id IN (%s)", inList(ids)))
}
