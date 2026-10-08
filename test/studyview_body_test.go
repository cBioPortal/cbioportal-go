package contract

import (
	"strings"
	"testing"
)

// The column-store study-view controllers dereference the parts of their
// filter: a body without them fails with Spring's 500 (or, for a filter that
// names no studies, the SQL-exception message).
func TestContractStudyViewBodyWithoutFilter(t *testing.T) {
	requireAPI(t)
	for _, c := range []struct{ path, body, want string }{
		{"/api/mutation-data-counts/fetch", `{"genomicDataFilters":[{"hugoGeneSymbol":"BRCA1","profileType":"mutations"}]}`, `"error":"Internal Server Error"`},
		{"/api/genomic-data-counts/fetch", `{"genomicDataFilters":[{"hugoGeneSymbol":"BRCA1","profileType":"gistic"}]}`, `"error":"Internal Server Error"`},
		{"/api/clinical-data-counts/fetch", `{"attributes":[{"attributeId":"AGE"}],"studyViewFilter":{}}`, `"message":"SQL exception.`},
		{"/api/generic-assay-data-counts/fetch", `{"genericAssayDataFilters":[{"stableId":"x","profileType":"y"}]}`, `"error":"Internal Server Error"`},
	} {
		resp, body := rawDo(t, "POST", c.path, c.body)
		if resp.StatusCode != 500 || !strings.Contains(string(body), c.want) {
			t.Errorf("POST %s %s: status %d body %s", c.path, c.body, resp.StatusCode, body)
		}
	}
	resp, _ := rawDo(t, "POST", "/api/mutation-data-counts/fetch",
		`{"genomicDataFilters":[{"hugoGeneSymbol":"BRCA1","profileType":"mutations"}],"studyViewFilter":{"studyIds":["study_es_0"]}}`)
	wantEq(t, resp.StatusCode, 200)
}
