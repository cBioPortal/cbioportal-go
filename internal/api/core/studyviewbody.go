package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// studyViewBody describes the fields of a column-store study-view filter that
// ColumnarStoreStudyViewController and its binners dereference without a
// null check: a request that leaves one out (or sends no body) fails there
// with a NullPointerException, Spring's generic 500.
type studyViewBody struct {
	// required are the fields that must be present and not null.
	required []string
	// nonEmpty are the required lists whose emptiness fails as well (the
	// query built from them is not valid SQL).
	nonEmpty []string
	// studiesRequired fails a studyViewFilter with neither studyIds nor
	// sampleIdentifiers (or only empty ones) with the SQL-exception 500 of
	// the controller advice.
	studiesRequired bool
}

var studyViewBodies = map[string]studyViewBody{
	"/api/mutation-data-counts/fetch":          {required: []string{"genomicDataFilters", "studyViewFilter"}},
	"/api/genomic-data-counts/fetch":           {required: []string{"genomicDataFilters", "studyViewFilter"}, nonEmpty: []string{"genomicDataFilters"}},
	"/api/clinical-data-counts/fetch":          {required: []string{"attributes", "studyViewFilter"}, nonEmpty: []string{"attributes"}, studiesRequired: true},
	"/api/clinical-data-bin-counts/fetch":      {required: []string{"attributes", "studyViewFilter"}, nonEmpty: []string{"attributes"}, studiesRequired: true},
	"/api/genomic-data-bin-counts/fetch":       {required: []string{"genomicDataBinFilters"}},
	"/api/generic-assay-data-bin-counts/fetch": {required: []string{"genericAssayDataBinFilters"}},
}

// MsgSQLException is the controller advice's answer to a failed query.
const MsgSQLException = "SQL exception. If you are a maintainer of this instance, see logs for details."

// StudyViewBodyErrors answers the study-view POSTs whose body lacks a field
// the Java controller dereferences (studyViewBodies) as Java does; bodies
// that are not a JSON object are left to the handler's JSON errors.
func StudyViewBodyErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spec, ok := studyViewBodies[r.URL.Path]
		if !ok || r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if status, msg, failed := spec.check(raw); failed {
			ReadBody(r, nil)
			if msg != "" {
				WriteError(w, status, msg)
			} else {
				SpringError(w, r, status)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

// check reports how Java fails on body raw, if it does.
func (spec studyViewBody) check(raw []byte) (status int, msg string, failed bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return http.StatusInternalServerError, "", true
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return 0, "", false
	}
	present := func(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" }
	for _, f := range spec.required {
		if !present(body[f]) {
			return http.StatusInternalServerError, "", true
		}
	}
	for _, f := range spec.nonEmpty {
		var list []json.RawMessage
		if json.Unmarshal(body[f], &list) == nil && len(list) == 0 {
			return http.StatusInternalServerError, "", true
		}
	}
	if spec.studiesRequired {
		var svf map[string]json.RawMessage
		nonEmptyList := func(v json.RawMessage) bool {
			var list []json.RawMessage
			return json.Unmarshal(v, &list) == nil && len(list) > 0
		}
		if json.Unmarshal(body["studyViewFilter"], &svf) == nil &&
			!nonEmptyList(svf["studyIds"]) && !nonEmptyList(svf["sampleIdentifiers"]) {
			return http.StatusInternalServerError, MsgSQLException, true
		}
	}
	return 0, "", false
}
