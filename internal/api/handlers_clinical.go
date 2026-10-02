package api

import (
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

type clinicalRow struct {
	StudyID             string `json:"studyId"`
	ID                  string `json:"id"`
	PatientID           string `json:"patientId"`
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	Value               string `json:"value"`
}

type ClinicalData struct {
	ClinicalAttributeID string             `json:"clinicalAttributeId"`
	Value               string             `json:"value"`
	SampleID            string             `json:"sampleId,omitempty"`
	PatientID           string             `json:"patientId"`
	StudyID             string             `json:"studyId"`
	UniqueSampleKey     string             `json:"uniqueSampleKey,omitempty"`
	UniquePatientKey    string             `json:"uniquePatientKey"`
	PatientAttribute    bool               `json:"patientAttribute"`
	ClinicalAttribute   *ClinicalAttribute `json:"clinicalAttribute,omitempty"`
}

func isPatient(r *http.Request) bool {
	return strings.EqualFold(r.URL.Query().Get("clinicalDataType"), "PATIENT")
}

func toClinicalData(rows []clinicalRow, patient bool) []ClinicalData {
	out := make([]ClinicalData, len(rows))
	for i, r := range rows {
		d := ClinicalData{
			ClinicalAttributeID: r.ClinicalAttributeID, Value: r.Value, StudyID: r.StudyID, PatientAttribute: patient,
		}
		if patient {
			d.PatientID = r.ID
		} else {
			d.SampleID, d.PatientID = r.ID, r.PatientID
			d.UniqueSampleKey = uniqueKey(r.StudyID, r.ID)
		}
		d.UniquePatientKey = uniqueKey(r.StudyID, d.PatientID)
		out[i] = d
	}
	return out
}

// clinicalSQL selects clinical rows (sample- or patient-level) matching where,
// which may reference study_id, the entity id column (sample_id / patient_id)
// and attr_id. Sample rows carry their patient id.
func clinicalSQL(patient bool, where string) string {
	if patient {
		return "SELECT study_id AS studyId, patient_id AS id, patient_id AS patientId, attr_id AS clinicalAttributeId, " +
			"attr_value AS value FROM clinical_patient WHERE " + where + " ORDER BY studyId, id, clinicalAttributeId"
	}
	return "SELECT c.study_id AS studyId, c.sample_id AS id, s.patient_id AS patientId, c.attr_id AS clinicalAttributeId, " +
		"c.attr_value AS value FROM (SELECT * FROM clinical_sample WHERE " + where + ") c " +
		"LEFT JOIN sample s ON s.study_id = c.study_id AND s.sample_id = c.sample_id ORDER BY studyId, id, clinicalAttributeId"
}

// clinicalDataOut is a legacy ClinicalData record as Jackson writes it
// (NON_NULL, see ClinicalDataMapper.selectSample / selectPatient): ID carries
// the identifying fields only, SUMMARY adds the value and DETAILED the
// attribute metadata, with patientAttribute derived from it.
type clinicalDataOut struct {
	UniqueSampleKey     string             `json:"uniqueSampleKey,omitempty"`
	UniquePatientKey    string             `json:"uniquePatientKey"`
	SampleID            string             `json:"sampleId,omitempty"`
	PatientID           string             `json:"patientId"`
	StudyID             string             `json:"studyId"`
	ClinicalAttribute   *ClinicalAttribute `json:"clinicalAttribute,omitempty"`
	PatientAttribute    *bool              `json:"patientAttribute,omitempty"`
	ClinicalAttributeID string             `json:"clinicalAttributeId"`
	Value               *string            `json:"value,omitempty"`
}

// writeClinical runs the query and writes it in the requested projection
// (ID, SUMMARY, DETAILED with the attribute metadata, or META as a count header).
func (s *State) writeClinical(w http.ResponseWriter, r *http.Request, patient bool, where string) {
	projection := projectionParam(r)
	if projection == "META" {
		type c struct {
			N uint64 `json:"n"`
		}
		view := "clinical_sample"
		if patient {
			view = "clinical_patient"
		}
		rows, _ := ch.Query[c](s.CH, fmt.Sprintf("SELECT count() AS n FROM %s WHERE %s", view, where))
		var n uint64
		if len(rows) > 0 {
			n = rows[0].N
		}
		w.Header().Set("total-count", fmt.Sprint(n))
		w.WriteHeader(http.StatusOK)
		return
	}
	rows, err := ch.Query[clinicalRow](s.CH, clinicalSQL(patient, where))
	if err != nil {
		fail(w, err)
		return
	}
	data := toClinicalData(rows, patient)
	var attrs map[string]ClinicalAttribute
	if projection == "DETAILED" && len(data) > 0 {
		studies := map[string]bool{}
		for _, d := range data {
			studies[d.StudyID] = true
		}
		attrs = s.clinicalAttributes(sortedKeys(studies))
	}
	out := make([]clinicalDataOut, len(data))
	for i, d := range data {
		o := clinicalDataOut{
			UniqueSampleKey: d.UniqueSampleKey, UniquePatientKey: d.UniquePatientKey, SampleID: d.SampleID,
			PatientID: d.PatientID, StudyID: d.StudyID, ClinicalAttributeID: d.ClinicalAttributeID,
		}
		if projection != "ID" {
			o.Value = &data[i].Value
		}
		if a, ok := attrs[d.StudyID+"\x00"+d.ClinicalAttributeID]; ok {
			o.ClinicalAttribute = &a
			o.PatientAttribute = &a.PatientAttribute
		}
		out[i] = o
	}
	writeJSON(w, out)
}

func (s *State) clinicalData(w http.ResponseWriter, r *http.Request) {
	where := fmt.Sprintf("study_id = '%s'", sqlLit(r.PathValue("studyId")))
	if a := r.URL.Query().Get("attributeId"); a != "" {
		where += fmt.Sprintf(" AND attr_id = '%s'", sqlLit(a))
	}
	s.writeClinical(w, r, isPatient(r), where)
}

func (s *State) clinicalDataStudyFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		IDs          []string `json:"ids"`
		AttributeIDs []string `json:"attributeIds"`
	}
	readBody(r, &f)
	patient := isPatient(r)
	idCol := "sample_id"
	if patient {
		idCol = "patient_id"
	}
	where := fmt.Sprintf("study_id = '%s'", sqlLit(r.PathValue("studyId")))
	if len(f.IDs) > 0 {
		where += fmt.Sprintf(" AND %s IN (%s)", idCol, inList(f.IDs))
	}
	if len(f.AttributeIDs) > 0 {
		where += fmt.Sprintf(" AND attr_id IN (%s)", inList(f.AttributeIDs))
	}
	s.writeClinical(w, r, patient, where)
}

// clinicalDataFetch serves POST /api/clinical-data/fetch: clinical data for
// (studyId, entityId) identifiers across studies, optionally limited to
// attributeIds.
func (s *State) clinicalDataFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Identifiers []struct {
			StudyID  string `json:"studyId"`
			EntityID string `json:"entityId"`
		} `json:"identifiers"`
		AttributeIDs []string `json:"attributeIds"`
	}
	readBody(r, &f)
	if len(f.Identifiers) == 0 {
		writeJSON(w, []ClinicalData{})
		return
	}
	patient := isPatient(r)
	idCol := "sample_id"
	if patient {
		idCol = "patient_id"
	}
	pairs := make([][2]string, len(f.Identifiers))
	for i, id := range f.Identifiers {
		pairs[i] = [2]string{id.StudyID, id.EntityID}
	}
	where := fmt.Sprintf("(study_id, %s) IN (%s)", idCol, tupleIn(pairs))
	if len(f.AttributeIDs) > 0 {
		where += fmt.Sprintf(" AND attr_id IN (%s)", inList(f.AttributeIDs))
	}
	s.writeClinical(w, r, patient, where)
}

// clinicalAttributes indexes the studies' attribute metadata by study and id.
func (s *State) clinicalAttributes(studyIDs []string) map[string]ClinicalAttribute {
	out := map[string]ClinicalAttribute{}
	rows, _ := ch.Query[clinAttrRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, clinical_attribute_id AS clinicalAttributeId, display_name AS displayName, "+
			"description, datatype, priority, patient_attribute AS patientFlag FROM clinical_attribute_meta WHERE study_id IN (%s)",
		orEmpty(studyIDs)))
	for _, r := range rows {
		out[r.StudyID+"\x00"+r.ClinicalAttributeID] = r.toAttribute()
	}
	return out
}

type clinAttrRow struct {
	StudyID             string `json:"studyId"`
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	DisplayName         string `json:"displayName"`
	Description         string `json:"description"`
	Datatype            string `json:"datatype"`
	Priority            string `json:"priority"`
	PatientFlag         int    `json:"patientFlag"`
}

type ClinicalAttribute struct {
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	DisplayName         string `json:"displayName"`
	Description         string `json:"description"`
	Datatype            string `json:"datatype"`
	PatientAttribute    bool   `json:"patientAttribute"`
	Priority            string `json:"priority"`
	StudyID             string `json:"studyId"`
}

func (s *State) clinicalAttributesFetch(w http.ResponseWriter, r *http.Request) {
	var studyIDs []string
	readBody(r, &studyIDs)
	if len(studyIDs) == 0 {
		writeJSON(w, []ClinicalAttribute{})
		return
	}
	rows, err := ch.Query[clinAttrRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, clinical_attribute_id AS clinicalAttributeId, display_name AS displayName, "+
			"description, datatype, priority, patient_attribute AS patientFlag FROM clinical_attribute_meta WHERE study_id IN (%s)",
		inList(studyIDs)))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]ClinicalAttribute, len(rows))
	for i, r := range rows {
		out[i] = r.toAttribute()
	}
	writeJSON(w, out)
}

func (r clinAttrRow) toAttribute() ClinicalAttribute {
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
