package api

import (
	"fmt"
	"net/http"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// samplesOfPatient — GET /api/studies/{studyId}/patients/{patientId}/samples
func (s *State) samplesOfPatient(w http.ResponseWriter, r *http.Request) {
	s.writeSamples(w, fmt.Sprintf("s.study_id = '%s' AND s.patient_id = '%s'",
		sqlLit(r.PathValue("studyId")), sqlLit(r.PathValue("patientId"))))
}

// patientClinicalData — GET /api/studies/{studyId}/patients/{patientId}/clinical-data
func (s *State) patientClinicalData(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	pid := sqlLit(r.PathValue("patientId"))
	s.writeClinical(w, r, true, fmt.Sprintf("study_id = '%s' AND patient_id = '%s'%s", sid, pid, attributeIDClause(r)))
}

// sampleClinicalData — GET /api/studies/{studyId}/samples/{sampleId}/clinical-data
func (s *State) sampleClinicalData(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	smp := sqlLit(r.PathValue("sampleId"))
	s.writeClinical(w, r, false, fmt.Sprintf("study_id = '%s' AND sample_id = '%s'%s", sid, smp, attributeIDClause(r)))
}

// attributeIDClause restricts clinical rows to the optional attributeId query
// parameter.
func attributeIDClause(r *http.Request) string {
	if a := r.URL.Query().Get("attributeId"); a != "" {
		return fmt.Sprintf(" AND attr_id = '%s'", sqlLit(a))
	}
	return ""
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
