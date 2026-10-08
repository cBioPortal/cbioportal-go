package contract

import "testing"

// GeneServiceImpl completes a keyword page with the genes having the keyword
// as an alias, drops symbols with several entrez ids and, without a sortBy,
// sorts the page by symbol; META counts every match. With a sortBy, go
// sorts the page by it (TestGeneKeywordSortBy).
func TestOracleGeneKeywordSearch(t *testing.T) {
	requireAPI(t)
	var genes []struct {
		HugoGeneSymbol string `json:"hugoGeneSymbol"`
	}
	get(t, "/api/genes?keyword=TP&pageSize=1000", &genes)
	if len(genes) == 0 || genes[0].HugoGeneSymbol != "LCN1" {
		t.Errorf("keyword TP: alias match LCN1 not first: %v", genes[:min(3, len(genes))])
	}
	for i := 1; i < len(genes); i++ {
		if genes[i-1].HugoGeneSymbol > genes[i].HugoGeneSymbol {
			t.Fatalf("keyword page not sorted by symbol at %d", i)
		}
	}
	resp, _ := rawDo(t, "GET", "/api/genes?keyword=TP&pageSize=5&projection=META", "")
	wantEq(t, resp.Header.Get("total-count"), itoa(len(genes)))
	genes = nil
	get(t, "/api/genes?keyword=TP&pageSize=5", &genes)
	wantEq(t, len(genes), 5)
	wantEq(t, genes[0].HugoGeneSymbol, "TP53")
}
