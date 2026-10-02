package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeMisc registers the view-backed endpoints implemented in this file.
// It deliberately does NOT register POST /api/clinical-attributes/counts/fetch
// (currently an emptyArray stub in routes.go); the integrator wires
// s.clinicalAttributeCountsFetch after removing that stub.
func (s *State) routeMisc(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/copy-number-segments/fetch", s.copyNumberSegmentsFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-attributes", s.studyClinicalAttributes)
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-attributes/{clinicalAttributeId}", s.studyClinicalAttribute)
}

// ---------- Copy-number segments ----------

type segRow struct {
	StudyID        string  `json:"studyId"`
	SampleID       string  `json:"sampleId"`
	PatientID      string  `json:"patientId"`
	Chromosome     string  `json:"chromosome"`
	Start          int64   `json:"start"`
	End            int64   `json:"end"`
	NumberOfProbes int64   `json:"numberOfProbes"`
	SegmentMean    float64 `json:"segmentMean"`
}

type CopyNumberSeg struct {
	UniqueSampleKey  string  `json:"uniqueSampleKey"`
	UniquePatientKey string  `json:"uniquePatientKey"`
	PatientID        string  `json:"patientId"`
	SampleID         string  `json:"sampleId"`
	StudyID          string  `json:"studyId"`
	Chromosome       string  `json:"chromosome"`
	Start            int64   `json:"start"`
	End              int64   `json:"end"`
	NumberOfProbes   int64   `json:"numberOfProbes"`
	SegmentMean      float64 `json:"segmentMean"`
	SegID            int64   `json:"segId"`
	Genes            []any   `json:"genes"`
}

func (s *State) copyNumberSegmentsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers []identifier `json:"sampleIdentifiers"`
		Chromosome        string       `json:"chromosome"`
	}
	// The body is a list of sample identifiers (an object wrapping them is also
	// accepted); the chromosome is a query parameter.
	var raw json.RawMessage
	readBody(r, &raw)
	if json.Unmarshal(raw, &f.SampleIdentifiers) != nil {
		json.Unmarshal(raw, &f)
	}
	if c := r.URL.Query().Get("chromosome"); c != "" {
		f.Chromosome = c
	}
	if len(f.SampleIdentifiers) == 0 {
		writeJSON(w, []CopyNumberSeg{})
		return
	}
	pairs := make([][2]string, len(f.SampleIdentifiers))
	for i, id := range f.SampleIdentifiers {
		pairs[i] = [2]string{id.StudyID, id.id()}
	}
	where := fmt.Sprintf("(g.study_id, g.sample_id) IN (%s)", tupleIn(pairs))
	if f.Chromosome != "" {
		where += fmt.Sprintf(" AND g.chrom = '%s'", sqlLit(f.Chromosome))
	}
	rows, err := ch.Query[segRow](s.CH, fmt.Sprintf(
		"SELECT g.study_id AS studyId, g.sample_id AS sampleId, ifNull(sp.patient_id, '') AS patientId, "+
			"g.chrom AS chromosome, toInt64OrZero(g.start) AS start, toInt64OrZero(g.end) AS end, "+
			"toInt64OrZero(g.num_probes) AS numberOfProbes, toFloat64OrZero(g.segment_mean) AS segmentMean "+
			"FROM seg g LEFT JOIN sample sp ON g.study_id = sp.study_id AND g.sample_id = sp.sample_id "+
			"WHERE %s ORDER BY g.study_id, g.sample_id, g.chrom, start", where))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]CopyNumberSeg, len(rows))
	for i, r := range rows {
		out[i] = CopyNumberSeg{
			UniqueSampleKey:  uniqueKey(r.StudyID, r.SampleID),
			UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
			PatientID:        r.PatientID, SampleID: r.SampleID, StudyID: r.StudyID,
			Chromosome: r.Chromosome, Start: r.Start, End: r.End,
			NumberOfProbes: r.NumberOfProbes, SegmentMean: r.SegmentMean,
			Genes: []any{},
		}
	}
	writeJSON(w, out)
}

// ---------- Clinical attributes (per study) ----------

func toClinicalAttribute(r clinAttrRow) ClinicalAttribute {
	dt := r.Datatype
	if dt == "" {
		dt = "STRING"
	}
	pr := r.Priority
	if pr == "" {
		pr = "1"
	}
	return ClinicalAttribute{
		ClinicalAttributeID: r.ClinicalAttributeID, DisplayName: r.DisplayName, Description: r.Description,
		Datatype: dt, PatientAttribute: r.PatientFlag != 0, Priority: pr, StudyID: r.StudyID,
	}
}

const clinAttrSelect = "SELECT study_id AS studyId, clinical_attribute_id AS clinicalAttributeId, " +
	"display_name AS displayName, description, datatype, priority, patient_attribute AS patientFlag " +
	"FROM clinical_attribute_meta"

func (s *State) studyClinicalAttributes(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	rows, err := ch.Query[clinAttrRow](s.CH, fmt.Sprintf(
		"%s WHERE study_id = '%s' ORDER BY clinical_attribute_id", clinAttrSelect, sid))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]ClinicalAttribute, len(rows))
	for i, r := range rows {
		out[i] = toClinicalAttribute(r)
	}
	writeJSON(w, out)
}

func (s *State) studyClinicalAttribute(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	aid := sqlLit(r.PathValue("clinicalAttributeId"))
	rows, err := ch.Query[clinAttrRow](s.CH, fmt.Sprintf(
		"%s WHERE study_id = '%s' AND clinical_attribute_id = '%s' LIMIT 1", clinAttrSelect, sid, aid))
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "clinical attribute not found", http.StatusNotFound)
		return
	}
	writeJSON(w, toClinicalAttribute(rows[0]))
}

// ---------- Clinical attribute counts ----------

type ClinicalAttributeCount struct {
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	Count               uint64 `json:"count"`
}

// clinicalAttributeCountsFetch serves POST /api/clinical-attributes/counts/fetch.
// It is intentionally NOT registered in routeMisc: routes.go currently has an
// emptyArray stub for that pattern. The integrator removes the stub and wires
// this method to avoid a duplicate-pattern panic.
func (s *State) clinicalAttributeCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers []identifier    `json:"sampleIdentifiers"`
		SampleListID      string          `json:"sampleListId"`
		StudyViewFilter   studyViewFilter `json:"studyViewFilter"`
		StudyIDs          []string        `json:"studyIds"`
	}
	readBody(r, &f)

	scopeFilter := studyViewFilter{}
	var sampleScope string
	switch {
	case f.SampleListID != "":
		sampleScope = fmt.Sprintf("(study_id, sample_id) IN (SELECT study_id, sample_id FROM sample_list WHERE list_id = '%s')",
			sqlLit(f.SampleListID))
	case len(f.SampleIdentifiers) > 0:
		scopeFilter.SampleIdentifiers = f.SampleIdentifiers
	case len(f.StudyViewFilter.SampleIdentifiers) > 0 || len(f.StudyViewFilter.StudyIDs) > 0:
		scopeFilter = f.StudyViewFilter
	case len(f.StudyIDs) > 0:
		scopeFilter.StudyIDs = f.StudyIDs
	default:
		writeJSON(w, []ClinicalAttributeCount{})
		return
	}
	if sampleScope == "" {
		sampleScope = s.sampleScope(scopeFilter)
	}
	patientScope := fmt.Sprintf(
		"(study_id, patient_id) IN (SELECT study_id, patient_id FROM sample WHERE %s)", sampleScope)

	type countRow struct {
		AttrID string `json:"attr_id"`
		C      uint64 `json:"c"`
	}
	out := []ClinicalAttributeCount{}
	queries := []struct {
		view, idCol, scope string
	}{
		{"clinical_sample", "sample_id", sampleScope},
		{"clinical_patient", "patient_id", patientScope},
	}
	for _, q := range queries {
		rows, _ := ch.Query[countRow](s.CH, fmt.Sprintf(
			"SELECT attr_id, uniqExact(%s) AS c FROM %s WHERE %s GROUP BY attr_id",
			q.idCol, q.view, q.scope))
		for _, r := range rows {
			out = append(out, ClinicalAttributeCount{ClinicalAttributeID: r.AttrID, Count: r.C})
		}
	}
	writeJSON(w, out)
}
