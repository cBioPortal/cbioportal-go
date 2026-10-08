package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// TestStudyViewBodyErrors checks the answers production's Java gives to
// study-view bodies without the fields its controllers dereference.
func TestStudyViewBodyErrors(t *testing.T) {
	h := NewRouter(newState(&core.State{}))
	for _, c := range []struct {
		path, body string
		status     int
		want       string // a substring of the body
	}{
		{"/api/mutation-data-counts/fetch", `{"genomicDataFilters":[{"hugoGeneSymbol":"ERBB2","profileType":"mutations"}]}`, 500, `"error":"Internal Server Error"`},
		{"/api/mutation-data-counts/fetch", `{"studyViewFilter":{"studyIds":["s"]}}`, 500, `"path":"/api/mutation-data-counts/fetch"`},
		{"/api/mutation-data-counts/fetch", `{"genomicDataFilters":[],"studyViewFilter":null}`, 500, ""},
		{"/api/mutation-data-counts/fetch", ``, 500, ""},
		{"/api/genomic-data-counts/fetch", `{"genomicDataFilters":[],"studyViewFilter":{"studyIds":["s"]}}`, 500, ""},
		{"/api/clinical-data-counts/fetch", `{"attributes":[{"attributeId":"AGE"}]}`, 500, ""},
		{"/api/clinical-data-counts/fetch", `{"attributes":[],"studyViewFilter":{"studyIds":["s"]}}`, 500, ""},
		{"/api/clinical-data-counts/fetch", `{"attributes":[{"attributeId":"AGE"}],"studyViewFilter":{}}`, 500, core.MsgSQLException},
		{"/api/clinical-data-bin-counts/fetch", `{"attributes":[{"attributeId":"AGE"}],"studyViewFilter":{}}`, 500, core.MsgSQLException},
		{"/api/clinical-data-bin-counts/fetch", `{"attributes":[]}`, 500, ""},
		{"/api/clinical-data-bin-counts/fetch", `{"attributes":[],"studyViewFilter":{"studyIds":["s"]}}`, 500, ""},
		{"/api/clinical-data-counts/fetch", `{"attributes":[{"attributeId":"AGE"}],"studyViewFilter":{"studyIds":[]}}`, 500, core.MsgSQLException},
		{"/api/genomic-data-bin-counts/fetch", `{"studyViewFilter":{"studyIds":["s"]}}`, 500, ""},
		{"/api/generic-assay-data-bin-counts/fetch", `{}`, 500, ""},
		{"/api/generic-assay-data-counts/fetch", `{"genericAssayDataFilters":[{"stableId":"x","profileType":"y"}]}`, 500, ""},
		{"/api/generic-assay-data-counts/fetch", `{"genericAssayDataFilters":[]}`, 400, ""},
		{"/api/mutation-data-counts/fetch", `{bad`, 400, core.MsgBadJSON},
	} {
		req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("POST %s %s: status %d body %q, want %d with %q", c.path, c.body, w.Code, w.Body.String(), c.status, c.want)
		}
	}
}
