package contract

import (
	"encoding/json"
	"testing"
)

// The GET endpoints that take one gene (Integer entrezGeneId) bind the first
// of repeated values, as Spring does.
func TestContractRepeatedEntrezGeneID(t *testing.T) {
	requireAPI(t)
	count := func(path string) int {
		resp, body := rawDo(t, "GET", path, "")
		wantEq(t, resp.StatusCode, 200)
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return len(rows)
	}
	const base = "/api/molecular-profiles/study_es_0_mutations/mutations?sampleListId=study_es_0_all"
	brca1, egfr := count(base+"&entrezGeneId=675"), count(base+"&entrezGeneId=55")
	if brca1 == 0 || egfr == 0 {
		t.Fatalf("fixture: %d and %d mutations", brca1, egfr)
	}
	wantEq(t, count(base+"&entrezGeneId=675&entrezGeneId=55"), brca1)
	wantEq(t, count(base+"&entrezGeneId=55&entrezGeneId=675"), egfr)
}
