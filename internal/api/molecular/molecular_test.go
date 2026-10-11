package molecular

import (
	"net/url"
	"slices"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

func TestSingleEntrezGeneID(t *testing.T) {
	for query, want := range map[string][]int64{
		"entrezGeneId=7157&entrezGeneId=5925": {7157},
		"entrezGeneId=5925&entrezGeneId=x":    {5925},
		"entrezGeneId=%207157":                {7157},
		"entrezGeneId=":                       nil,
		"":                                    nil,
	} {
		q, _ := url.ParseQuery(query)
		if got := singleEntrezGeneID(q); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", query, got, want)
		}
	}
}

// searchTerm matches the stable id, NAME and DESCRIPTION ignoring case, and
// the stable id alone for the ID projection (GetGenericAssayMetaUseCase).
func TestGenericAssayMetaSearch(t *testing.T) {
	var props core.JavaStringMap
	props.Set("NAME", "TBX15")
	props.Set("DESCRIPTION", "T-box transcription factor")
	props.Set("URL", "https://example.org/x")
	m := GenericAssayMeta{StableID: "cg00173504", EntityType: "GENERIC_ASSAY", GenericEntityMetaProperties: props}
	for _, c := range []struct {
		term   string
		idOnly bool
		want   bool
	}{
		{"tbx15", false, true},
		{"BOX TRANS", false, true},
		{"CG0017", false, true},
		{"example.org", false, false},
		{"cg00_73", false, true},
		{"tbx15", true, false},
		{"CG0017", true, true},
		{"cg00_73", true, false},
	} {
		if got := genericAssayMetaSearch(c.term, c.idOnly)(m); got != c.want {
			t.Errorf("%q (ID only %v) = %v, want %v", c.term, c.idOnly, got, c.want)
		}
	}
}
