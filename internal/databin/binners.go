package databin

import (
	"math"
	"math/big"
	"strconv"
)

// possibleIntervals mirrors LinearDataBinner.POSSIBLE_INTERVALS.
var possibleIntervals = []float64{
	0.001, 0.002, 0.0025, 0.005, 0.01, 0.02, 0.025, 0.05, 0.1, 0.2, 0.25, 0.5, 1.0, 2.0, 5.0, 10.0,
	20.0, 25.0, 50.0, 100.0, 200.0, 250.0, 500.0, 1000.0, 2000.0, 2500.0, 5000.0, 10000.0,
}

// possibleDiscreteIntervals mirrors LinearDataBinner.POSSIBLE_DISCRETE_INTERVALS.
var possibleDiscreteIntervals = []float64{
	1.0, 2.0, 5.0, 10.0,
	20.0, 25.0, 50.0, 100.0,
	200.0, 250.0, 500.0, 1000.0,
	2000.0, 2500.0, 5000.0, 10000.0,
}

const defaultIntervalCount = 20

// linearDataBins mirrors LinearDataBinner.calculateDataBins(areAllIntegers,
// boxRange, values, lowerOutlier, upperOutlier, attributeId).
func linearDataBins(areAllIntegers bool, boxRange *interval, values []Decimal, lowerOutlier, upperOutlier *Decimal, attributeID *string) []*dataBin {
	minV := *minDecimal(values)
	if lowerOutlier != nil {
		minV = minV.maxOf(*lowerOutlier)
	}
	maxV := *maxDecimal(values)
	if upperOutlier != nil {
		maxV = maxV.minOf(*upperOutlier)
	}

	bins := linearInitDataBins(areAllIntegers, minV, maxV, lowerOutlier, upperOutlier)

	if attributeID != nil &&
		isAgeAttribute(*attributeID) &&
		minV.Float64() < 18 &&
		boxRange.upperEndpoint().sub(boxRange.lowerEndpoint()).divExact(decimalFromInt(2)).Cmp(decimalFromInt(18)) == 1 &&
		bins[0].end.Cmp(decimalFromInt(18)) == 1 {
		bins[0].start = decPtr(decimalFromInt(18))
	}

	calcCounts(bins, values)
	return bins
}

// linearCustomDataBins mirrors LinearDataBinner.calculateDataBins(customBins,
// values).
func linearCustomDataBins(boundaries []Decimal, values []Decimal) []*dataBin {
	bins := initDataBinsFromIntervals(ptrs(boundaries))
	calcCounts(bins, values)
	return bins
}

func linearInitDataBins(areAllIntegers bool, minV, maxV Decimal, lowerOutlier, upperOutlier *Decimal) []*dataBin {
	candidates := possibleIntervals
	if areAllIntegers {
		candidates = possibleDiscreteIntervals
	}
	intervals := make([]Decimal, len(candidates))
	for i, v := range candidates {
		intervals[i] = DecimalFromFloat64(v)
	}
	interval := calcBinInterval(intervals, maxV.sub(minV), defaultIntervalCount)

	start := minV.add(interval).sub(minV.rem(interval))
	if lowerOutlier == nil || start.sub(interval).Cmp(*lowerOutlier) == 1 {
		start = start.sub(interval)
	}

	end := maxV
	if !(upperOutlier == nil || maxV.add(interval).Cmp(*upperOutlier) == -1) {
		end = maxV.sub(interval)
	}

	var bins []*dataBin
	for d := start; d.Cmp(end) != 1; {
		newEnd := d.add(interval)
		bins = append(bins, &dataBin{start: decPtr(d), end: decPtr(newEnd)})
		d = newEnd
	}
	return bins
}

// calcBinInterval mirrors LinearDataBinner.calcBinInterval.
func calcBinInterval(intervals []Decimal, totalRange Decimal, maxIntervalCount int) Decimal {
	interval := MustParseDecimal("-1.0")
	for _, iv := range intervals {
		interval = iv
		count := totalRange.divExact(iv)
		if count.Float64() < float64(maxIntervalCount) {
			break
		}
	}
	return interval
}

// logScaleDataBins mirrors LogScaleDataBinner.calculateDataBins.
func logScaleDataBins(boxRange *interval, values []Decimal, lowerOutlier, upperOutlier *Decimal) []*dataBin {
	var intervals []*Decimal
	start := MustParseDecimal("0")
	lower := boxRange.lowerEndpoint()
	if lower.Cmp(decZero) != 0 {
		absLog := javaLog10(lower.abs().Float64())
		if lower.Cmp(decZero) == -1 {
			start = DecimalFromFloat64(-math.Ceil(absLog))
		} else {
			start = DecimalFromFloat64(math.Floor(absLog))
		}
	}

	if lowerOutlier != nil {
		intervals = append(intervals, lowerOutlier)
	}

	half := MustParseDecimal("0.5")
	upper := boxRange.upperEndpoint()
	for exponent := start; ; exponent = exponent.add(half) {
		value := calcIntervalValue(exponent)
		if (lowerOutlier == nil || value.Cmp(*lowerOutlier) == 1) &&
			(upperOutlier == nil || value.Cmp(*upperOutlier) != 1) {
			intervals = append(intervals, decPtr(value))
		}
		if value.Cmp(upper) == 1 {
			exponent = exponent.add(half)
			if !addOutlierInterval(&intervals, exponent, upperOutlier) {
				intervals = append(intervals, upperOutlier)
			}
			if exponent.stripTrailingZeros().scale > 0 {
				exponent = exponent.add(half)
				addOutlierInterval(&intervals, exponent, upperOutlier)
			}
			break
		}
	}

	for _, iv := range intervals {
		if iv == nil {
			panic(javaError("NullPointerException: null log scale interval"))
		}
	}
	return initDataBinsWithCounts(values, intervals)
}

// calcIntervalValue mirrors LogScaleDataBinner.calcIntervalValue:
// signum(exponent) * floor(10^|exponent|).
func calcIntervalValue(exponent Decimal) Decimal {
	return DecimalFromFloat64(float64(exponent.sign()) * math.Floor(javaPow10(exponent.abs().Float64())))
}

func addOutlierInterval(intervals *[]*Decimal, exponent Decimal, upperOutlier *Decimal) bool {
	value := calcIntervalValue(exponent)
	if upperOutlier == nil || value.Cmp(*upperOutlier) != 1 {
		*intervals = append(*intervals, decPtr(value))
		return true
	}
	return false
}

// scientificSmallDataBins mirrors ScientificSmallDataBinner.calculateDataBins.
func scientificSmallDataBins(sorted, withoutOutliers []Decimal, lowerOutlier, upperOutlier *Decimal) []*dataBin {
	var exponents []Decimal
	for _, d := range sorted {
		e := calcExponent(d)
		if e != 0 {
			exponents = append(exponents, decimalFromInt(int64(e)))
		}
	}
	exponents = sortDecimals(exponents)
	exponentBoxRange := calcBoxRange(exponents)

	var intervals []Decimal
	var exponentRange *Decimal
	if exponentBoxRange != nil {
		exponentRange = decPtr(exponentBoxRange.upperEndpoint().sub(exponentBoxRange.lowerEndpoint()))
	}

	switch {
	case exponentRange == nil:
		intervals = append(intervals, sorted[0], sorted[len(sorted)-1])
	case exponentRange.Cmp(decOne) == 1:
		interval := javaRoundFloat(exponentRange.float32() / 4)
		if interval <= 0 {
			// Java loops forever here.
			panic(javaError("IllegalStateException: scientific binning interval is zero"))
		}
		for i := exponentBoxRange.lowerEndpoint().intValue() - interval; i <= exponentBoxRange.upperEndpoint().intValue(); i += interval {
			intervals = append(intervals, DecimalFromFloat64(javaPow10(float64(i))))
		}
	case exponentRange.Cmp(decOne) == 0:
		intervals = append(intervals, DecimalFromFloat64(javaPow10(exponentBoxRange.lowerEndpoint().Float64())/3))
		for i := exponentBoxRange.lowerEndpoint().intValue(); i <= exponentBoxRange.upperEndpoint().intValue()+1; i++ {
			powerTen := DecimalFromFloat64(javaPow10(float64(i)))
			intervals = append(intervals, powerTen, powerTen.mul(MustParseDecimal("3")))
		}
	default:
		interval := DecimalFromFloat64(2 * javaPow10(exponentBoxRange.lowerEndpoint().Float64()))
		limit := javaPow10(exponentBoxRange.upperEndpoint().Float64() + 1)
		for d := DecimalFromFloat64(javaPow10(float64(exponentBoxRange.lowerEndpoint().intValue()))); d.Float64() <= limit; d = d.add(interval) {
			intervals = append(intervals, d)
		}
	}

	return initDataBinsWithCounts(withoutOutliers, ptrs(filterIntervals(intervals, lowerOutlier, upperOutlier)))
}

// discreteDataBins mirrors DiscreteDataBinner.calculateDataBins: one
// single-value bin per unique value.
func discreteDataBins(values, uniqueValues []Decimal) []*dataBin {
	bins := make([]*dataBin, 0, len(uniqueValues))
	for _, d := range uniqueValues {
		bins = append(bins, &dataBin{start: decPtr(d), end: decPtr(d)})
	}
	calcCounts(bins, values)
	return bins
}

// javaPow10 is Math.pow(10, x). Java's result is correctly rounded for the
// arguments the binners use (integral and half-integral exponents), so those
// are computed exactly here; other arguments fall back to math.Pow.
func javaPow10(x float64) float64 {
	if math.Abs(x) > 400 {
		return math.Pow(10, x)
	}
	if x == math.Trunc(x) {
		return exactPow10(int(x))
	}
	if 2*x == math.Trunc(2*x) {
		k := int(math.Floor(x))
		sqrt10 := new(big.Float).SetPrec(300).Sqrt(new(big.Float).SetPrec(300).SetInt64(10))
		p := new(big.Float).SetPrec(300).SetInt(pow10(int64(abs(k))))
		if k >= 0 {
			sqrt10.Mul(sqrt10, p)
		} else {
			sqrt10.Quo(sqrt10, p)
		}
		f, _ := sqrt10.Float64()
		return f
	}
	return math.Pow(10, x)
}

// javaLog10 is Math.log10, which is correctly rounded on the JVM. Near
// integral results, where floor/ceil of the result is sensitive to the last
// bit, the logarithm is evaluated relative to the nearest power of ten with
// extra precision.
func javaLog10(x float64) float64 {
	l := math.Log10(x)
	if math.IsInf(l, 0) || math.IsNaN(l) || x < 1e-300 || x > 1e300 {
		return l
	}
	n := math.Round(l)
	if math.Abs(l-n) > 1e-9 {
		return l
	}
	if exactPow10(int(n)) == x {
		return n
	}
	// r - 1 = x/10^n - 1, computed exactly enough to keep full precision.
	bx := new(big.Float).SetPrec(300).SetFloat64(x)
	var p *big.Float
	if n >= 0 {
		p = new(big.Float).SetPrec(300).SetInt(pow10(int64(n)))
		bx.Quo(bx, p)
	} else {
		p = new(big.Float).SetPrec(300).SetInt(pow10(int64(-n)))
		bx.Mul(bx, p)
	}
	bx.Sub(bx, new(big.Float).SetPrec(300).SetInt64(1))
	rm1, _ := bx.Float64()
	return n + math.Log1p(rm1)/math.Ln10
}

// exactPow10 is the float64 nearest to 10^n.
func exactPow10(n int) float64 {
	f, _ := strconv.ParseFloat("1e"+strconv.Itoa(n), 64)
	return f
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// javaRoundFloat is Math.round(float): floor(x + 0.5).
func javaRoundFloat(x float32) int {
	if x != x {
		return 0
	}
	return int(math.Floor(float64(x) + 0.5))
}
