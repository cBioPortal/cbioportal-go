package comparison

import (
	"math"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/stats"
)

// TestEnrichmentPValueChiSquare pins the p-value of three or more groups:
// commons-math's chi-square test over the groups profiled for the gene, as
// Java's AlterationEnrichmentUtil computes it when every group is profiled.
func TestEnrichmentPValueChiSquare(t *testing.T) {
	all := []CountSummary{{AlteredCount: 5, ProfiledCount: 40}, {AlteredCount: 12, ProfiledCount: 35}, {AlteredCount: 2, ProfiledCount: 50}}
	want := stats.CommonsChiSquareTest([][]int64{{5, 35}, {12, 23}, {2, 48}})
	if got := enrichmentPValue(all); got != want || math.IsNaN(got) || got <= 0 || got >= 1 {
		t.Errorf("all profiled: %v, want %v", got, want)
	}

	// A group with no sample profiled for the gene is left out, not an
	// empty row that would make the test NaN (p = 1).
	partial := []CountSummary{{AlteredCount: 5, ProfiledCount: 40}, {AlteredCount: 0, ProfiledCount: 0}, {AlteredCount: 12, ProfiledCount: 35}}
	want = stats.CommonsChiSquareTest([][]int64{{5, 35}, {12, 23}})
	if got := enrichmentPValue(partial); got != want || got == 1 {
		t.Errorf("one group unprofiled: %v, want %v", got, want)
	}

	// Every profiled case altered: an empty column, NaN in commons-math, 1 as in Java.
	full := []CountSummary{{AlteredCount: 4, ProfiledCount: 4}, {AlteredCount: 3, ProfiledCount: 3}, {AlteredCount: 9, ProfiledCount: 9}}
	if got := enrichmentPValue(full); got != 1 {
		t.Errorf("all altered: %v, want 1", got)
	}
}
