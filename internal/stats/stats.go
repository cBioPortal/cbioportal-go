// Package stats provides the statistical tests behind the API's enrichments:
// chi-square, Welch's t-test and one-way ANOVA p-values, ported from Java.
package stats

import (
	"math"
)

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
