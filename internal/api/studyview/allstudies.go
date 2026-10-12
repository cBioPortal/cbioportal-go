package studyview

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// allStudiesRouter registers each handler behind allStudies.
type allStudiesRouter struct {
	mux router
	s   Server
}

func (r allStudiesRouter) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	_, path, _ := strings.Cut(pattern, " ")
	r.mux.HandleFunc(pattern, r.s.allStudies(h, wholeBodyFilter[path]))
}

// wholeBodyFilter are the endpoints whose body is the study-view filter;
// the others carry it as their studyViewFilter, if at all.
var wholeBodyFilter = map[string]bool{
	"/api/filtered-samples/fetch": true, "/api/mutated-genes/fetch": true, "/api/cna-genes/fetch": true,
	"/api/structuralvariant-genes/fetch": true, "/api/molecular-profile-sample-counts/fetch": true,
	"/api/structuralvariant-counts/fetch": true, "/api/clinical-data-density-plot/fetch": true,
	"/api/clinical-data-violin-plots/fetch": true, "/api/sample-lists-counts/fetch": true,
	"/api/clinical-data-table/fetch": true, "/api/clinical-event-type-counts/fetch": true,
	"/api/treatments/patient-counts/fetch": true, "/api/treatments/sample-counts/fetch": true,
	"/api/treatments/patient": true, "/api/treatments/sample": true,
}

// allStudies covers a study-view filter that names neither studies nor
// samples, but narrows by something else (clinical data, genes, case lists),
// with every study the caller may read: Java's filter SQL then has no study
// condition and searches the whole portal. A filter that narrows by nothing
// else still selects no samples, as in Java. The filter is the body when
// whole, else its studyViewFilter.
func (s Server) allStudies(h http.HandlerFunc, whole bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			studies := func() ([]string, error) { return s.readableStudies(r) }
			if body, ok := withStudies(core.BufferedBody(r), whole, studies); ok {
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
			}
		}
		h(w, r)
	}
}

// withStudies returns the body with the filter's studyIds set to studies(),
// when the filter names no studies or samples and has another filter. The
// filter is the body when whole, else its studyViewFilter.
func withStudies(body []byte, whole bool, studies func() ([]string, error)) ([]byte, bool) {
	var outer map[string]json.RawMessage
	if json.Unmarshal(body, &outer) != nil || outer == nil {
		return nil, false
	}
	target, raw := outer, body
	if !whole {
		raw, target = outer["studyViewFilter"], nil
		if json.Unmarshal(raw, &target) != nil || target == nil {
			return nil, false
		}
	}
	var f studyViewFilter
	core.DecodeJSON(raw, &f)
	if len(f.StudyIDs) > 0 || len(f.SampleIdentifiers) > 0 || !f.hasSubFilters() {
		return nil, false
	}
	ids, err := studies()
	if err != nil {
		return nil, false
	}
	target["studyIds"], _ = json.Marshal(ids)
	delete(target, "sampleIdentifiers")
	if !whole {
		outer["studyViewFilter"], _ = json.Marshal(target)
	}
	out, err := json.Marshal(outer)
	return out, err == nil
}

// readableStudies are the ids of the portal's studies the caller may read.
func (s Server) readableStudies(r *http.Request) ([]string, error) {
	type row struct {
		ID string `json:"id"`
	}
	rows, err := core.CachedQuery[row](s.State, "SELECT study_id AS id FROM cancer_study ORDER BY id")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, x := range rows {
		if core.StudyReadable(r, x.ID) {
			ids = append(ids, x.ID)
		}
	}
	return ids, nil
}
