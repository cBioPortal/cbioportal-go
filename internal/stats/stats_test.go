package stats

import (
	"math"
	"testing"
)

func almostEqual(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}

// ---------------------------------------------------------------------------
// Chi-square test
// ---------------------------------------------------------------------------

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

// A small t statistic with many degrees of freedom puts the incomplete beta
// argument close to 1, where it must be evaluated through its symmetry to
// match commons-math's TDistribution (a p-value near 1 was off by up to 0.3%).
func TestTDistCDFSmallT(t *testing.T) {
	for _, c := range [][2]float64{{0.01, 198.5}, {0.067, 80}, {0.3, 40}, {1.5, 12}, {3, 150}} {
		if got, want := TDistCDF(-c[0], c[1]), cmTCdf(-c[0], c[1]); !almostEqual(got, want, 1e-12) {
			t.Errorf("TDistCDF(-%v, %v) = %.16g, want %.16g", c[0], c[1], got, want)
		}
	}
}

// Large samples with nearly equal means give a p-value close to 1.
func TestWelchTTestNearOne(t *testing.T) {
	for _, shift := range []float64{0, 0.001, 0.01, 0.05, 0.2} {
		var a, b []float64
		for i := 0; i < 45; i++ {
			a = append(a, math.Mod(float64(i)*0.37, 1)*0.1)
			b = append(b, math.Mod(float64(i)*0.61, 1)*0.07+shift*0.1)
		}
		n1, m1, v1 := sampleStats(a)
		n2, m2, v2 := sampleStats(b)
		got, want := WelchTTest(n1, m1, v1, n2, m2, v2), CommonsTTest(a, b)
		if !almostEqual(got, want, 1e-12) {
			t.Errorf("shift %v: WelchTTest = %.16g, CommonsTTest = %.16g", shift, got, want)
		}
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
