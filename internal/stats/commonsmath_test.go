package stats

import (
	"math"
	"testing"
)

// Expected values were produced by Apache Commons Math 3.6.1 (the version
// cBioPortal depends on) running on the same inputs. FastMath's exp/log can
// differ from Go's in the last ulp, so comparisons use closeRel.

func TestCommonsChiSquareTest(t *testing.T) {
	cases := []struct {
		table [][]int64
		want  float64
	}{
		{[][]int64{{1, 0, 14, 5}, {4, 0, 14, 2}, {6, 1, 14, 0}}, 0.09532479250348536},
		{[][]int64{{10, 20}, {30, 40}}, 0.3729984836134872},
		{[][]int64{{3, 0}, {0, 3}}, 0.01430587843542952},
		{[][]int64{{1, 1}, {1, 1}}, 1.0},
		{[][]int64{{200, 150, 50}, {180, 170, 40}}, 0.1932358916150776},
		{[][]int64{{0, 5, 1}, {7, 0, 2}, {1, 1, 9}, {4, 4, 4}}, 1.3851354866334376e-4},
		{[][]int64{{1000, 1}, {1, 1000}}, 0.0},
	}
	for _, c := range cases {
		if got := CommonsChiSquareTest(c.table); !closeRel(got, c.want) {
			t.Errorf("CommonsChiSquareTest(%v) = %v, want %v", c.table, got, c.want)
		}
	}
	for _, bad := range [][][]int64{{{1, 2}}, {{1}, {2}}, {{1, 2}, {3}}, {{1, -1}, {2, 2}}} {
		if got := CommonsChiSquareTest(bad); !math.IsNaN(got) {
			t.Errorf("CommonsChiSquareTest(%v) = %v, want NaN", bad, got)
		}
	}
}

func TestCommonsTTest(t *testing.T) {
	sbs11Rec := make([]float64, 31)
	for i := 24; i < 30; i++ {
		sbs11Rec[i] = 1
	}
	cases := []struct {
		a, b            []float64
		want, mean, std float64
	}{
		{[]float64{1, 0, 0, 1, 1, 0, 1}, []float64{0, 0, 0, 1, 0}, 0.22168235128963276, 0.5714285714285714, 0.5345224838248488},
		{[]float64{1, 1, 1, 0}, []float64{0, 0, 0, 0, 0, 1}, 0.10372121929536349, 0.75, 0.5},
		{[]float64{2.5, 3.1, 4.7, 1.2}, []float64{8.1, 7.3, 9.9, 6.0, 7.7}, 0.0017393290688108242, 2.875, 1.452297031142964},
		{sbs11Rec, make([]float64, 30), 0.011747250414768744, 0.19354838709677424, 0.4016096644512494},
	}
	for _, c := range cases {
		if got := CommonsTTest(c.a, c.b); !closeRel(got, c.want) {
			t.Errorf("CommonsTTest(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := CommonsMean(c.a); got != c.mean {
			t.Errorf("CommonsMean(%v) = %v, want %v", c.a, got, c.mean)
		}
		if got := CommonsStandardDeviation(c.a); got != c.std {
			t.Errorf("CommonsStandardDeviation(%v) = %v, want %v", c.a, got, c.std)
		}
	}
	if got := CommonsTTest([]float64{1, 1}, []float64{0, 0}); !math.IsNaN(got) {
		t.Errorf("zero-variance t-test = %v, want NaN", got)
	}
}

func TestCommonsAnovaPValue(t *testing.T) {
	cases := []struct {
		groups [][]float64
		want   float64
	}{
		{[][]float64{{1, 0, 0, 1}, {0, 0, 0, 1, 0}, {1, 1, 1, 0, 1}}, 0.19497510815109298},
		{[][]float64{{2.5, 3.1, 4.7}, {8.1, 7.3, 9.9}, {1, 2, 3, 4}}, 0.0011533781909275342},
	}
	for _, c := range cases {
		if got := CommonsAnovaPValue(c.groups); !closeRel(got, c.want) {
			t.Errorf("CommonsAnovaPValue(%v) = %v, want %v", c.groups, got, c.want)
		}
	}
}

func TestCommonsSpecialFunctions(t *testing.T) {
	for _, c := range []struct{ x, want float64 }{
		{0.1, 2.2527126517342055}, {0.5, 0.5723649429247001}, {2.0, 0.0}, {3.5, 1.2009736023470743},
		{7.9, 8.32426586800881}, {12.3, 18.238983407092242}, {150, 600.0094705553274},
	} {
		if got := CommonsLogGamma(c.x); !closeRel(got, c.want) {
			t.Errorf("CommonsLogGamma(%v) = %v, want %v", c.x, got, c.want)
		}
	}
	for _, c := range []struct{ a, x, want float64 }{
		{3, 2, 0.3233235838169365}, {3, 5.4, 0.9052421318239863}, {0.5, 0.3, 0.5614219739190001}, {25, 40, 0.9955173434344269},
	} {
		if got := CommonsRegularizedGammaP(c.a, c.x); !closeRel(got, c.want) {
			t.Errorf("CommonsRegularizedGammaP(%v, %v) = %v, want %v", c.a, c.x, got, c.want)
		}
	}
	for _, c := range []struct{ x, a, b, want float64 }{
		{0.3, 2, 3, 0.34829999999999994}, {0.9, 0.5, 14.5, 0.9999999999999996}, {0.5, 12, 0.5, 5.370758946235898e-5},
		{0.2, 1.5, 2.5, 0.2510282644167142}, {0.7, 30, 40, 0.9999986807222218},
	} {
		if got := CommonsRegularizedBeta(c.x, c.a, c.b); !closeRel(got, c.want) {
			t.Errorf("CommonsRegularizedBeta(%v, %v, %v) = %v, want %v", c.x, c.a, c.b, got, c.want)
		}
	}
}
