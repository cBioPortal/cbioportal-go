package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeResources registers the resource-definition and resource-data
// endpoints (ResourceDefinitionController / ResourceDataController), served
// from the resource_definition and resource_{study,patient,sample} views.
func (s *State) routeResources(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/studies/{studyId}/resource-definitions", s.resourceDefinitionsInStudy)
	mux.HandleFunc("GET /api/studies/{studyId}/resource-definitions/{resourceId}", s.resourceDefinitionInStudy)
	mux.HandleFunc("POST /api/resource-definitions/fetch", s.resourceDefinitionsFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/resource-data", s.studyResourceData)
	mux.HandleFunc("GET /api/studies/{studyId}/resource-data-all", s.allResourceDataInStudy)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}/resource-data", s.patientResourceData)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}/resource-data", s.sampleResourceData)
}

// ResourceDefinition is the ResourceDefinition model as serialized through
// ResourceDefinitionMixin (cancerStudyIdentifier -> studyId), with nulls
// omitted. The ID projection carries only resourceId, displayName and studyId.
type ResourceDefinition struct {
	ResourceID     string  `json:"resourceId"`
	DisplayName    string  `json:"displayName"`
	Description    *string `json:"description,omitempty"`
	ResourceType   *string `json:"resourceType,omitempty"`
	Priority       *string `json:"priority,omitempty"`
	OpenByDefault  *bool   `json:"openByDefault,omitempty"`
	StudyID        string  `json:"studyId"`
	CustomMetaData *string `json:"customMetaData,omitempty"`
}

// ResourceData is the ResourceData model (UniqueKeyBase keys are set only for
// the ids present, as UniqueKeyInterceptor does).
type ResourceData struct {
	UniqueSampleKey    string              `json:"uniqueSampleKey,omitempty"`
	UniquePatientKey   string              `json:"uniquePatientKey,omitempty"`
	SampleID           string              `json:"sampleId,omitempty"`
	PatientID          string              `json:"patientId,omitempty"`
	StudyID            string              `json:"studyId"`
	ResourceID         string              `json:"resourceId"`
	URL                *string             `json:"url,omitempty"`
	ResourceDefinition *ResourceDefinition `json:"resourceDefinition,omitempty"`
}

type resourceDefinitionRow struct {
	StudyID        string  `json:"study_id"`
	ResourceID     string  `json:"resource_id"`
	DisplayName    string  `json:"display_name"`
	Description    string  `json:"description"`
	ResourceType   string  `json:"resource_type"`
	OpenByDefault  uint8   `json:"open_by_default"`
	Priority       int64   `json:"priority"`
	CustomMetadata *string `json:"custom_metadata"`
}

const resourceDefinitionCols = "study_id, resource_id, display_name, description, resource_type, open_by_default, priority, custom_metadata"

func (r resourceDefinitionRow) model(projection string) ResourceDefinition {
	d := ResourceDefinition{ResourceID: r.ResourceID, DisplayName: r.DisplayName, StudyID: r.StudyID}
	if projection == "ID" {
		return d
	}
	desc, typ, prio, open := r.Description, r.ResourceType, strconv.FormatInt(r.Priority, 10), r.OpenByDefault != 0
	d.Description, d.ResourceType, d.Priority, d.OpenByDefault = &desc, &typ, &prio, &open
	d.CustomMetaData = r.CustomMetadata
	return d
}

// projectionParam returns the upper-cased `projection` query param (default
// SUMMARY).
func projectionParam(r *http.Request) string {
	if p := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("projection"))); p != "" {
		return p
	}
	return "SUMMARY"
}

// pagingClause builds `ORDER BY … LIMIT … OFFSET …` from the pageSize /
// pageNumber / sortBy / direction query params. sortCols maps the API sortBy
// names to SQL expressions; an unknown or absent sortBy uses defaultOrder.
func pagingClause(r *http.Request, sortCols map[string]string, defaultOrder string) string {
	q := r.URL.Query()
	pageSize := 10000000
	if v, err := strconv.Atoi(q.Get("pageSize")); err == nil && v > 0 {
		pageSize = v
	}
	pageNumber := 0
	if v, err := strconv.Atoi(q.Get("pageNumber")); err == nil && v >= 0 {
		pageNumber = v
	}
	order := defaultOrder
	if col, ok := sortCols[q.Get("sortBy")]; ok {
		dir := "ASC"
		if strings.EqualFold(q.Get("direction"), "DESC") {
			dir = "DESC"
		}
		order = col + " " + dir + ", " + defaultOrder
	}
	return fmt.Sprintf(" ORDER BY %s LIMIT %d OFFSET %d", order, pageSize, pageNumber*pageSize)
}

// queryView runs a query against one of the study side-data views, treating a
// view that does not exist yet (a target DB transformed before these views
// were added) as empty.
func queryView[T any](c *ch.Client, sql string) ([]T, error) {
	rows, err := ch.Query[T](c, sql)
	if err != nil && strings.Contains(err.Error(), "UNKNOWN_TABLE") {
		return []T{}, nil
	}
	return rows, err
}

// existsIn reports whether a SELECT count() query returns a non-zero count.
func (s *State) existsIn(sql string) (bool, error) {
	rows, err := cachedQuery[struct {
		N uint64 `json:"n"`
	}](s, sql)
	if err != nil {
		return false, err
	}
	return len(rows) > 0 && rows[0].N > 0, nil
}

// requireStudy writes a 404 and returns false when the study does not exist.
func (s *State) requireStudy(w http.ResponseWriter, studyID string) bool {
	ok, err := s.existsIn(fmt.Sprintf("SELECT count() AS n FROM cancer_study WHERE study_id = '%s'", sqlLit(studyID)))
	if err != nil {
		fail(w, err)
		return false
	}
	if !ok {
		http.Error(w, "study not found: "+studyID, http.StatusNotFound)
		return false
	}
	return true
}

var resourceDefinitionSort = map[string]string{
	"resourceId": "resource_id", "displayName": "display_name", "description": "description",
	"resourceType": "resource_type", "priority": "priority", "openByDefault": "open_by_default",
	"studyId": "study_id", "customMetaData": "custom_metadata",
}

func (s *State) writeResourceDefinitions(w http.ResponseWriter, where, tail, projection string) {
	rows, err := queryView[resourceDefinitionRow](s.CH,
		"SELECT "+resourceDefinitionCols+" FROM resource_definition WHERE "+where+tail)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]ResourceDefinition, len(rows))
	for i, r := range rows {
		out[i] = r.model(projection)
	}
	writeJSON(w, out)
}

// resourceDefinitionsInStudy — GET /api/studies/{studyId}/resource-definitions.
func (s *State) resourceDefinitionsInStudy(w http.ResponseWriter, r *http.Request) {
	studyID := r.PathValue("studyId")
	if !s.requireStudy(w, studyID) {
		return
	}
	s.writeResourceDefinitions(w, fmt.Sprintf("study_id = '%s'", sqlLit(studyID)),
		pagingClause(r, resourceDefinitionSort, "resource_id ASC"), projectionParam(r))
}

// resourceDefinitionInStudy — GET /api/studies/{studyId}/resource-definitions/{resourceId}
// (DETAILED projection; 404 when the study or definition is unknown).
func (s *State) resourceDefinitionInStudy(w http.ResponseWriter, r *http.Request) {
	studyID, resourceID := r.PathValue("studyId"), r.PathValue("resourceId")
	if !s.requireStudy(w, studyID) {
		return
	}
	rows, err := queryView[resourceDefinitionRow](s.CH, fmt.Sprintf(
		"SELECT %s FROM resource_definition WHERE study_id = '%s' AND resource_id = '%s' LIMIT 1",
		resourceDefinitionCols, sqlLit(studyID), sqlLit(resourceID)))
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, fmt.Sprintf("resource definition not found: %s in %s", resourceID, studyID), http.StatusNotFound)
		return
	}
	writeJSON(w, rows[0].model("DETAILED"))
}

// resourceDefinitionsFetch — POST /api/resource-definitions/fetch (body: study ids).
func (s *State) resourceDefinitionsFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	if len(ids) == 0 {
		http.Error(w, "studyIds: size must be between 1 and 10000000", http.StatusBadRequest)
		return
	}
	s.writeResourceDefinitions(w, fmt.Sprintf("study_id IN (%s)", inList(ids)),
		" ORDER BY resource_id ASC, study_id ASC", projectionParam(r))
}

// ---------- Resource data ----------

type resourceDataRow struct {
	StudyID    string `json:"study_id"`
	PatientID  string `json:"patient_id"`
	SampleID   string `json:"sample_id"`
	ResourceID string `json:"resource_id"`
	URL        string `json:"url"`
	resourceDefinitionRow
	DefResourceID string `json:"def_resource_id"`
}

var resourceDataSort = map[string]string{"ResourceId": "d.resource_id", "url": "d.url"}

// resourceDataLevel describes one of the resource_{study,patient,sample} views.
type resourceDataLevel struct {
	view       string
	idCols     string // extra id columns selected from the view
	idFilter   string // view column the {patientId}/{sampleId} path value filters
	tieBreaker string // secondary order after resource_id
}

var (
	studyResources   = resourceDataLevel{view: "resource_study", idCols: "'' AS patient_id, '' AS sample_id", tieBreaker: "d.url"}
	patientResources = resourceDataLevel{view: "resource_patient", idCols: "d.patient_id AS patient_id, '' AS sample_id", idFilter: "patient_id", tieBreaker: "d.patient_id"}
	sampleResources  = resourceDataLevel{view: "resource_sample", idCols: "d.patient_id AS patient_id, d.sample_id AS sample_id", idFilter: "sample_id", tieBreaker: "d.sample_id"}
)

// fetchResourceData queries one resource level of a study. id (optional)
// restricts to one patient/sample; resourceID (optional) to one resource.
func (s *State) fetchResourceData(r *http.Request, lvl resourceDataLevel, studyID, id string) ([]ResourceData, error) {
	projection := projectionParam(r)
	where := []string{fmt.Sprintf("d.study_id = '%s'", sqlLit(studyID))}
	if id != "" {
		where = append(where, fmt.Sprintf("d.%s = '%s'", lvl.idFilter, sqlLit(id)))
	}
	if rid := r.URL.Query().Get("resourceId"); rid != "" {
		where = append(where, fmt.Sprintf("d.resource_id = '%s'", sqlLit(rid)))
	}
	sql := fmt.Sprintf(
		"SELECT d.study_id AS study_id, %s, d.resource_id AS resource_id, d.url AS url, "+
			"rd.resource_id AS def_resource_id, rd.display_name AS display_name, rd.description AS description, "+
			"rd.resource_type AS resource_type, rd.open_by_default AS open_by_default, rd.priority AS priority, "+
			"rd.custom_metadata AS custom_metadata "+
			"FROM %s d LEFT JOIN resource_definition rd ON d.study_id = rd.study_id AND d.resource_id = rd.resource_id "+
			"WHERE %s%s",
		lvl.idCols, lvl.view, strings.Join(where, " AND "),
		pagingClause(r, resourceDataSort, "d.resource_id ASC, "+lvl.tieBreaker+" ASC"))
	rows, err := queryView[resourceDataRow](s.CH, sql)
	if err != nil {
		return nil, err
	}
	out := make([]ResourceData, 0, len(rows))
	for _, row := range rows {
		d := ResourceData{StudyID: row.StudyID, ResourceID: row.ResourceID, PatientID: row.PatientID, SampleID: row.SampleID}
		if row.SampleID != "" {
			d.UniqueSampleKey = uniqueKey(row.StudyID, row.SampleID)
		}
		if row.PatientID != "" {
			d.UniquePatientKey = uniqueKey(row.StudyID, row.PatientID)
		}
		if projection != "ID" {
			url := row.URL
			d.URL = &url
		}
		if projection == "DETAILED" {
			if row.DefResourceID == "" {
				continue // DETAILED inner-joins the definition
			}
			def := row.resourceDefinitionRow
			def.StudyID, def.ResourceID = row.StudyID, row.DefResourceID
			m := def.model("DETAILED")
			d.ResourceDefinition = &m
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *State) writeResourceData(w http.ResponseWriter, r *http.Request, levels ...resourceDataLevel) {
	studyID := r.PathValue("studyId")
	if !s.requireStudy(w, studyID) {
		return
	}
	out := []ResourceData{}
	for _, lvl := range levels {
		rows, err := s.fetchResourceData(r, lvl, studyID, "")
		if err != nil {
			fail(w, err)
			return
		}
		out = append(out, rows...)
	}
	writeJSON(w, out)
}

// studyResourceData — GET /api/studies/{studyId}/resource-data (study-level resources).
func (s *State) studyResourceData(w http.ResponseWriter, r *http.Request) {
	s.writeResourceData(w, r, studyResources)
}

// allResourceDataInStudy — GET /api/studies/{studyId}/resource-data-all: the
// study-, patient- and sample-level resources concatenated in that order.
func (s *State) allResourceDataInStudy(w http.ResponseWriter, r *http.Request) {
	s.writeResourceData(w, r, studyResources, patientResources, sampleResources)
}

// patientResourceData — GET /api/studies/{studyId}/patients/{patientId}/resource-data.
func (s *State) patientResourceData(w http.ResponseWriter, r *http.Request) {
	studyID, patientID := r.PathValue("studyId"), r.PathValue("patientId")
	if !s.requireStudy(w, studyID) {
		return
	}
	ok, err := s.existsIn(fmt.Sprintf("SELECT count() AS n FROM patient WHERE study_id = '%s' AND patient_id = '%s'",
		sqlLit(studyID), sqlLit(patientID)))
	if err != nil {
		fail(w, err)
		return
	}
	if !ok {
		http.Error(w, "patient not found: "+patientID, http.StatusNotFound)
		return
	}
	rows, err := s.fetchResourceData(r, patientResources, studyID, patientID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, rows)
}

// sampleResourceData — GET /api/studies/{studyId}/samples/{sampleId}/resource-data.
func (s *State) sampleResourceData(w http.ResponseWriter, r *http.Request) {
	studyID, sampleID := r.PathValue("studyId"), r.PathValue("sampleId")
	if !s.requireStudy(w, studyID) {
		return
	}
	ok, err := s.existsIn(fmt.Sprintf("SELECT count() AS n FROM sample WHERE study_id = '%s' AND sample_id = '%s'",
		sqlLit(studyID), sqlLit(sampleID)))
	if err != nil {
		fail(w, err)
		return
	}
	if !ok {
		http.Error(w, "sample not found: "+sampleID, http.StatusNotFound)
		return
	}
	rows, err := s.fetchResourceData(r, sampleResources, studyID, sampleID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, rows)
}
