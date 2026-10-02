package databin

import (
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// dataBin is the mutable working form of a bin (Java DataBin).
type dataBin struct {
	specialValue *string
	start        *Decimal
	end          *Decimal
	count        int
}

func strPtr(s string) *string { return &s }

func decPtr(d Decimal) *Decimal { return &d }

func (b *dataBin) special() string {
	if b.specialValue == nil {
		return ""
	}
	return *b.specialValue
}

func (b *dataBin) hasSpecial(s string) bool {
	return b.specialValue != nil && *b.specialValue == s
}

// calcUpperOutlierBin mirrors DataBinHelper.calcUpperOutlierBin.
func calcUpperOutlierBin(gteValues, gtValues []Decimal) *dataBin {
	gteMin := minDecimal(gteValues)
	gtMin := minDecimal(gtValues)
	var start *Decimal
	var value string
	switch {
	case gtMin == nil && gteMin == nil:
		value = ">"
	case gtMin == nil || (gteMin != nil && gteMin.Cmp(*gtMin) < 0):
		start = gteMin
		value = ">="
	default:
		start = gtMin
		value = ">"
	}
	return &dataBin{count: len(gteValues) + len(gtValues), specialValue: strPtr(value), start: start}
}

// calcLowerOutlierBin mirrors DataBinHelper.calcLowerOutlierBin.
func calcLowerOutlierBin(lteValues, ltValues []Decimal) *dataBin {
	lteMax := maxDecimal(lteValues)
	ltMax := maxDecimal(ltValues)
	var end *Decimal
	var value string
	switch {
	case ltMax == nil && lteMax == nil:
		value = "<="
	case lteMax == nil || (ltMax != nil && lteMax.Cmp(*ltMax) < 0):
		end = ltMax
		value = "<"
	default:
		end = lteMax
		value = "<="
	}
	return &dataBin{count: len(lteValues) + len(ltValues), specialValue: strPtr(value), end: end}
}

// minDecimal mirrors Collections.min: the first minimal element.
func minDecimal(values []Decimal) *Decimal {
	if len(values) == 0 {
		return nil
	}
	m := values[0]
	for _, v := range values[1:] {
		if v.Cmp(m) < 0 {
			m = v
		}
	}
	return &m
}

// maxDecimal mirrors Collections.max: the first maximal element.
func maxDecimal(values []Decimal) *Decimal {
	if len(values) == 0 {
		return nil
	}
	m := values[0]
	for _, v := range values[1:] {
		if v.Cmp(m) > 0 {
			m = v
		}
	}
	return &m
}

// sortDecimals is a stable ascending sort (Collections.sort): numerically
// equal values keep their input order. Distinct values are sorted once and
// the input is then bucketed by rank, which keeps large inputs with many
// repeated values fast.
func sortDecimals(values []Decimal) []Decimal {
	if len(values) == 0 {
		return nil
	}
	index := map[decKey]int{}
	var distinct []Decimal
	ids := make([]int, len(values))
	for i, v := range values {
		k := v.key()
		id, ok := index[k]
		if !ok {
			id = len(distinct)
			index[k] = id
			distinct = append(distinct, v)
		}
		ids[i] = id
	}
	order := make([]int, len(distinct))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return distinct[order[i]].Cmp(distinct[order[j]]) < 0 })
	rank := make([]int, len(distinct))
	r := 0
	for i, id := range order {
		if i > 0 && distinct[order[i-1]].Cmp(distinct[id]) != 0 {
			r++
		}
		rank[id] = r
	}
	buckets := make([]int, r+2)
	for _, id := range ids {
		buckets[rank[id]+1]++
	}
	for i := 1; i < len(buckets); i++ {
		buckets[i] += buckets[i-1]
	}
	out := make([]Decimal, len(values))
	for i, v := range values {
		b := rank[ids[i]]
		out[buckets[b]] = v
		buckets[b]++
	}
	return out
}

// calcQuartileBoundaries mirrors DataBinHelper.calcQuartileBoundaries.
func calcQuartileBoundaries(sorted []Decimal) []Decimal {
	q1 := calcQ1(sorted)
	q2 := calcMedian(sorted)
	q3 := calcQ3(sorted)
	boundaries := []Decimal{*q1}
	if q1.Cmp(*q2) < 0 {
		boundaries = append(boundaries, *q2)
	}
	if q2.Cmp(*q3) < 0 {
		boundaries = append(boundaries, *q3)
	}
	return boundaries
}

var (
	decOneAndHalf  = MustParseDecimal("1.5")
	decOneThousand = MustParseDecimal("0.001")
	decOne         = MustParseDecimal("1")
	decZero        = MustParseDecimal("0")
	decTwoPoint0   = MustParseDecimal("2.0")
)

// calcBoxRange mirrors DataBinHelper.calcBoxRange (nil for no values).
func calcBoxRange(sorted []Decimal) *interval {
	if len(sorted) == 0 {
		return nil
	}
	iqrRange := calcInterquartileRangeApproximation(sorted)
	q1 := iqrRange.lowerEndpoint()
	q3 := iqrRange.upperEndpoint()
	iqr := q3.sub(q1)
	iqrOneAndHalf := iqr.mul(decOneAndHalf)
	q1Lower := q1.sub(iqrOneAndHalf)
	q3Upper := q3.add(iqrOneAndHalf)

	first, last := sorted[0], sorted[len(sorted)-1]
	var minValue, maxValue Decimal
	switch {
	case first.Cmp(last) == 0:
		minValue = first
		maxValue = minValue
	case q3.Cmp(decOneThousand) != -1 && q3.Cmp(decOne) < 0:
		maxValue = q3Upper.setScale(3, roundHalfUp)
		minValue = q1Lower.setScale(3, roundHalfUp)
	case q3.Cmp(decOneThousand) < 0:
		maxValue = q3Upper
		minValue = q1Lower
	default:
		maxValue = q3Upper.setScale(1, roundCeiling)
		minValue = q1Lower.setScale(1, roundFloor)
	}
	if minValue.Cmp(first) < 0 {
		minValue = first
	}
	if maxValue.Cmp(last) > 0 {
		maxValue = last
	}
	return closedRange(minValue, maxValue)
}

// calcInterquartileRangeApproximation mirrors the Java method of the same
// name, including the recursive search for a non-zero IQR.
func calcInterquartileRangeApproximation(sorted []Decimal) *interval {
	if len(sorted) == 0 {
		return nil
	}
	q1 := *valueCloseToQ1(sorted)
	q3 := *valueCloseToQ3(sorted)
	maxV := sorted[len(sorted)-1]
	var iqr *interval
	if q1.Cmp(q3) == 0 && maxV.Cmp(q3) == 0 {
		var rest []Decimal
		for _, d := range sorted {
			if d.Cmp(maxV) < 0 {
				rest = append(rest, d)
			}
		}
		iqr = calcInterquartileRangeApproximation(rest)
	}
	if iqr == nil || iqr.isEmpty() {
		iqr = closedOpenRange(q1, q3)
	}
	return iqr
}

// calcQ1 mirrors DataBinHelper.calcQ1.
func calcQ1(sorted []Decimal) *Decimal {
	if len(sorted) == 0 {
		return nil
	}
	stop := int(float64(len(sorted))*0.5) - 1
	return calcMedianRange(sorted, 0, stop)
}

// calcMedian mirrors DataBinHelper.calcMedian.
func calcMedian(sorted []Decimal) *Decimal {
	if len(sorted) == 0 {
		return nil
	}
	return calcMedianRange(sorted, 0, len(sorted)-1)
}

// calcQ3 mirrors DataBinHelper.calcQ3.
func calcQ3(sorted []Decimal) *Decimal {
	if len(sorted) == 0 {
		return nil
	}
	start := int(float64(len(sorted)) * 0.5)
	if len(sorted)%2 != 0 {
		start++
	}
	return calcMedianRange(sorted, start, len(sorted)-1)
}

func calcMedianRange(sorted []Decimal, start, stop int) *Decimal {
	if len(sorted) == 0 {
		return nil
	}
	if start < 0 || stop+1 > len(sorted) || start > stop+1 {
		panic(javaError("IndexOutOfBoundsException: subList"))
	}
	values := sorted[start : stop+1]
	index := int(float64(len(values)) * 0.5)
	if index >= len(values) {
		panic(javaError("IndexOutOfBoundsException: median of empty list"))
	}
	value := values[index]
	if len(values)%2 != 0 {
		return &value
	}
	below := values[index-1]
	m := value.add(below).divExact(decTwoPoint0)
	return &m
}

func valueCloseToQ1(sorted []Decimal) *Decimal {
	if len(sorted) == 0 {
		return nil
	}
	v := sorted[int(float64(len(sorted))*0.25)]
	return &v
}

func valueCloseToQ3(sorted []Decimal) *Decimal {
	if len(sorted) == 0 {
		return nil
	}
	v := sorted[int(float64(len(sorted))*0.75)]
	return &v
}

// filterIntervals keeps the boundaries strictly between the outlier limits.
func filterIntervals(intervals []Decimal, lowerOutlier, upperOutlier *Decimal) []Decimal {
	var out []Decimal
	for _, d := range intervals {
		if (lowerOutlier == nil || d.Cmp(*lowerOutlier) > 0) &&
			(upperOutlier == nil || d.Cmp(*upperOutlier) < 0) {
			out = append(out, d)
		}
	}
	return out
}

// initDataBinsWithCounts mirrors DataBinHelper.initDataBins(values,
// intervals): consecutive boundaries become (start, end] bins, counted
// against values.
func initDataBinsWithCounts(values []Decimal, intervals []*Decimal) []*dataBin {
	bins := initDataBinsFromIntervals(intervals)
	calcCounts(bins, values)
	return bins
}

func initDataBinsFromIntervals(intervals []*Decimal) []*dataBin {
	var bins []*dataBin
	for i := 0; i < len(intervals)-1; i++ {
		bins = append(bins, &dataBin{start: intervals[i], end: intervals[i+1]})
	}
	return bins
}

func ptrs(values []Decimal) []*Decimal {
	out := make([]*Decimal, len(values))
	for i := range values {
		out[i] = decPtr(values[i])
	}
	return out
}

// trim mirrors DataBinHelper.trim: drop leading and trailing empty bins.
func trim(bins []*dataBin) []*dataBin {
	start := 0
	for start < len(bins) && bins[start].count <= 0 {
		start++
	}
	end := len(bins)
	for end > start && bins[end-1].count <= 0 {
		end--
	}
	return append([]*dataBin(nil), bins[start:end]...)
}

// calcCounts mirrors DataBinHelper.calcCounts, including its failure when two
// bins map to the same range key.
func calcCounts(bins []*dataBin, values []Decimal) {
	seen := make(map[string]bool, len(bins))
	ranges := make([]*interval, len(bins))
	for i, b := range bins {
		r := calcRange(b)
		k := "null"
		if r != nil {
			k = r.key()
		}
		if seen[k] {
			panic(javaError("IllegalStateException: Duplicate key " + k))
		}
		seen[k] = true
		ranges[i] = r
	}
	weighted := groupDistinct(values)
	for i, r := range ranges {
		if r == nil {
			continue
		}
		for _, w := range weighted {
			if r.contains(w.d) {
				bins[i].count += w.n
			}
		}
	}
}

// weightedDecimal is a distinct value (by BigDecimal.equals) with its
// multiplicity.
type weightedDecimal struct {
	d Decimal
	n int
}

// groupDistinct collapses equal values (same value and scale) in
// first-seen order, so range tests run once per distinct value.
func groupDistinct(values []Decimal) []weightedDecimal {
	index := map[decKey]int{}
	var out []weightedDecimal
	for _, v := range values {
		k := v.key()
		if i, ok := index[k]; ok {
			out[i].n++
			continue
		}
		index[k] = len(out)
		out = append(out, weightedDecimal{d: v, n: 1})
	}
	return out
}

// calcRange mirrors DataBinHelper.calcRange(DataBin).
func calcRange(b *dataBin) *interval {
	startInclusive := b.hasSpecial(">=")
	endInclusive := !b.hasSpecial("<")
	if b.start != nil && b.end != nil && b.start.Cmp(*b.end) == 0 {
		startInclusive, endInclusive = true, true
	}
	return calcRangeBounds(b.start, startInclusive, b.end, endInclusive)
}

// calcRangeForOperator mirrors DataBinHelper.calcRange(String, BigDecimal).
func calcRangeForOperator(operator string, value Decimal) *interval {
	startInclusive := operator == ">="
	endInclusive := operator != "<"
	var start, end *Decimal
	if strings.Contains(operator, ">") {
		start = decPtr(value)
	}
	if strings.Contains(operator, "<") {
		end = decPtr(value)
	}
	return calcRangeBounds(start, startInclusive, end, endInclusive)
}

// isNA mirrors DataBinHelper.isNA.
func isNA(value string) bool {
	return equalsIgnoreCase(value, "NA") || equalsIgnoreCase(value, "NAN") || equalsIgnoreCase(value, "N/A")
}

// isSmallData mirrors DataBinHelper.isSmallData.
func isSmallData(sorted []Decimal) bool {
	idx := int(math.Ceil(float64(len(sorted)) * (1.0 / 2.0)))
	if idx >= len(sorted) {
		panic(javaError("IndexOutOfBoundsException: isSmallData"))
	}
	median := sorted[idx]
	return median.Cmp(decOneThousand) < 0 &&
		median.Cmp(MustParseDecimal("-0.001")) > 0 &&
		median.Cmp(decZero) != 0
}

func operatorLength(value string) int {
	t := javaTrim(value)
	if strings.HasPrefix(t, ">=") || strings.HasPrefix(t, "<=") {
		return 2
	}
	if strings.HasPrefix(t, ">") || strings.HasPrefix(t, "<") {
		return 1
	}
	return 0
}

// extractOperator mirrors DataBinHelper.extractOperator.
func extractOperator(value string) string {
	return javaTrim(value)[:operatorLength(value)]
}

// stripOperator mirrors DataBinHelper.stripOperator.
func stripOperator(value string) string {
	return javaTrim(value)[operatorLength(value):]
}

// calcExponent mirrors DataBinHelper.calcExponent: precision - scale - 1.
func calcExponent(d Decimal) int {
	return d.precision() - int(d.scale) - 1
}

var ageAttributePattern = regexp.MustCompile(`^(?:(^AGE$)|(^AGE_.*)|(.*_AGE_.*)|(.*_AGE&))$`)

// isAgeAttribute mirrors DataBinHelper.isAgeAttribute, including its pattern
// "(.*_AGE&)", which only matches ids ending in a literal "_AGE&".
func isAgeAttribute(attributeID string) bool {
	return ageAttributePattern.MatchString(attributeID)
}

// convertToDistinctBins mirrors DataBinHelper.convertToDistinctBins.
func convertToDistinctBins(bins []*dataBin, numericalValues []Decimal, rangeValues []*interval) []*dataBin {
	distinct := make([]*dataBin, 0, len(bins))
	weighted := groupDistinct(numericalValues)
	for _, b := range bins {
		r := calcRange(b)
		if r == nil {
			panic(javaError("NullPointerException: bin without range"))
		}
		var values []Decimal
		for _, w := range weighted {
			if r.contains(w.d) {
				values = append(values, w.d)
			}
		}
		rangeCount := 0
		if slices.ContainsFunc(rangeValues, r.encloses) {
			rangeCount++
		}
		if rangeCount == 0 && len(values) == 1 && areAllIntegers(values) {
			v := values[0]
			distinct = append(distinct, &dataBin{count: b.count, start: decPtr(v), end: decPtr(v)})
		} else {
			distinct = append(distinct, b)
		}
	}
	if areAllDistinctExceptOutliers(distinct) {
		return distinct
	}
	return bins
}

func areAllDistinctExceptOutliers(bins []*dataBin) bool {
	any := false
	all := true
	for _, b := range bins {
		if b.start == nil || b.end == nil {
			continue
		}
		any = true
		if !b.start.identical(*b.end) {
			all = false
		}
	}
	return any && all
}

// areAllIntegers mirrors DataBinHelper.areAllIntegers (false when empty).
func areAllIntegers(values []Decimal) bool {
	if len(values) == 0 {
		return false
	}
	for _, v := range values {
		if v.stripTrailingZeros().scale > 0 {
			return false
		}
	}
	return true
}

// generateBins mirrors DataBinHelper.generateBins. It returns nil for no
// values.
func generateBins(sorted []Decimal, binSize, anchorValue *Decimal) []Decimal {
	if sorted == nil {
		panic(javaError("IllegalArgumentException: sortedNumerical values is null!"))
	}
	if binSize == nil {
		panic(javaError("IllegalArgumentException: binSize values is null!"))
	}
	if anchorValue == nil {
		panic(javaError("IllegalArgumentException: anchorValue values is null!"))
	}
	if len(sorted) == 0 {
		return nil
	}
	minValue := sorted[0]
	maxValue := sorted[len(sorted)-1]
	if minValue.Cmp(maxValue) >= 1 {
		panic(javaError("IllegalArgumentException: minValue larger than maxValue. Input is not sorted in ascending order!"))
	}
	if binSize.sign() <= 0 && minValue.Cmp(maxValue) < 0 {
		// A non-positive bin size never reaches maxValue; Java loops
		// until it runs out of memory.
		panic(javaError("IllegalArgumentException: binSize must be positive"))
	}
	bins := []Decimal{}
	deltaL := anchorValue.sub(minValue)
	remainderL := deltaL.rem(*binSize)
	if remainderL.Cmp(decimalFromInt(0)) < 0 {
		remainderL = remainderL.add(*binSize)
	}
	lowerBound := minValue.add(remainderL)
	for lowerBound.Cmp(maxValue) < 0 {
		bins = append(bins, lowerBound)
		lowerBound = lowerBound.add(*binSize)
	}
	return bins
}

// javaTrim mirrors String.trim: strip leading and trailing chars <= ' '.
func javaTrim(s string) string {
	start, end := 0, len(s)
	for start < end && s[start] <= ' ' {
		start++
	}
	for end > start && s[end-1] <= ' ' {
		end--
	}
	return s[start:end]
}

// equalsIgnoreCase mirrors String.equalsIgnoreCase.
func equalsIgnoreCase(a, b string) bool {
	return strings.EqualFold(a, b)
}
