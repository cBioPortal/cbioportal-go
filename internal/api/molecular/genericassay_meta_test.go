package molecular

import (
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// The generic-assay-meta endpoints leave out empty properties, as
// generic_assay_meta_derived does; the enrichments keep them, as Java's
// legacy GenericAssayMapper reads them.
func TestMetaPropertiesEmptyValues(t *testing.T) {
	var m core.JavaStringMap
	m.Set("NAME", "SBS1")
	m.Set("DESCRIPTION", "Mutational signature contribution")
	m.Set("URL", "")
	if got := nonEmpty(m); len(got.Keys) != 2 || got.Vals["NAME"] != "SBS1" {
		t.Errorf("meta endpoint properties %v", got.Vals)
	}
	got := SummaryMetaProperties(map[string]core.JavaStringMap{"e": m})["e"]
	if v, ok := got.Vals["URL"]; !ok || v != "" {
		t.Errorf("enrichment properties %v, want URL kept empty", got.Vals)
	}
}
