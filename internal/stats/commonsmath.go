package stats

import "math"

// Ports of the Apache Commons Math 3.6.1 routines cBioPortal's
// ExpressionEnrichmentUtil calls for generic assay categorical and binary
// enrichments: ChiSquareTest.chiSquareTest(long[][]), TestUtils.tTest(double[],
// double[]), OneWayAnova.anovaPValue, StatUtils.mean/variance and the
// special functions behind them (Gamma, Beta, ContinuedFraction). The
// arithmetic follows the Java source step by step, so results agree with
// cBioPortal to the last few ulps (FastMath vs Go math rounding).

const cmEpsilon = 1e-14

var cmLanczosCoef = [...]float64{
	0.99999999999999709182,
	57.156235665862923517,
	-59.597960355475491248,
	14.136097974741747174,
	-0.49191381609762019978,
	.33994649984811888699e-4,
	.46523628927048575665e-4,
	-.98374475304879564677e-4,
	.15808870322491248884e-3,
	-.21026444172410488319e-3,
	.21743961811521264320e-3,
	-.16431810653676389022e-3,
	.84418223983852743293e-4,
	-.26190838401581408670e-4,
	.36899182659531622704e-5,
}

const (
	cmLanczosG    = 607.0 / 128.0
	cmSqrtTwoPi   = 2.506628274631000502
	cmHalfLog2Pi  = .9189385332046727
	cmInvA0       = .611609510448141581788e-08
	cmInvA1       = .624730830116465516210e-08
	cmInvB1       = .203610414066806987300e+00
	cmInvB2       = .266205348428949217746e-01
	cmInvB3       = .493944979382446875238e-03
	cmInvB4       = -.851419432440314906588e-05
	cmInvB5       = -.643045481779353022248e-05
	cmInvB6       = .992641840672773722196e-06
	cmInvB7       = -.607761895722825260739e-07
	cmInvB8       = .195755836614639731882e-09
	cmInvP0       = .6116095104481415817861e-08
	cmInvP1       = .6871674113067198736152e-08
	cmInvP2       = .6820161668496170657918e-09
	cmInvP3       = .4686843322948848031080e-10
	cmInvP4       = .1572833027710446286995e-11
	cmInvP5       = -.1249441572276366213222e-12
	cmInvP6       = .4343529937408594255178e-14
	cmInvQ1       = .3056961078365221025009e+00
	cmInvQ2       = .5464213086042296536016e-01
	cmInvQ3       = .4956830093825887312020e-02
	cmInvQ4       = .2692369466186361192876e-03
	cmInvC        = -.422784335098467139393487909917598e+00
	cmInvC0       = .577215664901532860606512090082402e+00
	cmInvC1       = -.655878071520253881077019515145390e+00
	cmInvC2       = -.420026350340952355290039348754298e-01
	cmInvC3       = .166538611382291489501700795102105e+00
	cmInvC4       = -.421977345555443367482083012891874e-01
	cmInvC5       = -.962197152787697356211492167234820e-02
	cmInvC6       = .721894324666309954239501034044657e-02
	cmInvC7       = -.116516759185906511211397108401839e-02
	cmInvC8       = -.215241674114950972815729963053648e-03
	cmInvC9       = .128050282388116186153198626328164e-03
	cmInvC10      = -.201348547807882386556893914210218e-04
	cmInvC11      = -.125049348214267065734535947383309e-05
	cmInvC12      = .113302723198169588237412962033074e-05
	cmInvC13      = -.205633841697760710345015413002057e-06
	cmContFracMin = 1e-50
)

var cmBetaDelta = [...]float64{
	.833333333333333333333333333333e-01,
	-.277777777777777777777777752282e-04,
	.793650793650793650791732130419e-07,
	-.595238095238095232389839236182e-09,
	.841750841750832853294451671990e-11,
	-.191752691751854612334149171243e-12,
	.641025640510325475730918472625e-14,
	-.295506514125338232839867823991e-15,
	.179643716359402238723287696452e-16,
	-.139228964661627791231203060395e-17,
	.133802855014020915603275339093e-18,
	-.154246009867966094273710216533e-19,
	.197701992980957427278370133333e-20,
	-.234065664793997056856992426667e-21,
	.171348014966398575409015466667e-22,
}

// ---------------------------------------------------------------------------
// Gamma
// ---------------------------------------------------------------------------

func cmLanczos(x float64) float64 {
	sum := 0.0
	for i := len(cmLanczosCoef) - 1; i > 0; i-- {
		sum += cmLanczosCoef[i] / (x + float64(i))
	}
	return sum + cmLanczosCoef[0]
}

// cmInvGamma1pm1 is Gamma.invGamma1pm1: 1/Gamma(1+x) - 1 for -0.5 <= x <= 1.5.
func cmInvGamma1pm1(x float64) float64 {
	if x < -0.5 || x > 1.5 {
		return math.NaN()
	}
	t := x
	if x > 0.5 {
		t = (x - 0.5) - 0.5
	}
	if t < 0.0 {
		a := cmInvA0 + t*cmInvA1
		b := cmInvB8
		b = cmInvB7 + t*b
		b = cmInvB6 + t*b
		b = cmInvB5 + t*b
		b = cmInvB4 + t*b
		b = cmInvB3 + t*b
		b = cmInvB2 + t*b
		b = cmInvB1 + t*b
		b = 1.0 + t*b
		c := cmInvC13 + t*(a/b)
		c = cmInvC12 + t*c
		c = cmInvC11 + t*c
		c = cmInvC10 + t*c
		c = cmInvC9 + t*c
		c = cmInvC8 + t*c
		c = cmInvC7 + t*c
		c = cmInvC6 + t*c
		c = cmInvC5 + t*c
		c = cmInvC4 + t*c
		c = cmInvC3 + t*c
		c = cmInvC2 + t*c
		c = cmInvC1 + t*c
		c = cmInvC + t*c
		if x > 0.5 {
			return t * c / x
		}
		return x * ((c + 0.5) + 0.5)
	}
	p := cmInvP6
	p = cmInvP5 + t*p
	p = cmInvP4 + t*p
	p = cmInvP3 + t*p
	p = cmInvP2 + t*p
	p = cmInvP1 + t*p
	p = cmInvP0 + t*p
	q := cmInvQ4
	q = cmInvQ3 + t*q
	q = cmInvQ2 + t*q
	q = cmInvQ1 + t*q
	q = 1.0 + t*q
	c := cmInvC13 + (p/q)*t
	c = cmInvC12 + t*c
	c = cmInvC11 + t*c
	c = cmInvC10 + t*c
	c = cmInvC9 + t*c
	c = cmInvC8 + t*c
	c = cmInvC7 + t*c
	c = cmInvC6 + t*c
	c = cmInvC5 + t*c
	c = cmInvC4 + t*c
	c = cmInvC3 + t*c
	c = cmInvC2 + t*c
	c = cmInvC1 + t*c
	c = cmInvC0 + t*c
	if x > 0.5 {
		return (t / x) * ((c - 0.5) - 0.5)
	}
	return x * c
}

func cmLogGamma1p(x float64) float64 {
	return -math.Log1p(cmInvGamma1pm1(x))
}

// CommonsLogGamma is Gamma.logGamma.
func CommonsLogGamma(x float64) float64 {
	switch {
	case math.IsNaN(x) || x <= 0.0:
		return math.NaN()
	case x < 0.5:
		return cmLogGamma1p(x) - math.Log(x)
	case x <= 2.5:
		return cmLogGamma1p((x - 0.5) - 0.5)
	case x <= 8.0:
		n := int(math.Floor(x - 1.5))
		prod := 1.0
		for i := 1; i <= n; i++ {
			prod *= x - float64(i)
		}
		return cmLogGamma1p(x-float64(n+1)) + math.Log(prod)
	}
	sum := cmLanczos(x)
	tmp := x + cmLanczosG + .5
	return ((x + .5) * math.Log(tmp)) - tmp + cmHalfLog2Pi + math.Log(sum/x)
}

// cmGamma is Gamma.gamma.
func cmGamma(x float64) float64 {
	if x == math.RoundToEven(x) && x <= 0.0 {
		return math.NaN()
	}
	absX := math.Abs(x)
	if absX <= 20.0 {
		if x >= 1.0 {
			prod := 1.0
			t := x
			for t > 2.5 {
				t -= 1.0
				prod *= t
			}
			return prod / (1.0 + cmInvGamma1pm1(t-1.0))
		}
		prod := x
		t := x
		for t < -0.5 {
			t += 1.0
			prod *= t
		}
		return 1.0 / (prod * (1.0 + cmInvGamma1pm1(t)))
	}
	y := absX + cmLanczosG + 0.5
	gammaAbs := cmSqrtTwoPi / absX * math.Pow(y, absX+0.5) * math.Exp(-y) * cmLanczos(absX)
	if x > 0.0 {
		return gammaAbs
	}
	return -math.Pi / (x * math.Sin(math.Pi*x) * gammaAbs)
}

// cmContinuedFraction is ContinuedFraction.evaluate (modified Lentz) with
// the given coefficient functions; NaN where Java throws a
// ConvergenceException.
func cmContinuedFraction(getA, getB func(n int, x float64) float64, x, epsilon float64) float64 {
	hPrev := getA(0, x)
	if math.Abs(hPrev) <= cmContFracMin {
		hPrev = cmContFracMin
	}
	dPrev := 0.0
	cPrev := hPrev
	hN := hPrev
	for n := 1; n < math.MaxInt32; n++ {
		a := getA(n, x)
		b := getB(n, x)
		dN := a + b*dPrev
		if math.Abs(dN) <= cmContFracMin {
			dN = cmContFracMin
		}
		cN := a + b/cPrev
		if math.Abs(cN) <= cmContFracMin {
			cN = cmContFracMin
		}
		dN = 1 / dN
		deltaN := cN * dN
		hN = hPrev * deltaN
		if math.IsInf(hN, 0) || math.IsNaN(hN) {
			return math.NaN()
		}
		if math.Abs(deltaN-1.0) < epsilon {
			break
		}
		dPrev = dN
		cPrev = cN
		hPrev = hN
	}
	return hN
}

// CommonsRegularizedGammaP is Gamma.regularizedGammaP(a, x) with the default
// 1e-14 epsilon.
func CommonsRegularizedGammaP(a, x float64) float64 {
	switch {
	case math.IsNaN(a) || math.IsNaN(x) || a <= 0.0 || x < 0.0:
		return math.NaN()
	case x == 0.0:
		return 0.0
	case x >= a+1:
		return 1.0 - CommonsRegularizedGammaQ(a, x)
	}
	n := 0.0
	an := 1.0 / a
	sum := an
	for math.Abs(an/sum) > cmEpsilon && sum < math.Inf(1) {
		n += 1.0
		an *= x / (a + n)
		sum += an
	}
	if math.IsInf(sum, 0) {
		return 1.0
	}
	return math.Exp(-x+(a*math.Log(x))-CommonsLogGamma(a)) * sum
}

// CommonsRegularizedGammaQ is Gamma.regularizedGammaQ(a, x).
func CommonsRegularizedGammaQ(a, x float64) float64 {
	switch {
	case math.IsNaN(a) || math.IsNaN(x) || a <= 0.0 || x < 0.0:
		return math.NaN()
	case x == 0.0:
		return 1.0
	case x < a+1.0:
		return 1.0 - CommonsRegularizedGammaP(a, x)
	}
	cf := cmContinuedFraction(
		func(n int, x float64) float64 { return ((2.0 * float64(n)) + 1.0) - a + x },
		func(n int, x float64) float64 { return float64(n) * (a - float64(n)) },
		x, cmEpsilon)
	ret := 1.0 / cf
	return math.Exp(-x+(a*math.Log(x))-CommonsLogGamma(a)) * ret
}

// ---------------------------------------------------------------------------
// Beta
// ---------------------------------------------------------------------------

func cmLogGammaSum(a, b float64) float64 {
	x := (a - 1.0) + (b - 1.0)
	switch {
	case x <= 0.5:
		return cmLogGamma1p(1.0 + x)
	case x <= 1.5:
		return cmLogGamma1p(x) + math.Log1p(x)
	}
	return cmLogGamma1p(x-1.0) + math.Log(x*(1.0+x))
}

func cmLogGammaMinusLogGammaSum(a, b float64) float64 {
	var d, w float64
	if a <= b {
		d = b + (a - 0.5)
		w = cmDeltaMinusDeltaSum(a, b)
	} else {
		d = a + (b - 0.5)
		w = cmDeltaMinusDeltaSum(b, a)
	}
	u := d * math.Log1p(a/b)
	v := a * (math.Log(b) - 1.0)
	if u <= v {
		return (w - u) - v
	}
	return (w - v) - u
}

func cmDeltaMinusDeltaSum(a, b float64) float64 {
	h := a / b
	p := h / (1.0 + h)
	q := 1.0 / (1.0 + h)
	q2 := q * q
	var s [len(cmBetaDelta)]float64
	s[0] = 1.0
	for i := 1; i < len(s); i++ {
		s[i] = 1.0 + (q + q2*s[i-1])
	}
	sqrtT := 10.0 / b
	t := sqrtT * sqrtT
	w := cmBetaDelta[len(cmBetaDelta)-1] * s[len(s)-1]
	for i := len(cmBetaDelta) - 2; i >= 0; i-- {
		w = t*w + cmBetaDelta[i]*s[i]
	}
	return w * p / b
}

func cmSumDeltaMinusDeltaSum(p, q float64) float64 {
	a := math.Min(p, q)
	b := math.Max(p, q)
	sqrtT := 10.0 / a
	t := sqrtT * sqrtT
	z := cmBetaDelta[len(cmBetaDelta)-1]
	for i := len(cmBetaDelta) - 2; i >= 0; i-- {
		z = t*z + cmBetaDelta[i]
	}
	return z/a + cmDeltaMinusDeltaSum(a, b)
}

// CommonsLogBeta is Beta.logBeta.
func CommonsLogBeta(p, q float64) float64 {
	if math.IsNaN(p) || math.IsNaN(q) || p <= 0.0 || q <= 0.0 {
		return math.NaN()
	}
	a := math.Min(p, q)
	b := math.Max(p, q)
	switch {
	case a >= 10.0:
		w := cmSumDeltaMinusDeltaSum(a, b)
		h := a / b
		c := h / (1.0 + h)
		u := -(a - 0.5) * math.Log(c)
		v := b * math.Log1p(h)
		if u <= v {
			return (((-0.5*math.Log(b) + cmHalfLog2Pi) + w) - u) - v
		}
		return (((-0.5*math.Log(b) + cmHalfLog2Pi) + w) - v) - u
	case a > 2.0:
		if b > 1000.0 {
			n := int(math.Floor(a - 1.0))
			prod := 1.0
			ared := a
			for i := 0; i < n; i++ {
				ared -= 1.0
				prod *= ared / (1.0 + ared/b)
			}
			return (math.Log(prod) - float64(n)*math.Log(b)) +
				(CommonsLogGamma(ared) + cmLogGammaMinusLogGammaSum(ared, b))
		}
		prod1 := 1.0
		ared := a
		for ared > 2.0 {
			ared -= 1.0
			h := ared / b
			prod1 *= h / (1.0 + h)
		}
		if b < 10.0 {
			prod2 := 1.0
			bred := b
			for bred > 2.0 {
				bred -= 1.0
				prod2 *= bred / (ared + bred)
			}
			return math.Log(prod1) + math.Log(prod2) +
				(CommonsLogGamma(ared) + (CommonsLogGamma(bred) - cmLogGammaSum(ared, bred)))
		}
		return math.Log(prod1) + CommonsLogGamma(ared) + cmLogGammaMinusLogGammaSum(ared, b)
	case a >= 1.0:
		if b > 2.0 {
			if b < 10.0 {
				prod := 1.0
				bred := b
				for bred > 2.0 {
					bred -= 1.0
					prod *= bred / (a + bred)
				}
				return math.Log(prod) + (CommonsLogGamma(a) + (CommonsLogGamma(bred) - cmLogGammaSum(a, bred)))
			}
			return CommonsLogGamma(a) + cmLogGammaMinusLogGammaSum(a, b)
		}
		return CommonsLogGamma(a) + CommonsLogGamma(b) - cmLogGammaSum(a, b)
	}
	if b >= 10.0 {
		return CommonsLogGamma(a) + cmLogGammaMinusLogGammaSum(a, b)
	}
	return math.Log(cmGamma(a) * cmGamma(b) / cmGamma(a+b))
}

// CommonsRegularizedBeta is Beta.regularizedBeta(x, a, b) with the default
// 1e-14 epsilon.
func CommonsRegularizedBeta(x, a, b float64) float64 {
	if math.IsNaN(x) || math.IsNaN(a) || math.IsNaN(b) || x < 0 || x > 1 || a <= 0 || b <= 0 {
		return math.NaN()
	}
	if x > (a+1)/(2+b+a) && 1-x <= (b+1)/(2+b+a) {
		return 1 - CommonsRegularizedBeta(1-x, b, a)
	}
	getB := func(n int, x float64) float64 {
		if n%2 == 0 {
			m := float64(n) / 2.0
			return (m * (b - m) * x) / ((a + (2 * m) - 1) * (a + (2 * m)))
		}
		m := (float64(n) - 1.0) / 2.0
		return -((a + m) * (a + b + m) * x) / ((a + (2 * m)) * (a + (2 * m) + 1.0))
	}
	getA := func(int, float64) float64 { return 1.0 }
	return math.Exp((a*math.Log(x))+(b*math.Log1p(-x))-math.Log(a)-CommonsLogBeta(a, b)) *
		1.0 / cmContinuedFraction(getA, getB, x, cmEpsilon)
}

// ---------------------------------------------------------------------------
// Descriptive statistics and tests
// ---------------------------------------------------------------------------

// CommonsMean is StatUtils.mean: the sum divided by n, plus the mean residual
// as a correction term. NaN for no values.
func CommonsMean(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	n := float64(len(values))
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	xbar := sum / n
	correction := 0.0
	for _, v := range values {
		correction += v - xbar
	}
	return xbar + (correction / n)
}

// CommonsVariance is StatUtils.variance: the bias-corrected two-pass
// variance; 0 for one value, NaN for none.
func CommonsVariance(values []float64) float64 {
	switch len(values) {
	case 0:
		return math.NaN()
	case 1:
		return 0
	}
	m := CommonsMean(values)
	accum, accum2 := 0.0, 0.0
	for _, v := range values {
		dev := v - m
		accum += dev * dev
		accum2 += dev
	}
	n := float64(len(values))
	return (accum - (accum2 * accum2 / n)) / (n - 1.0)
}

// CommonsStandardDeviation is DescriptiveStatistics.getStandardDeviation.
func CommonsStandardDeviation(values []float64) float64 {
	switch len(values) {
	case 0:
		return math.NaN()
	case 1:
		return 0
	}
	return math.Sqrt(CommonsVariance(values))
}

// CommonsChiSquareTest is ChiSquareTest.chiSquareTest(long[][]): the p-value
// of Pearson's chi-square test of independence on a rows x columns table of
// counts. NaN where Java rejects the table (fewer than two rows or columns,
// ragged rows, negative counts).
func CommonsChiSquareTest(counts [][]int64) float64 {
	if len(counts) < 2 || len(counts[0]) < 2 {
		return math.NaN()
	}
	nRows, nCols := len(counts), len(counts[0])
	for _, row := range counts {
		if len(row) != nCols {
			return math.NaN()
		}
		for _, c := range row {
			if c < 0 {
				return math.NaN()
			}
		}
	}
	rowSum := make([]float64, nRows)
	colSum := make([]float64, nCols)
	total := 0.0
	for r := 0; r < nRows; r++ {
		for c := 0; c < nCols; c++ {
			rowSum[r] += float64(counts[r][c])
			colSum[c] += float64(counts[r][c])
			total += float64(counts[r][c])
		}
	}
	sumSq := 0.0
	for r := 0; r < nRows; r++ {
		for c := 0; c < nCols; c++ {
			expected := (rowSum[r] * colSum[c]) / total
			d := float64(counts[r][c]) - expected
			sumSq += (d * d) / expected
		}
	}
	df := (float64(nRows) - 1) * (float64(nCols) - 1)
	return 1 - cmChiSquaredCdf(sumSq, df)
}

// cmChiSquaredCdf is ChiSquaredDistribution(df).cumulativeProbability, i.e.
// GammaDistribution(df/2, 2).cumulativeProbability.
func cmChiSquaredCdf(x, df float64) float64 {
	if x <= 0 {
		return 0
	}
	return CommonsRegularizedGammaP(df/2, x/2)
}

// CommonsTTest is TestUtils.tTest(sample1, sample2): the two-sided p-value of
// Welch's unequal-variance t-test. NaN when either sample has fewer than two
// values or the statistic is undefined.
func CommonsTTest(sample1, sample2 []float64) float64 {
	if len(sample1) < 2 || len(sample2) < 2 {
		return math.NaN()
	}
	m1, m2 := CommonsMean(sample1), CommonsMean(sample2)
	v1, v2 := CommonsVariance(sample1), CommonsVariance(sample2)
	n1, n2 := float64(len(sample1)), float64(len(sample2))
	t := math.Abs((m1 - m2) / math.Sqrt((v1/n1)+(v2/n2)))
	df := (((v1 / n1) + (v2 / n2)) * ((v1 / n1) + (v2 / n2))) /
		((v1*v1)/(n1*n1*(n1-1)) + (v2*v2)/(n2*n2*(n2-1)))
	if df <= 0 {
		return math.NaN()
	}
	return 2.0 * cmTCdf(-t, df)
}

// cmTCdf is TDistribution(df).cumulativeProbability.
func cmTCdf(x, df float64) float64 {
	if x == 0 {
		return 0.5
	}
	t := CommonsRegularizedBeta(df/(df+(x*x)), 0.5*df, 0.5)
	if x < 0.0 {
		return 0.5 * t
	}
	return 1.0 - 0.5*t
}

// CommonsAnovaPValue is OneWayAnova.anovaPValue: the p-value of a one-way
// ANOVA F-test over the groups. NaN with fewer than two groups or a group
// with fewer than two values.
func CommonsAnovaPValue(groups [][]float64) float64 {
	if len(groups) < 2 {
		return math.NaN()
	}
	dfwg := 0
	var sswg, totsum, totsumsq float64
	totnum := 0
	for _, g := range groups {
		if len(g) <= 1 {
			return math.NaN()
		}
		sum, sumsq := 0.0, 0.0
		for _, v := range g {
			sum += v
			sumsq += v * v
		}
		num := len(g)
		totnum += num
		totsum += sum
		totsumsq += sumsq
		dfwg += num - 1
		sswg += sumsq - ((sum * sum) / float64(num))
	}
	sst := totsumsq - ((totsum * totsum) / float64(totnum))
	ssbg := sst - sswg
	dfbg := len(groups) - 1
	msbg := ssbg / float64(dfbg)
	mswg := sswg / float64(dfwg)
	f := msbg / mswg
	return 1.0 - cmFCdf(f, float64(dfbg), float64(dfwg))
}

// cmFCdf is FDistribution(n, m).cumulativeProbability.
func cmFCdf(x, n, m float64) float64 {
	if x <= 0 {
		return 0
	}
	return CommonsRegularizedBeta((n*x)/(m+n*x), 0.5*n, 0.5*m)
}
