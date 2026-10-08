package studies

import (
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// RouteMisc registers the study clinical-attribute endpoints.
func (s Server) RouteMisc(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-attributes", s.studyClinicalAttributes)
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-attributes/{clinicalAttributeId}", s.studyClinicalAttribute)
}

// ---------- Clinical attributes (per study) ----------

func toClinicalAttribute(r ClinAttrRow) ClinicalAttribute {
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

func (s Server) studyClinicalAttributes(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.QueryP[ClinAttrRow](s.CH, clinAttrSelect+" WHERE study_id = {study:String} ORDER BY clinical_attribute_id",
		ch.Params{"study": r.PathValue("studyId")})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]ClinicalAttribute, len(rows))
	for i, r := range rows {
		out[i] = toClinicalAttribute(r)
	}
	core.WriteJSON(w, out)
}

func (s Server) studyClinicalAttribute(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.QueryP[ClinAttrRow](s.CH, clinAttrSelect+" WHERE study_id = {study:String} AND clinical_attribute_id = {attr:String} LIMIT 1",
		ch.Params{"study": r.PathValue("studyId"), "attr": r.PathValue("clinicalAttributeId")})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "clinical attribute not found", http.StatusNotFound)
		return
	}
	core.WriteJSON(w, toClinicalAttribute(rows[0]))
}
