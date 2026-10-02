package stats

import (
	"math"
	"sort"
)

// Ports of the Datumbox Framework 0.8.2 statistics that cBioPortal's
// clinical-data enrichments use (KruskalWallis, Chisquare and
// ContinuousDistributions). Datumbox evaluates the chi-square distribution
// with series/continued-fraction approximations stopped at a 1e-5 relative
// tolerance and inverts it by bisection, so p-values and scores differ from an
// exact implementation in the sixth significant digit or so; these functions
// reproduce Datumbox's arithmetic step by step so the results match
// cBioPortal's.

// DatumboxChisquareCdf is ContinuousDistributions.chisquareCdf: the lower
// tail probability of a chi-square variable with df degrees of freedom.
// Datumbox rejects x < 0 and df <= 0; those return NaN here.
func DatumboxChisquareCdf(x float64, df int) float64 {
	if df <= 0 {
		return math.NaN()
	}
	return datumboxGammaCdf(x/2.0, float64(df)/2.0)
}

// DatumboxChisquareInverseCdf is ContinuousDistributions.chisquareInverseCdf:
// the chi-square value whose upper tail probability is p, found by bisection
// to within 1e-6 on [0, 99999].
func DatumboxChisquareInverseCdf(p float64, df int) float64 {
	const epsilon = 0.000001
	const chiMax = 99999.0
	minChisq := 0.0
	maxChisq := chiMax
	if p <= 0.0 {
		return chiMax
	} else if p >= 1.0 {
		return 0.0
	}
	chisqval := float64(df) / math.Sqrt(p)
	for maxChisq-minChisq > epsilon {
		if 1-DatumboxChisquareCdf(chisqval, df) < p {
			maxChisq = chisqval
		} else {
			minChisq = chisqval
		}
		chisqval = (maxChisq + minChisq) * 0.5
	}
	return chisqval
}

// datumboxGammaCdf is ContinuousDistributions.gammaCdf(x, a), including the
// Edgeworth expansion for a > 200 with its integer XOR in place of z^4.
func datumboxGammaCdf(x, a float64) float64 {
	if x < 0 {
		return math.NaN()
	}
	if a > 200 {
		z := (x - a) / math.Sqrt(a)
		y := datumboxGaussCdf(z)
		b1 := 2 / math.Sqrt(a)
		phiz := 0.39894228 * math.Exp(-z*z/2)
		w := y - b1*(z*z-1)*phiz/6
		b2 := 6 / a
		zXor4 := javaD2I(z) ^ 4
		u := 3*b2*(z*z-3) + b1*b1*(float64(zXor4)-10*z*z+15)
		return w - phiz*z*u/72
	}
	if x < a+1 {
		return datumboxGSer(x, a)
	}
	return datumboxGCf(x, a)
}

// javaD2I is Java's double-to-int conversion: truncation, saturating at the
// int range, NaN to 0.
func javaD2I(v float64) int32 {
	switch {
	case math.IsNaN(v):
		return 0
	case v >= math.MaxInt32:
		return math.MaxInt32
	case v <= math.MinInt32:
		return math.MinInt32
	}
	return int32(v)
}

func datumboxGSer(x, a float64) float64 {
	t9 := 1 / a
	g := t9
	i := 1.0
	for t9 > g*0.00001 {
		t9 = t9 * x / (a + i)
		g = g + t9
		i++
	}
	return g * math.Exp(a*math.Log(x)-x-DatumboxLogGamma(a))
}

func datumboxGCf(x, a float64) float64 {
	a0, b0, a1, b1 := 0.0, 1.0, 1.0, x
	aOld, n := 0.0, 0.0
	for math.Abs((a1-aOld)/a1) > 0.00001 {
		aOld = a1
		n = n + 1
		a0 = a1 + (n-a)*a0
		b0 = b1 + (n-a)*b0
		a1 = x*a0 + n*a1
		b1 = x*b0 + n*b1
		a0 = a0 / b1
		b0 = b0 / b1
		a1 = a1 / b1
		b1 = 1
	}
	prob := math.Exp(a*math.Log(x)-x-DatumboxLogGamma(a)) * a1
	return 1.0 - prob
}

// DatumboxLogGamma is ContinuousDistributions.logGamma (Lanczos, 6 terms).
func DatumboxLogGamma(z float64) float64 {
	s := 1.0 + 76.18009173/z - 86.50532033/(z+1.0) + 24.01409822/(z+2.0) - 1.231739516/(z+3.0) +
		0.00120858003/(z+4.0) - 0.00000536382/(z+5.0)
	return (z-0.5)*math.Log(z+4.5) - (z + 4.5) + math.Log(s*2.50662827465)
}

// datumboxGaussCdf is ContinuousDistributions.gaussCdf (polynomial
// approximation of the standard normal CDF).
func datumboxGaussCdf(z float64) float64 {
	var x float64
	if z == 0 {
		x = 0
	} else {
		y := math.Abs(z) / 2
		if y >= 3 {
			x = 1
		} else if y < 1 {
			w := y * y
			x = ((((((((0.000124818987*w-0.001075204047)*w+0.005198775019)*w-0.019198292004)*w+
				0.059054035642)*w-0.151968751364)*w+0.319152932694)*w-0.5319230073)*w + 0.797884560593) * y * 2
		} else {
			y = y - 2
			x = (((((((((((((-0.000045255659*y+0.00015252929)*y-0.000019538132)*y-0.000676904986)*y+
				0.001390604284)*y-0.00079462082)*y-0.002034254874)*y+0.006549791214)*y-0.010557625006)*y+
				0.011630447319)*y-0.009279453341)*y+0.005353579108)*y-0.002141268741)*y+0.000535310849)*y +
				0.999936657524
		}
	}
	if z > 0 {
		return (x + 1) / 2
	}
	return (1 - x) / 2
}

// DatumboxKruskalWallisFromRanks is KruskalWallis.getPvalue computed from each
// non-empty group's value count and sum of tie-averaged ranks (in group
// order) and tieSum, the sum over distinct values of t^3 - t for t values
// sharing it. H is tie-corrected and tested against chi-square with k-1
// degrees of freedom. A negative H (rounding when all groups rank alike) is
// tested as 0.
func DatumboxKruskalWallisFromRanks(counts []uint64, rankSums []float64, tieSum float64) float64 {
	k := len(counts)
	if k < 2 {
		return math.NaN()
	}
	var n int32
	for _, c := range counts {
		n += int32(c)
	}
	c := tieSum / ((float64(n*n) - 1) * float64(n))
	sum := 0.0
	for i, r := range rankSums {
		sum += r * r / float64(counts[i])
	}
	nf := float64(n)
	h := 12.0/(nf*(nf+1.0))*sum - 3.0*(nf+1.0)
	h = h / (1 - c)
	if h < 0 {
		h = 0
	}
	return 1 - DatumboxChisquareCdf(h, k-1)
}

// DatumboxKruskalWallis is KruskalWallis.getPvalue over raw group values.
func DatumboxKruskalWallis(groups [][]float64) float64 {
	type obs struct {
		v     float64
		group int
	}
	var all []obs
	for gi, g := range groups {
		for _, v := range g {
			all = append(all, obs{v, gi})
		}
	}
	sorted := make([]float64, len(all))
	for i, o := range all {
		sorted[i] = o.v
	}
	sort.Float64s(sorted)
	rankOf := map[float64]float64{}
	tieSum := 0.0
	for i := 0; i < len(sorted); {
		j := i
		for j < len(sorted) && sorted[j] == sorted[i] {
			j++
		}
		t := float64(j - i)
		rankOf[sorted[i]] = (float64(i+1) + (float64(i) + t)) / 2
		if t > 1 {
			tieSum += (t*t - 1) * t
		}
		i = j
	}
	counts := make([]uint64, len(groups))
	sums := make([]float64, len(groups))
	for _, o := range all {
		counts[o.group]++
		sums[o.group] += rankOf[o.v]
	}
	var nonEmptyCounts []uint64
	var nonEmptySums []float64
	for i := range groups {
		if counts[i] > 0 {
			nonEmptyCounts = append(nonEmptyCounts, counts[i])
			nonEmptySums = append(nonEmptySums, sums[i])
		}
	}
	return DatumboxKruskalWallisFromRanks(nonEmptyCounts, nonEmptySums, tieSum)
}

// DatumboxChisquare is Chisquare.getScoreValue and getPvalue for a groups x
// categories contingency table (rows and columns in Datumbox's iteration
// order). A 2x2 table gets Yates' continuity correction, applied as
// (|O-E| - 0.5)^2 / E without clamping at zero and skipping cells whose
// expected count is 0; larger tables use Pearson's statistic.
func DatumboxChisquare(table [][]float64) (score, pValue float64) {
	k := len(table)
	if k == 0 {
		return 0, math.NaN()
	}
	cols := len(table[0])
	rowTotals := make([]float64, k)
	colTotals := make([]float64, cols)
	total := 0.0
	for i, row := range table {
		for j, v := range row {
			rowTotals[i] += v
			colTotals[j] += v
			total += v
		}
	}
	yates := k == 2 && cols == 2
	for i, row := range table {
		for j, v := range row {
			expected := rowTotals[i] * colTotals[j] / total
			if yates {
				if expected == 0 {
					continue
				}
				d := math.Abs(v-expected) - 0.5
				score += d * d / expected
			} else {
				d := v - expected
				score += d * d / expected
			}
		}
	}
	return score, 1 - DatumboxChisquareCdf(score, (k-1)*(cols-1))
}

// JavaFisherTwoTailed is cBioPortal's FisherExactTestCalculator
// .getTwoTailedPValue(a, b, c, d): table probabilities from cumulative log
// factorials, summing the observed table and every table in either direction
// whose probability is at most the observed one. The sum is not clamped, so it
// can exceed 1 by rounding.
func JavaFisherTwoTailed(a, b, c, d int) float64 {
	n := a + b + c + d
	f := make([]float64, n+1)
	for j := 1; j <= n; j++ {
		f[j] = f[j-1] + math.Log(float64(j))
	}
	prob := func(a, b, c, d int) float64 {
		n := a + b + c + d
		p := (f[a+b] + f[c+d] + f[a+c] + f[b+d]) - (f[a] + f[b] + f[c] + f[d] + f[n])
		return math.Exp(p)
	}
	baseP := prob(a, b, c, d)
	p := baseP
	ia, ib, ic, id := a, b, c, d
	for range min(c, b) {
		ia, ib, ic, id = ia+1, ib-1, ic-1, id+1
		if t := prob(ia, ib, ic, id); t <= baseP {
			p += t
		}
	}
	ia, ib, ic, id = a, b, c, d
	for range min(a, d) {
		ia, ib, ic, id = ia-1, ib+1, ic+1, id-1
		if t := prob(ia, ib, ic, id); t <= baseP {
			p += t
		}
	}
	return p
}
