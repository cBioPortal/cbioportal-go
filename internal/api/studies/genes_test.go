package studies

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGenesFetchCondition(t *testing.T) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(`["C11orf91", "7157", 672, "-1", "+5", "12a", ""]`), &raw); err != nil {
		t.Fatal(err)
	}
	ids := geneIDStrings(raw)
	if want := []string{"C11orf91", "7157", "672", "-1", "+5", "12a", ""}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %q, want %q", ids, want)
	}

	// ENTREZ_GENE_ID (the default): only the integers, symbols are ignored.
	cond, p := genesFetchCondition(ids, false)
	if cond != "entrez_gene_id IN (7157, 672, -1)" || p != nil {
		t.Errorf("by entrez: %q %v", cond, p)
	}
	if cond, _ := genesFetchCondition([]string{"KRAS"}, false); cond != "" {
		t.Errorf("symbols by entrez id: %q, want no condition", cond)
	}

	// HUGO_GENE_SYMBOL: every id as given, matched case-sensitively.
	cond, p = genesFetchCondition([]string{"C11orf91", "kras"}, true)
	if cond != "hugo_gene_symbol IN {symbols:Array(String)}" || !reflect.DeepEqual(p["symbols"], []string{"C11orf91", "kras"}) {
		t.Errorf("by symbol: %q %v", cond, p)
	}
	if cond, _ := genesFetchCondition(nil, true); cond != "" {
		t.Errorf("no ids: %q", cond)
	}
}

func TestUniqueSymbols(t *testing.T) {
	got := uniqueSymbols([]GeneRow{
		{EntrezGeneID: 7157, HugoGeneSymbol: "TP53"},
		{EntrezGeneID: 1, HugoGeneSymbol: "DUP"},
		{EntrezGeneID: 672, HugoGeneSymbol: "BRCA1"},
		{EntrezGeneID: 2, HugoGeneSymbol: "DUP"},
	})
	want := []GeneRow{{EntrezGeneID: 672, HugoGeneSymbol: "BRCA1"}, {EntrezGeneID: 7157, HugoGeneSymbol: "TP53"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uniqueSymbols = %v, want %v", got, want)
	}
}

func TestIsInteger(t *testing.T) {
	for id, want := range map[string]bool{"7157": true, "-1": true, "+1": false, "-": false, "": false, "1e3": false, "TP53": false} {
		if isInteger(id) != want {
			t.Errorf("isInteger(%q) = %v", id, !want)
		}
	}
}
