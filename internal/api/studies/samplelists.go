package studies

import (
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

type sampleListRow struct {
	SampleListID string   `json:"sampleListId"`
	StudyID      string   `json:"studyId"`
	Category     string   `json:"category"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SampleIDs    []string `json:"sampleIds"`
}

// SampleList is the legacy SampleList model; sampleCount and sampleIds belong
// to the DETAILED projection (the others are cut by javaContract).
type SampleList struct {
	SampleListID string   `json:"sampleListId"`
	StudyID      string   `json:"studyId"`
	Category     string   `json:"category"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SampleCount  int      `json:"sampleCount"`
	SampleIDs    []string `json:"sampleIds"`
}

// sampleListSelect selects the lists of sample_list in the scope (to be
// grouped by study and list), each list's samples in SampleListMapper's
// order: by internal id (the storage order of sample_list_list) where the
// database carries them.
func (s Server) sampleListSelect(b core.Binder, sc core.Scope) string {
	ids := "groupArray(sample_id)"
	if s.ImportOrdered() {
		ids = "arrayMap(x -> x.2, arraySort(groupArray((internal_id, sample_id))))"
	}
	return "SELECT list_id AS sampleListId, any(category) AS category, any(name) AS name, " +
		"any(description) AS description, study_id AS studyId, " + ids + " AS sampleIds FROM " + sc.From(b, core.TSampleList)
}

func toSampleList(r sampleListRow) SampleList {
	return SampleList{
		SampleListID: r.SampleListID, Category: r.Category, Name: r.Name, Description: r.Description,
		StudyID: r.StudyID, SampleCount: len(r.SampleIDs), SampleIDs: r.SampleIDs,
	}
}

// sampleListsQuery writes the sample lists in the scope, by stable id or,
// with byInternalID, in the order of their internal ids
// (SampleListMapper.getSampleLists has no ORDER BY) where the database
// carries them.
// The lists stream as they are read; a sorted page is read as sqlPageOrder
// orders it.
func (s Server) sampleListsQuery(w http.ResponseWriter, r *http.Request, sc core.Scope, byInternalID bool) {
	p := ch.Params{}
	sql := s.sampleListSelect(core.ParamsBinder(p), sc)
	order := "list_id"
	if byInternalID && s.ModelHasColumn("sample_list", "list_internal_id") {
		order = "any(list_internal_id), list_id"
	}
	// Small blocks: a list carries up to every sample of its study.
	sql += " GROUP BY study_id, list_id" + core.SQLPageOrder(r, sampleListSortable, order) + " SETTINGS max_block_size = 64"
	out := core.NewJSONArray(w, r)
	err := ch.QueryEachP(s.CH, sql, p, func(row *sampleListRow) error {
		return out.Add(toSampleList(*row))
	})
	out.Finish(err)
}

// sampleListSortable are the SampleListSortBy columns (stableId, category,
// cancerStudyIdentifier, name, description) as sampleListSelect names them.
var sampleListSortable = map[string]string{
	"sampleListId": "list_id", "category": "category", "studyId": "study_id", "name": "name", "description": "description",
}

func (s Server) StudySampleLists(w http.ResponseWriter, r *http.Request) {
	s.sampleListsQuery(w, r, core.ByStudy(r.PathValue("studyId")), false)
}

// SampleList serves GET /api/sample-lists/{sampleListId}: the DETAILED list.
func (s Server) SampleList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := ch.Params{}
	rows, err := ch.QueryP[sampleListRow](s.CH, s.sampleListSelect(core.ParamsBinder(p), s.BySampleList(id))+
		" GROUP BY study_id, list_id", p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		core.WriteError(w, http.StatusNotFound, core.MsgSampleListNotFound(id))
		return
	}
	core.WriteJSON(w, toSampleList(rows[0]))
}

func (s Server) SampleListsFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	core.ReadBody(r, &ids)
	if len(ids) == 0 {
		core.WriteJSON(w, []SampleList{})
		return
	}
	s.sampleListsQuery(w, r, s.BySampleLists(ids...), true)
}
