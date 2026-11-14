package comparison

import (
	"math"
	"os"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// TestLiveRNASeqValue checks the RNA-seq transform against Java's
// ExpressionEnrichmentUtil, Math.log1p(max(x, 0)) / Math.log(2): values far
// below 1 keep their size instead of becoming 0, so a gene whose values are
// all tiny still has a spread and a p-value.
func TestLiveRNASeqValue(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	type row struct {
		X float64 `json:"x"`
		V float64 `json:"v"`
	}
	rows, err := ch.Query[row](ch.New(base, "default"),
		"SELECT x, "+rnaSeqValueSQL("x", true)+" AS v FROM (SELECT arrayJoin([1.2e-71, 3.5e-20, 0, -3, 0.5, 5.5, 1234.5678]) AS x)")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 7 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, r := range rows {
		want := math.Log1p(math.Max(r.X, 0)) / math.Log(2)
		if r.V != want && math.Abs(r.V-want) > 1e-15*math.Abs(want) {
			t.Errorf("x = %g: %g, want %g", r.X, r.V, want)
		}
		if r.X > 0 && r.V <= 0 {
			t.Errorf("x = %g became %g", r.X, r.V)
		}
	}
}
