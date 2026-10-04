package api

import (
	"fmt"
	"net/http"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// samplesOfPatient — GET /api/studies/{studyId}/patients/{patientId}/samples
func (s *State) samplesOfPatient(w http.ResponseWriter, r *http.Request) {
	args := newArgs()
	s.writeSamples(w, "s.study_id = "+args.str(r.PathValue("studyId"))+" AND s.patient_id = "+args.str(r.PathValue("patientId")), args)
}

// patientClinicalData — GET /api/studies/{studyId}/patients/{patientId}/clinical-data
func (s *State) patientClinicalData(w http.ResponseWriter, r *http.Request) {
	args := newArgs()
	s.writeClinical(w, r, true, fmt.Sprintf("study_id = %s AND patient_id = %s%s",
		args.str(r.PathValue("studyId")), args.str(r.PathValue("patientId")), attributeIDClause(r, args)), args)
}

// sampleClinicalData — GET /api/studies/{studyId}/samples/{sampleId}/clinical-data
func (s *State) sampleClinicalData(w http.ResponseWriter, r *http.Request) {
	args := newArgs()
	s.writeClinical(w, r, false, fmt.Sprintf("study_id = %s AND sample_id = %s%s",
		args.str(r.PathValue("studyId")), args.str(r.PathValue("sampleId")), attributeIDClause(r, args)), args)
}

// attributeIDClause restricts clinical rows to the optional attributeId query
// parameter.
func attributeIDClause(r *http.Request, args *sqlArgs) string {
	if a := r.URL.Query().Get("attributeId"); a != "" {
		return " AND attr_id = " + args.str(a)
	}
	return ""
}

// patient — GET /api/studies/{studyId}/patients/{patientId}
func (s *State) patient(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.QueryP[idRow](s.CH,
		"SELECT study_id AS studyId, patient_id AS patientId FROM patient WHERE study_id = {study:String} AND patient_id = {patient:String} LIMIT 1",
		ch.Params{"study": r.PathValue("studyId"), "patient": r.PathValue("patientId")})
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
