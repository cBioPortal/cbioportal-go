package stats

import (
	"math"
	"testing"
)

func almostEqual(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}

// ---------------------------------------------------------------------------
// Fisher's exact test
// ---------------------------------------------------------------------------

func TestFisherExactTest_KnownTables(t *testing.T) {
	tests := []struct {
		name           string
		a, b, c, d     int
		expectedPValue float64
		tolerance      float64
	}{
		{
			name: "classic example",
			a:    1, b: 9, c: 11, d: 3,
			expectedPValue: 0.002759, tolerance: 0.001,
		},
		{
			name: "no association",
			a:    5, b: 5, c: 5, d: 5,
			expectedPValue: 1.0, tolerance: 0.01,
		},
		{
			name: "all in one cell",
			a:    10, b: 0, c: 0, d: 10,
			expectedPValue: 0.0, tolerance: 0.0001,
		},
		{
			name: "empty table",
			a:    0, b: 0, c: 0, d: 0,
			expectedPValue: 1.0, tolerance: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := FisherExactTest(tt.a, tt.b, tt.c, tt.d)
			if !almostEqual(p, tt.expectedPValue, tt.tolerance) {
				t.Errorf("FisherExactTest(%d,%d,%d,%d) = %f, want ~%f",
					tt.a, tt.b, tt.c, tt.d, p, tt.expectedPValue)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Chi-square test
// ---------------------------------------------------------------------------

func TestChiSquareTestCounts(t *testing.T) {
	tests := []struct {
		name     string
		altered  []int
		profiled []int
		wantLow  float64
		wantHigh float64
	}{
		{
			name:     "significant difference",
			altered:  []int{90, 10},
			profiled: []int{100, 100},
			wantLow:  0.0, wantHigh: 0.001,
		},
		{
			name:     "no difference",
			altered:  []int{50, 50},
			profiled: []int{100, 100},
			wantLow:  0.99, wantHigh: 1.01,
		},
		{
			name:     "three groups with difference",
			altered:  []int{80, 10, 10},
			profiled: []int{100, 100, 100},
			wantLow:  0.0, wantHigh: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ChiSquareTestCounts(tt.altered, tt.profiled)
			if p < tt.wantLow || p > tt.wantHigh {
				t.Errorf("ChiSquareTestCounts = %f, want in [%f, %f]",
					p, tt.wantLow, tt.wantHigh)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Spearman p-value
// ---------------------------------------------------------------------------

func TestSpearmanPValue(t *testing.T) {
	tests := []struct {
		name     string
		r        float64
		n        int
		wantLow  float64
		wantHigh float64
	}{
		{
			name: "perfect positive correlation",
			r:    1.0, n: 10,
			wantLow: 0.0, wantHigh: 0.0001,
		},
		{
			name: "no correlation",
			r:    0.0, n: 100,
			wantLow: 0.99, wantHigh: 1.01,
		},
		{
			name: "moderate positive n=30",
			r:    0.5, n: 30,
			wantLow: 0.001, wantHigh: 0.01,
		},
		{
			name: "too few observations",
			r:    0.9, n: 2,
			wantLow: 0.99, wantHigh: 1.01,
		},
		{
			name: "strong negative n=50",
			r:    -0.8, n: 50,
			wantLow: 0.0, wantHigh: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := SpearmanPValue(tt.r, tt.n)
			if p < tt.wantLow || p > tt.wantHigh {
				t.Errorf("SpearmanPValue(%f, %d) = %f, want in [%f, %f]",
					tt.r, tt.n, p, tt.wantLow, tt.wantHigh)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Kruskal-Wallis test
// ---------------------------------------------------------------------------

func TestKruskalWallisTest(t *testing.T) {
	tests := []struct {
		name     string
		groups   [][]float64
		wantLow  float64
		wantHigh float64
	}{
		{
			name:    "clearly different groups",
			groups:  [][]float64{{1, 2, 3, 4, 5}, {10, 11, 12, 13, 14}},
			wantLow: 0.0, wantHigh: 0.01,
		},
		{
			name:    "identical groups",
			groups:  [][]float64{{5, 5, 5}, {5, 5, 5}},
			wantLow: 0.99, wantHigh: 1.01,
		},
		{
			name:    "three groups with difference",
			groups:  [][]float64{{1, 2, 3}, {10, 11, 12}, {20, 21, 22}},
			wantLow: 0.0, wantHigh: 0.05,
		},
		{
			name:    "single group",
			groups:  [][]float64{{1, 2, 3}},
			wantLow: 0.99, wantHigh: 1.01,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := KruskalWallisTest(tt.groups)
			if p < tt.wantLow || p > tt.wantHigh {
				t.Errorf("KruskalWallisTest = %f, want in [%f, %f]",
					p, tt.wantLow, tt.wantHigh)
			}
		})
	}
}

func TestChiSquareContingency(t *testing.T) {
	// 2x3 table with clear association
	observed := [][]float64{
		{90, 5, 5},
		{5, 90, 5},
	}
	p, chi := ChiSquareContingency(observed)
	if p > 0.001 {
		t.Errorf("expected very small p-value, got %f", p)
	}
	if chi < 10 {
		t.Errorf("expected large chi-square statistic, got %f", chi)
	}

	// No association
	observed2 := [][]float64{
		{50, 50},
		{50, 50},
	}
	p2, _ := ChiSquareContingency(observed2)
	if p2 < 0.99 {
		t.Errorf("expected p-value ~1.0, got %f", p2)
	}
}

func TestNormalCDF(t *testing.T) {
	if !almostEqual(NormalCDF(0), 0.5, 1e-10) {
		t.Errorf("NormalCDF(0) = %f, want 0.5", NormalCDF(0))
	}
}

func sampleStats(v []float64) (int, float64, float64) {
	n := len(v)
	var s float64
	for _, x := range v {
		s += x
	}
	m := s / float64(n)
	var ss float64
	for _, x := range v {
		ss += (x - m) * (x - m)
	}
	return n, m, ss / float64(n-1)
}

// Expected values come from cbioportal's ExpressionEnrichmentServiceImplTest.
func TestWelchTTest(t *testing.T) {
	cases := []struct {
		a, b []float64
		want float64
	}{
		{[]float64{2, 3}, []float64{2.1, 3}, 0.9475795430163914},
		{[]float64{1.1, 5}, []float64{2.3, 3}, 0.8716148250471419},
	}
	for _, c := range cases {
		n1, m1, v1 := sampleStats(c.a)
		n2, m2, v2 := sampleStats(c.b)
		if got := WelchTTest(n1, m1, v1, n2, m2, v2); !almostEqual(got, c.want, 1e-12) {
			t.Errorf("WelchTTest(%v, %v) = %.16g, want %.16g", c.a, c.b, got, c.want)
		}
	}
	if p := WelchTTest(2, 1, 0, 2, 1, 0); !math.IsNaN(p) {
		t.Errorf("zero variance: got %v, want NaN", p)
	}
}

func TestOneWayAnovaP(t *testing.T) {
	// With two groups the F-test equals the pooled-variance t-test.
	a, b := []float64{1, 2, 3, 4}, []float64{2, 4, 6, 8, 10}
	n1, m1, v1 := sampleStats(a)
	n2, m2, v2 := sampleStats(b)
	df := float64(n1 + n2 - 2)
	sp := ((float64(n1-1))*v1 + float64(n2-1)*v2) / df
	tstat := (m1 - m2) / math.Sqrt(sp*(1/float64(n1)+1/float64(n2)))
	want := 2 * TDistCDF(-math.Abs(tstat), df)
	if got := OneWayAnovaP([]int{n1, n2}, []float64{m1, m2}, []float64{v1, v2}); !almostEqual(got, want, 1e-10) {
		t.Errorf("two-group ANOVA = %v, want pooled t-test %v", got, want)
	}
	// Three groups: F = 3.6 on (2, 9) degrees of freedom.
	got := OneWayAnovaP([]int{4, 4, 4}, []float64{1, 2, 3}, []float64{1, 1, 4.0 / 3})
	if got <= 0.05 || got >= 0.1 {
		t.Errorf("three-group ANOVA p = %v, want in (0.05, 0.1)", got)
	}
}
