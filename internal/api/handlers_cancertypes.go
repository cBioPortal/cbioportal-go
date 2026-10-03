package api

import (
	"net/http"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeCancerTypes registers the single cancer type lookup; the list endpoint
// is registered in routes.go.
func (s *State) routeCancerTypes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cancer-types/{cancerTypeId}", s.cancerType)
}

// refCancerTypesSQL selects the reference cancer types.
const refCancerTypesSQL = "SELECT type_of_cancer_id, name, dedicated_color, short_name, parent FROM cbioportal_ref.type_of_cancer"

// cancerTypesSQL is every cancer type: the reference set plus the
// study-supplied ones (type_of_cancer view) whose ids the reference does not
// define — cbioportal-core imports study cancer types without clobbering.
const cancerTypesSQL = refCancerTypesSQL + " UNION ALL " +
	"SELECT type_of_cancer_id, name, dedicated_color, short_name, parent FROM type_of_cancer " +
	"WHERE type_of_cancer_id NOT IN (SELECT type_of_cancer_id FROM cbioportal_ref.type_of_cancer)"

// studyCancerTypesSQL is cancerTypesSQL restricted to the columns the study
// list joins on.
const studyCancerTypesSQL = "SELECT type_of_cancer_id, name, dedicated_color, parent FROM cbioportal_ref.type_of_cancer UNION ALL " +
	"SELECT type_of_cancer_id, name, dedicated_color, parent FROM type_of_cancer " +
	"WHERE type_of_cancer_id NOT IN (SELECT type_of_cancer_id FROM cbioportal_ref.type_of_cancer)"

type typeOfCancerRow struct {
	TypeOfCancerID string `json:"type_of_cancer_id"`
	Name           string `json:"name"`
	DedicatedColor string `json:"dedicated_color"`
	ShortName      string `json:"short_name"`
	Parent         string `json:"parent"`
}

// queryCancerTypes returns the cancer types matching where (may be empty),
// falling back to the reference set when the study cancer-type view is absent
// or predates the short_name column. p holds the values of where's
// placeholders.
func (s *State) queryCancerTypes(where string, p ch.Params) ([]CancerType, error) {
	tail := where + " ORDER BY type_of_cancer_id"
	rows, err := ch.QueryP[typeOfCancerRow](s.CH, "SELECT * FROM ("+cancerTypesSQL+")"+tail, p)
	if err != nil {
		rows, err = ch.QueryP[typeOfCancerRow](s.CH, "SELECT * FROM ("+refCancerTypesSQL+")"+tail, p)
	}
	if err != nil {
		return nil, err
	}
	out := make([]CancerType, len(rows))
	for i, r := range rows {
		out[i] = CancerType{CancerTypeID: r.TypeOfCancerID, Name: r.Name, DedicatedColor: r.DedicatedColor, ShortName: r.ShortName, Parent: r.Parent}
	}
	return out, nil
}

// cancerType — GET /api/cancer-types/{cancerTypeId}.
func (s *State) cancerType(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cancerTypeId")
	rows, err := s.queryCancerTypes(" WHERE type_of_cancer_id = {id:String}", ch.Params{"id": id})
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "cancer type not found: "+id, http.StatusNotFound)
		return
	}
	writeJSON(w, rows[0])
}
