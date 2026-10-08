package core

import (
	"testing"
)

// TestStudyViewBodyComplete checks that complete bodies pass the check.
func TestStudyViewBodyComplete(t *testing.T) {
	for path, body := range map[string]string{
		"/api/mutation-data-counts/fetch":          `{"genomicDataFilters":[],"studyViewFilter":{}}`,
		"/api/genomic-data-counts/fetch":           `{"genomicDataFilters":[{"hugoGeneSymbol":"A","profileType":"gistic"}],"studyViewFilter":{}}`,
		"/api/clinical-data-counts/fetch":          `{"attributes":[{"attributeId":"AGE"}],"studyViewFilter":{"studyIds":["s"]}}`,
		"/api/clinical-data-bin-counts/fetch":      `{"attributes":[{"attributeId":"AGE"}],"studyViewFilter":{"studyIds":[],"sampleIdentifiers":[{"sampleId":"a","studyId":"s"}]}}`,
		"/api/genomic-data-bin-counts/fetch":       `{"genomicDataBinFilters":[]}`,
		"/api/generic-assay-data-bin-counts/fetch": `{"genericAssayDataBinFilters":[]}`,
	} {
		if _, _, failed := studyViewBodies[path].check([]byte(body)); failed {
			t.Errorf("%s %s: failed", path, body)
		}
	}
}
