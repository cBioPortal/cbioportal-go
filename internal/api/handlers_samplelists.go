package api

import (
	"net/http"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
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

// sampleListSelect selects the lists of sample_list (to be grouped by study
// and list), each list's samples in SampleListMapper's order: by internal id
// (the storage order of sample_list_list) where the database carries them.
func (s *State) sampleListSelect() string {
	ids := "groupArray(sample_id)"
	if s.importOrdered() {
		ids = "arrayMap(x -> x.2, arraySort(groupArray((internal_id, sample_id))))"
	}
	return "SELECT list_id AS sampleListId, any(category) AS category, any(name) AS name, " +
		"any(description) AS description, study_id AS studyId, " + ids + " AS sampleIds FROM sample_list"
}

func toSampleList(r sampleListRow) SampleList {
	return SampleList{
		SampleListID: r.SampleListID, Category: r.Category, Name: r.Name, Description: r.Description,
		StudyID: r.StudyID, SampleCount: len(r.SampleIDs), SampleIDs: r.SampleIDs,
	}
}

// sampleListsQuery writes the sample lists matching where, whose placeholders
// take their values from p, by stable id or, with byInternalID, in the order
// of their internal ids (SampleListMapper.getSampleLists has no ORDER BY)
// where the database carries them.
// The lists stream as they are read; a sorted page is read as sqlPageOrder
// orders it.
func (s *State) sampleListsQuery(w http.ResponseWriter, r *http.Request, where string, p ch.Params, byInternalID bool) {
	sql := s.sampleListSelect()
	if where != "" {
		sql += " WHERE " + where
	}
	order := "list_id"
	if byInternalID && s.modelHasColumn("sample_list", "list_internal_id") {
		order = "any(list_internal_id), list_id"
	}
	// Small blocks: a list carries up to every sample of its study.
	sql += " GROUP BY study_id, list_id" + sqlPageOrder(r, sampleListSortable, order) + " SETTINGS max_block_size = 64"
	out := newJSONArray(w)
	err := ch.QueryEachP(s.CH, sql, p, func(row *sampleListRow) error {
		return out.add(toSampleList(*row))
	})
	out.finish(err)
}

// sampleListSortable are the SampleListSortBy columns (stableId, category,
// cancerStudyIdentifier, name, description) as sampleListSelect names them.
var sampleListSortable = map[string]string{
	"sampleListId": "list_id", "category": "category", "studyId": "study_id", "name": "name", "description": "description",
}

func (s *State) studySampleLists(w http.ResponseWriter, r *http.Request) {
	s.sampleListsQuery(w, r, "study_id = {study:String}", ch.Params{"study": r.PathValue("studyId")}, false)
}

// sampleList serves GET /api/sample-lists/{sampleListId}: the DETAILED list.
func (s *State) sampleList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rows, err := ch.QueryP[sampleListRow](s.CH, s.sampleListSelect()+" WHERE list_id = {list:String} GROUP BY study_id, list_id",
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
	s.sampleListsQuery(w, r, "list_id IN {ids:Array(String)}", ch.Params{"ids": ids}, true)
}
