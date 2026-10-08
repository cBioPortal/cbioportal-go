package api

import (
	"fmt"
	"net/http"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// samplesOfPatient — GET /api/studies/{studyId}/patients/{patientId}/samples
func (s *State) samplesOfPatient(w http.ResponseWriter, r *http.Request) {
	args := newArgs()
	s.writeSamples(w, r, byPatient(r.PathValue("studyId"), r.PathValue("patientId")), "1", args)
}

// patientClinicalData — GET /api/studies/{studyId}/patients/{patientId}/clinical-data
func (s *State) patientClinicalData(w http.ResponseWriter, r *http.Request) {
	args := newArgs()
	s.writeClinical(w, r, true, []string{r.PathValue("studyId")}, fmt.Sprintf("study_id = %s AND patient_id = %s%s",
		args.str(r.PathValue("studyId")), args.str(r.PathValue("patientId")), attributeIDClause(r, args)), args)
}

// sampleClinicalData — GET /api/studies/{studyId}/samples/{sampleId}/clinical-data
func (s *State) sampleClinicalData(w http.ResponseWriter, r *http.Request) {
	args := newArgs()
	s.writeClinical(w, r, false, []string{r.PathValue("studyId")}, fmt.Sprintf("study_id = %s AND sample_id = %s%s",
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
	params := ch.Params{}
	rows, err := ch.QueryP[idRow](s.CH, "SELECT study_id AS studyId, patient_id AS patientId FROM "+
		byPatient(r.PathValue("studyId"), r.PathValue("patientId")).from(paramsBinder(params), tPatient)+" LIMIT 1", params)
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "patient not found", http.StatusNotFound)
		return
	}
	p := toPatients(rows)
	writeJSON(w, p[0])
}
