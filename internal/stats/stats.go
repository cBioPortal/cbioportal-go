// Package stats provides statistical test functions used across the
// application (Fisher's exact test, chi-square, Spearman p-value, etc.).
package stats

import (
	"math"
	"slices"
)

// ---------------------------------------------------------------------------
// Fisher's exact test (2x2 contingency tables)
// ---------------------------------------------------------------------------

// FisherExactTest computes the two-tailed p-value for a 2x2 contingency table.
// Matches Java FisherExactTestCalculator.getTwoTailedPValue(a, b, c, d).
func FisherExactTest(a, b, c, d int) float64 {
	n := a + b + c + d
	if n == 0 {
		return 1.0
	}

	// Calculate probability of current table
	p0 := HypergeometricPMF(a, a+b, a+c, n)
	if p0 == 0 {
		return 1.0
	}

	// Two-tailed: sum probabilities of all tables with P <= P(observed)
	pValue := 0.0
	row1 := a + b
	col1 := a + c

	for i := max(0, row1+col1-n); i <= min(row1, col1); i++ {
		j := row1 - i
		k := col1 - i
		l := n - row1 - k

		if j < 0 || k < 0 || l < 0 {
			continue
		}

		p := HypergeometricPMF(i, row1, col1, n)
		if p <= p0*(1.0+1e-7) { // small tolerance for floating point
			pValue += p
		}
	}

	if pValue > 1.0 {
		pValue = 1.0
	}
	return pValue
}

// HypergeometricPMF computes P(X=k) for the hypergeometric distribution.
// P(X=k) = C(K,k) * C(N-K, n-k) / C(N, n)
// where K=col1, N=total, n=row1, k=a.
func HypergeometricPMF(k, n, K, N int) float64 {
	return math.Exp(LogChoose(K, k) + LogChoose(N-K, n-k) - LogChoose(N, n))
}

// LogChoose returns log(C(n, k)).
func LogChoose(n, k int) float64 {
	if k < 0 || k > n {
		return math.Inf(-1) // log(0)
	}
	if k == 0 || k == n {
		return 0 // log(1)
	}
	v1, _ := math.Lgamma(float64(n + 1))
	v2, _ := math.Lgamma(float64(k + 1))
	v3, _ := math.Lgamma(float64(n - k + 1))
	return v1 - v2 - v3
}

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
// Kruskal-Wallis / Wilcoxon rank-sum test
// ---------------------------------------------------------------------------

// KruskalWallisTest computes the p-value for a Kruskal-Wallis H test
// (or Wilcoxon rank-sum for exactly 2 groups).
// Each element of groups is a slice of numeric observations for that group.
func KruskalWallisTest(groups [][]float64) float64 {
	k := len(groups)
	if k < 2 {
		return 1.0
	}

	// Flatten and rank all observations
	type ranked struct {
		value    float64
		groupIdx int
	}
	var all []ranked
	for gi, g := range groups {
		for _, v := range g {
			all = append(all, ranked{value: v, groupIdx: gi})
		}
	}
	n := len(all)
	if n < 2 {
		return 1.0
	}

	// Sort by value
	slices.SortFunc(all, func(a, b ranked) int {
		if a.value < b.value {
			return -1
		}
		if a.value > b.value {
			return 1
		}
		return 0
	})

	// Assign ranks with tie correction
	ranks := make([]float64, n)
	i := 0
	for i < n {
		j := i + 1
		for j < n && all[j].value == all[i].value {
			j++
		}
		avgRank := float64(i+j+1) / 2.0
		for t := i; t < j; t++ {
			ranks[t] = avgRank
		}
		i = j
	}

	// Calculate rank sums per group
	rankSum := make([]float64, k)
	groupN := make([]int, k)
	for idx, r := range all {
		rankSum[r.groupIdx] += ranks[idx]
		groupN[r.groupIdx]++
	}

	// H statistic
	N := float64(n)
	h := 0.0
	for gi := range k {
		ni := float64(groupN[gi])
		if ni == 0 {
			continue
		}
		h += (rankSum[gi] * rankSum[gi]) / ni
	}
	h = (12.0/(N*(N+1)))*h - 3.0*(N+1)

	if math.IsNaN(h) || h < 0 {
		return 1.0
	}

	df := float64(k - 1)
	pValue := 1.0 - ChiSquareCDF(h, df)
	if math.IsNaN(pValue) {
		return 1.0
	}
	return pValue
}

// ChiSquareContingency computes the chi-square p-value and test statistic
// for a general RxC contingency table (observed[row][col]).
func ChiSquareContingency(observed [][]float64) (pValue float64, statistic float64) {
	rows := len(observed)
	if rows < 2 {
		return 1.0, 0.0
	}
	cols := len(observed[0])
	if cols < 2 {
		return 1.0, 0.0
	}

	rowTotals := make([]float64, rows)
	colTotals := make([]float64, cols)
	total := 0.0
	for i := range observed {
		for j := range observed[i] {
			rowTotals[i] += observed[i][j]
			colTotals[j] += observed[i][j]
			total += observed[i][j]
		}
	}

	if total == 0 {
		return 1.0, 0.0
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

	df := float64((rows - 1) * (cols - 1))
	p := 1.0 - ChiSquareCDF(chiSq, df)
	if math.IsNaN(p) {
		return 1.0, chiSq
	}
	return p, chiSq
}

// ChiSquareInverseCDF computes the inverse CDF (quantile function) of the
// chi-square distribution using bisection.
func ChiSquareInverseCDF(p, df float64) float64 {
	if p <= 0 || p >= 1 || df <= 0 {
		return 0
	}
	lo, hi := 0.0, 1000.0
	// Expand upper bound if needed
	for ChiSquareCDF(hi, df) < p {
		hi *= 2
	}
	for range 100 {
		mid := (lo + hi) / 2
		if ChiSquareCDF(mid, df) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// ---------------------------------------------------------------------------
// T-distribution / Spearman p-value
// ---------------------------------------------------------------------------

// SpearmanPValue computes a two-tailed p-value for a Spearman correlation
// coefficient r with n observations, using the t-distribution.
func SpearmanPValue(r float64, n int) float64 {
	if n <= 2 {
		return 1.0
	}
	rSquared := r * r
	if rSquared >= 1.0 {
		return 0.0
	}
	t := r * math.Sqrt(float64(n-2)/(1.0-rSquared))
	df := float64(n - 2)
	x := math.Abs(t)
	var pValue float64
	if df > 100 {
		pValue = 2.0 * NormalCDF(-x)
	} else {
		pValue = 2.0 * TDistCDF(-x, df)
	}
	if pValue < 0 {
		pValue = 0
	}
	if pValue > 1 {
		pValue = 1
	}
	return pValue
}

// NormalCDF computes the standard normal CDF.
func NormalCDF(x float64) float64 {
	return 0.5 * math.Erfc(-x/math.Sqrt2)
}

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

// RegularizedIncompleteBeta computes I_x(a,b) using a continued fraction
// (Lentz's algorithm).
func RegularizedIncompleteBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	lbeta := lgammafn(a) + lgammafn(b) - lgammafn(a+b)
	front := math.Exp(math.Log(x)*a+math.Log(1-x)*b-lbeta) / a

	const maxIter = 200
	const epsilon = 1e-14

	f := 1.0
	c := 1.0
	d := 1.0 - (a+b)*x/(a+1)
	if math.Abs(d) < 1e-30 {
		d = 1e-30
	}
	d = 1.0 / d
	f = d

	for i := 1; i <= maxIter; i++ {
		m := float64(i)
		// Even step
		num := m * (b - m) * x / ((a + 2*m - 1) * (a + 2*m))
		d = 1.0 + num*d
		if math.Abs(d) < 1e-30 {
			d = 1e-30
		}
		c = 1.0 + num/c
		if math.Abs(c) < 1e-30 {
			c = 1e-30
		}
		d = 1.0 / d
		f *= c * d

		// Odd step
		num = -(a + m) * (a + b + m) * x / ((a + 2*m) * (a + 2*m + 1))
		d = 1.0 + num*d
		if math.Abs(d) < 1e-30 {
			d = 1e-30
		}
		c = 1.0 + num/c
		if math.Abs(c) < 1e-30 {
			c = 1e-30
		}
		d = 1.0 / d
		delta := c * d
		f *= delta
		if math.Abs(delta-1.0) < epsilon {
			break
		}
	}

	return front * f
}

func lgammafn(x float64) float64 {
	v, _ := math.Lgamma(x)
	return v
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
