// Package stats provides the statistical tests behind the API's enrichments:
// chi-square, Welch's t-test and one-way ANOVA p-values, ported from Java.
package stats

import (
	"math"
)

// ---------------------------------------------------------------------------
// Chi-square test
// ---------------------------------------------------------------------------

// ChiSquareTestCounts computes the chi-square p-value for a Kx2 contingency
// table given slices of altered/profiled counts. Each element is one group.
func ChiSquareTestCounts(altered, profiled []int) float64 {
	rows := len(altered)
	if rows < 2 {
		return 1.0
	}

	observed := make([][]float64, rows)
	for i := range rows {
		observed[i] = []float64{float64(altered[i]), float64(profiled[i] - altered[i])}
	}
	return chiSquare(observed)
}

// chiSquare computes the p-value for an Rx2 observed frequency table.
func chiSquare(observed [][]float64) float64 {
	rows := len(observed)
	if rows < 2 {
		return 1.0
	}

	rowTotals := make([]float64, rows)
	colTotals := []float64{0, 0}
	total := 0.0
	for i := range observed {
		for j := range observed[i] {
			rowTotals[i] += observed[i][j]
			colTotals[j] += observed[i][j]
			total += observed[i][j]
		}
	}

	if total == 0 {
		return 1.0
	}

	chiSq := 0.0
	for i := range observed {
		for j := range observed[i] {
			expected := rowTotals[i] * colTotals[j] / total
			if expected > 0 {
				diff := observed[i][j] - expected
				chiSq += diff * diff / expected
			}
		}
	}

	df := float64(rows - 1)
	pValue := 1.0 - ChiSquareCDF(chiSq, df)
	if math.IsNaN(pValue) {
		return 1.0
	}
	return pValue
}

// ChiSquareCDF computes the chi-square CDF using the regularized incomplete
// gamma function.
func ChiSquareCDF(x, df float64) float64 {
	if x <= 0 {
		return 0
	}
	return RegularizedGammaP(df/2.0, x/2.0)
}

// RegularizedGammaP computes the regularized lower incomplete gamma function P(a,x).
func RegularizedGammaP(a, x float64) float64 {
	if x < 0 || a <= 0 {
		return 0
	}
	if x == 0 {
		return 0
	}

	if x < a+1 {
		return GammaSeriesP(a, x)
	}
	return 1.0 - GammaCFQ(a, x)
}

// GammaSeriesP computes the regularized gamma P via its series representation.
func GammaSeriesP(a, x float64) float64 {
	const maxIter = 200
	const epsilon = 1e-10

	lgammaA, _ := math.Lgamma(a)
	ap := a
	sum := 1.0 / a
	del := sum

	for n := 1; n <= maxIter; n++ {
		ap += 1.0
		del *= x / ap
		sum += del
		if math.Abs(del) < math.Abs(sum)*epsilon {
			break
		}
	}

	return sum * math.Exp(-x+a*math.Log(x)-lgammaA)
}

// GammaCFQ computes the complementary regularized gamma Q=1-P via continued
// fraction.
func GammaCFQ(a, x float64) float64 {
	const maxIter = 200
	const epsilon = 1e-10
	const tiny = 1e-30

	lgammaA, _ := math.Lgamma(a)

	b := x + 1.0 - a
	c := 1.0 / tiny
	d := 1.0 / b
	h := d

	for i := 1; i <= maxIter; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2.0
		d = an*d + b
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = b + an/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1.0 / d
		del := d * c
		h *= del
		if math.Abs(del-1.0) < epsilon {
			break
		}
	}

	return math.Exp(-x+a*math.Log(x)-lgammaA) * h
}

// ---------------------------------------------------------------------------
// T-distribution
// ---------------------------------------------------------------------------

// TDistCDF approximates the t-distribution CDF using the regularized
// incomplete beta function.
func TDistCDF(t float64, df float64) float64 {
	x := df / (df + t*t)
	ib := RegularizedIncompleteBeta(x, df/2.0, 0.5)
	if t >= 0 {
		return 1.0 - 0.5*ib
	}
	return 0.5 * ib
}

// RegularizedIncompleteBeta computes I_x(a,b) as commons-math's
// Beta.regularizedBeta does (see CommonsRegularizedBeta), which evaluates the
// continued fraction on the side of the symmetry I_x(a,b) = 1 - I_{1-x}(b,a)
// where it converges.
func RegularizedIncompleteBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	return CommonsRegularizedBeta(x, a, b)
}

// WelchTTest returns the two-sided p-value of Welch's unequal-variance t-test
// from each sample's size, mean and sample variance (commons-math
// TestUtils.tTest). It is NaN when the statistic is undefined, e.g. when both
// variances are zero.
func WelchTTest(n1 int, mean1, var1 float64, n2 int, mean2, var2 float64) float64 {
	a, b := var1/float64(n1), var2/float64(n2)
	se := a + b
	if n1 < 2 || n2 < 2 || se <= 0 {
		return math.NaN()
	}
	t := (mean1 - mean2) / math.Sqrt(se)
	df := se * se / (a*a/float64(n1-1) + b*b/float64(n2-1))
	return 2 * TDistCDF(-math.Abs(t), df)
}

// OneWayAnovaP returns the p-value of a one-way ANOVA F-test from each
// group's size, mean and sample variance (commons-math OneWayAnova).
func OneWayAnovaP(ns []int, means, vars []float64) float64 {
	k := len(ns)
	total, sum := 0, 0.0
	for i, n := range ns {
		total += n
		sum += float64(n) * means[i]
	}
	if k < 2 || total <= k {
		return math.NaN()
	}
	grand := sum / float64(total)
	var ssb, ssw float64
	for i, n := range ns {
		d := means[i] - grand
		ssb += float64(n) * d * d
		ssw += float64(n-1) * vars[i]
	}
	dfb, dfw := float64(k-1), float64(total-k)
	if ssw <= 0 {
		return math.NaN()
	}
	f := (ssb / dfb) / (ssw / dfw)
	// Upper tail of F(dfb, dfw): I_{dfw/(dfw+dfb*f)}(dfw/2, dfb/2).
	return RegularizedIncompleteBeta(dfw/(dfw+dfb*f), dfw/2, dfb/2)
}
