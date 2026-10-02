package stats

import (
	"math"
	"testing"
)

// Expected values were produced by Datumbox Framework 0.8.2 (the version
// cBioPortal depends on) running on the same inputs.

func closeRel(got, want float64) bool {
	if got == want {
		return true
	}
	return math.Abs(got-want) <= 1e-12*math.Max(math.Abs(want), 1e-300)
}

func TestDatumboxChisquareCdf(t *testing.T) {
	cases := []struct {
		x    float64
		df   int
		want float64
	}{
		{0.1, 1, 0.2481703403266343},
		{1.0, 1, 0.6826892385803818},
		{3.84, 1, 0.9499563484960182},
		{40.0, 1, 0.9999999997460371},
		{300.0, 1, 1.0},
		{0.5, 2, 0.22119920720162428},
		{7.0, 2, 0.9698026165766507},
		{2.5, 3, 0.5247086357316851},
		{0.1, 5, 1.6231661015031696e-4},
		{15.0, 5, 0.9896376617919043},
		{0.1, 10, 2.4979513312547622e-9},
		{7.0, 10, 0.2745548602253673},
		{300.0, 400, 5.709606135008902e-5},
		{450.0, 400, 0.9575006908358517},
		{500.0, 400, 0.9995177868637823},
		{1.0, 600, -5.2190790284789445e-65},
		{450.0, 600, -7.082070951091234e-6},
		{500.0, 600, 9.068726912434825e-4},
	}
	for _, c := range cases {
		if got := DatumboxChisquareCdf(c.x, c.df); !closeRel(got, c.want) {
			t.Errorf("chisquareCdf(%v, %d) = %v, want %v", c.x, c.df, got, c.want)
		}
	}
}

func TestDatumboxChisquareInverseCdf(t *testing.T) {
	cases := []struct {
		p    float64
		df   int
		want float64
	}{
		{1.0e-4, 1, 15.136705711483955},
		{0.05, 1, 3.8414630809473893},
		{0.5, 1, 0.45493679174925916},
		{0.999, 1, 1.4312272672359091e-6},
		{0.01, 2, 9.210340082645416},
		{0.9, 2, 0.21072105062439933},
		{0.3, 4, 4.878435660914834},
		{0.999, 4, 0.09080421396978226},
		{0, 3, 99999},
		{1, 3, 0},
	}
	for _, c := range cases {
		if got := DatumboxChisquareInverseCdf(c.p, c.df); !closeRel(got, c.want) {
			t.Errorf("chisquareInverseCdf(%v, %d) = %v, want %v", c.p, c.df, got, c.want)
		}
	}
}

func TestDatumboxKruskalWallis(t *testing.T) {
	cases := []struct {
		groups    [][]float64
		wantP     float64
		wantScore float64
	}{
		{[][]float64{{1, 2, 3}, {4, 5, 6}}, 0.04953473889015536, 3.8571429325875766},
		{[][]float64{{1, 1, 2, 3}, {2, 3, 3, 4, 5}}, 0.059451340781693895, 3.5526317097048943},
		{[][]float64{{50, 62, 41, 38, 55, 70}, {45, 62, 49, 38, 66, 58}}, 1.0, 0.0},
		{[][]float64{{1.5, 2.5, 2.5, 10}, {3, 3, 3}, {0.5, 7, 7, 8}}, 0.6880928020770862, 0.7476635252990639},
		{[][]float64{{1, 2}, {3, 4}, {5, 6}, {1, 6}}, 0.3263411062837507, 3.4573173760338087},
	}
	for _, c := range cases {
		p := DatumboxKruskalWallis(c.groups)
		if !closeRel(p, c.wantP) {
			t.Errorf("KruskalWallis(%v) p = %v, want %v", c.groups, p, c.wantP)
		}
		if s := DatumboxChisquareInverseCdf(p, len(c.groups)-1); !closeRel(s, c.wantScore) {
			t.Errorf("KruskalWallis(%v) score = %v, want %v", c.groups, s, c.wantScore)
		}
	}
}

func TestDatumboxChisquare(t *testing.T) {
	cases := []struct {
		table     [][]float64
		wantScore float64
		wantP     float64
	}{
		// 2x2 tables get Yates' correction, unclamped: a table matching its
		// expectation exactly still scores 4 * 0.25 / E.
		{[][]float64{{3, 3}, {4, 2}}, 0.0, 1.0},
		{[][]float64{{6, 0}, {4, 2}}, 0.6, 0.43857819070450765},
		{[][]float64{{10, 0}, {0, 10}}, 16.2, 5.6994122515585666e-5},
		{[][]float64{{1, 2, 3}, {4, 5, 0}}, 5.714285714285714, 0.05743261926957788},
		{[][]float64{{5, 1, 2}, {2, 2, 2}, {0, 4, 1}}, 7.31952380952381, 0.11993593530126512},
	}
	for _, c := range cases {
		score, p := DatumboxChisquare(c.table)
		if !closeRel(score, c.wantScore) || !closeRel(p, c.wantP) {
			t.Errorf("Chisquare(%v) = (%v, %v), want (%v, %v)", c.table, score, p, c.wantScore, c.wantP)
		}
	}
	score, p := DatumboxChisquare([][]float64{{3, 3}, {3, 3}})
	if !closeRel(score, 4*0.25/3) || !closeRel(p, 1-DatumboxChisquareCdf(1.0/3, 1)) {
		t.Errorf("unclamped Yates: got (%v, %v)", score, p)
	}
}

// Expected values from cBioPortal's FisherExactTestCalculator.
func TestJavaFisherTwoTailed(t *testing.T) {
	cases := []struct {
		a, b, c, d int
		want       float64
	}{
		{4, 1, 5, 1, 1.0000000000000013},
		{4, 1, 3, 3, 0.5454545454545459},
		{0, 0, 0, 0, 1.0},
		{10, 2, 3, 15, 5.367241191434381e-4},
		{5, 0, 0, 5, 0.007936507936507901},
		{100, 20, 30, 150, 6.483592411712565e-32},
	}
	for _, c := range cases {
		if got := JavaFisherTwoTailed(c.a, c.b, c.c, c.d); !closeRel(got, c.want) {
			t.Errorf("JavaFisherTwoTailed(%d,%d,%d,%d) = %v, want %v", c.a, c.b, c.c, c.d, got, c.want)
		}
	}
}
