package molecular

import (
	"net/url"
	"slices"
	"testing"
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
