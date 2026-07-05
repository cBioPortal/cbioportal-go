package api

import (
	"fmt"
	"net/http"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// samplesOfPatient — GET /api/studies/{studyId}/patients/{patientId}/samples
func (s *State) samplesOfPatient(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	pid := sqlLit(r.PathValue("patientId"))
	rows, err := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM sample "+
			"WHERE study_id = '%s' AND patient_id = '%s' ORDER BY sample_id", sid, pid))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]Sample, len(rows))
	for i, r := range rows {
		out[i] = Sample{
			SampleID: r.SampleID, PatientID: r.PatientID, StudyID: r.StudyID,
			UniqueSampleKey: uniqueKey(r.StudyID, r.SampleID), UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
		}
	}
	writeJSON(w, out)
}

// patientClinicalData — GET /api/studies/{studyId}/patients/{patientId}/clinical-data
func (s *State) patientClinicalData(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	pid := sqlLit(r.PathValue("patientId"))
	rows, err := ch.Query[clinicalRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, patient_id AS id, attr_id AS clinicalAttributeId, attr_value AS value "+
			"FROM clinical_patient WHERE study_id = '%s' AND patient_id = '%s' ORDER BY attr_id", sid, pid))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toClinicalData(rows, true))
}

// sampleClinicalData — GET /api/studies/{studyId}/samples/{sampleId}/clinical-data
func (s *State) sampleClinicalData(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	smp := sqlLit(r.PathValue("sampleId"))
	rows, err := ch.Query[clinicalRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS id, attr_id AS clinicalAttributeId, attr_value AS value "+
			"FROM clinical_sample WHERE study_id = '%s' AND sample_id = '%s' ORDER BY attr_id", sid, smp))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toClinicalData(rows, false))
}

// patient — GET /api/studies/{studyId}/patients/{patientId}
func (s *State) patient(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	pid := sqlLit(r.PathValue("patientId"))
	rows, err := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, patient_id AS patientId FROM patient WHERE study_id = '%s' AND patient_id = '%s' LIMIT 1", sid, pid))
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "patient not found", http.StatusNotFound)
		return
	}
	p := toPatients(rows)
	writeJSON(w, p[0])
}
