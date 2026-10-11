package studyview

import (
	"encoding/json"
	"testing"
)

// The legacy gene tables list only real gene panels and have no
// numberOfAlteredCasesOnPanel, as Java's legacy ProfiledCasesCounter.
func TestLegacyGeneCounts(t *testing.T) {
	entrez := int64(7157)
	counts := []GeneAlterationCount{
		{NumberOfAlteredCases: 3, NumberOfAlteredCasesOnPanel: 3, TotalCount: 4, NumberOfProfiledCases: 10,
			MatchingGenePanelIDs: []string{"IMPACT468", "WES"}, EntrezGeneID: &entrez, HugoGeneSymbol: "TP53"},
		{NumberOfAlteredCases: 1, NumberOfAlteredCasesOnPanel: 1, TotalCount: 1, NumberOfProfiledCases: 10,
			MatchingGenePanelIDs: []string{"WES"}, EntrezGeneID: &entrez, HugoGeneSymbol: "TP53", profiledWES: 10, profiledAll: 12},
		// On no panel and no whole-exome samples: every profiled sample.
		{NumberOfAlteredCases: 1, NumberOfAlteredCasesOnPanel: 1, TotalCount: 1, NumberOfProfiledCases: 0,
			MatchingGenePanelIDs: []string{}, EntrezGeneID: &entrez, HugoGeneSymbol: "TP53", profiledAll: 12},
	}
	for i := range counts {
		counts[i] = counts[i].withEventKeys()
	}
	got, _ := json.Marshal(legacyGeneCounts(counts))
	want := `[{"numberOfAlteredCases":3,"totalCount":4,"numberOfProfiledCases":10,"matchingGenePanelIds":["IMPACT468"],"entrezGeneId":7157,"hugoGeneSymbol":"TP53","entrezGeneIds":[7157],"hugoGeneSymbols":["TP53"],"uniqueEventKey":"TP53"},` +
		`{"numberOfAlteredCases":1,"totalCount":1,"numberOfProfiledCases":10,"matchingGenePanelIds":[],"entrezGeneId":7157,"hugoGeneSymbol":"TP53","entrezGeneIds":[7157],"hugoGeneSymbols":["TP53"],"uniqueEventKey":"TP53"},` +
		`{"numberOfAlteredCases":1,"totalCount":1,"numberOfProfiledCases":12,"matchingGenePanelIds":[],"entrezGeneId":7157,"hugoGeneSymbol":"TP53","entrezGeneIds":[7157],"hugoGeneSymbols":["TP53"],"uniqueEventKey":"TP53"}]`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
