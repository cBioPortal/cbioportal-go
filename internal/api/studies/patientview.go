package studies

import (
	"fmt"
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// samplesOfPatient — GET /api/studies/{studyId}/patients/{patientId}/samples
func (s Server) samplesOfPatient(w http.ResponseWriter, r *http.Request) {
	args := core.NewArgs()
	s.writeSamples(w, r, core.ByPatient(r.PathValue("studyId"), r.PathValue("patientId")), "1", args)
}

// patientClinicalData — GET /api/studies/{studyId}/patients/{patientId}/clinical-data
func (s Server) patientClinicalData(w http.ResponseWriter, r *http.Request) {
	args := core.NewArgs()
	s.writeClinical(w, r, true, []string{r.PathValue("studyId")}, fmt.Sprintf("study_id = %s AND patient_id = %s%s",
		args.Str(r.PathValue("studyId")), args.Str(r.PathValue("patientId")), attributeIDClause(r, args)), args)
}

// sampleClinicalData — GET /api/studies/{studyId}/samples/{sampleId}/clinical-data
func (s Server) sampleClinicalData(w http.ResponseWriter, r *http.Request) {
	args := core.NewArgs()
	s.writeClinical(w, r, false, []string{r.PathValue("studyId")}, fmt.Sprintf("study_id = %s AND sample_id = %s%s",
		args.Str(r.PathValue("studyId")), args.Str(r.PathValue("sampleId")), attributeIDClause(r, args)), args)
}

// attributeIDClause restricts clinical rows to the optional attributeId query
// parameter.
func attributeIDClause(r *http.Request, args *core.SQLArgs) string {
	if a := r.URL.Query().Get("attributeId"); a != "" {
		return " AND attr_id = " + args.Str(a)
	}
	return ""
}

// patient — GET /api/studies/{studyId}/patients/{patientId}
func (s Server) patient(w http.ResponseWriter, r *http.Request) {
	params := ch.Params{}
	rows, err := ch.QueryP[IDRow](s.CH, "SELECT study_id AS studyId, patient_id AS patientId FROM "+
		core.ByPatient(r.PathValue("studyId"), r.PathValue("patientId")).From(core.ParamsBinder(params), core.TPatient)+" LIMIT 1", params)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "patient not found", http.StatusNotFound)
		return
	}
	p := toPatients(rows)
	core.WriteJSON(w, p[0])
}
