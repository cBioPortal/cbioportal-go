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
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	Value               string `json:"value"`
}

type ClinicalData struct {
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	Value               string `json:"value"`
	SampleID            string `json:"sampleId"`
	PatientID           string `json:"patientId"`
	StudyID             string `json:"studyId"`
	UniqueSampleKey     string `json:"uniqueSampleKey"`
	UniquePatientKey    string `json:"uniquePatientKey"`
	PatientAttribute    bool   `json:"patientAttribute"`
}

func isPatient(r *http.Request) bool {
	return strings.EqualFold(r.URL.Query().Get("clinicalDataType"), "PATIENT")
}

func toClinicalData(rows []clinicalRow, patient bool) []ClinicalData {
	out := make([]ClinicalData, len(rows))
	for i, r := range rows {
		var sampleID, patientID string
		if patient {
			patientID = r.ID
		} else {
			sampleID = r.ID
		}
		out[i] = ClinicalData{
			ClinicalAttributeID: r.ClinicalAttributeID, Value: r.Value,
			SampleID: sampleID, PatientID: patientID, StudyID: r.StudyID,
			UniqueSampleKey: uniqueKey(r.StudyID, sampleID), UniquePatientKey: uniqueKey(r.StudyID, patientID),
			PatientAttribute: patient,
		}
	}
	return out
}

func (s *State) clinicalData(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	patient := isPatient(r)
	view, idCol := "clinical_sample", "sample_id"
	if patient {
		view, idCol = "clinical_patient", "patient_id"
	}
	rows, err := ch.Query[clinicalRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, %s AS id, attr_id AS clinicalAttributeId, attr_value AS value FROM %s WHERE study_id = '%s' ORDER BY id, attr_id",
		idCol, view, sid))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toClinicalData(rows, patient))
}

func (s *State) clinicalDataStudyFetch(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	var f struct {
		IDs          []string `json:"ids"`
		AttributeIDs []string `json:"attributeIds"`
	}
	readBody(r, &f)
	patient := isPatient(r)
	view, idCol := "clinical_sample", "sample_id"
	if patient {
		view, idCol = "clinical_patient", "patient_id"
	}
	where := fmt.Sprintf("study_id = '%s'", sid)
	if len(f.IDs) > 0 {
		where += fmt.Sprintf(" AND %s IN (%s)", idCol, inList(f.IDs))
	}
	if len(f.AttributeIDs) > 0 {
		where += fmt.Sprintf(" AND attr_id IN (%s)", inList(f.AttributeIDs))
	}
	rows, err := ch.Query[clinicalRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, %s AS id, attr_id AS clinicalAttributeId, attr_value AS value FROM %s WHERE %s ORDER BY id, attr_id",
		idCol, view, where))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toClinicalData(rows, patient))
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
		dt := r.Datatype
		if dt == "" {
			dt = "STRING"
		}
		pr := r.Priority
		if pr == "" {
			pr = "1"
		}
		out[i] = ClinicalAttribute{
			ClinicalAttributeID: r.ClinicalAttributeID, DisplayName: r.DisplayName, Description: r.Description,
			Datatype: dt, PatientAttribute: r.PatientFlag != 0, Priority: pr, StudyID: r.StudyID,
		}
	}
	writeJSON(w, out)
}
