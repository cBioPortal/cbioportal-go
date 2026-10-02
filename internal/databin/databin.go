// Package databin is a faithful port of cBioPortal's study-view numeric data
// binning (org.cbioportal.legacy.web.util.DataBinner and its helpers
// DataBinHelper, LinearDataBinner, LogScaleDataBinner,
// ScientificSmallDataBinner and DiscreteDataBinner). It is pure computation:
// no database and no HTTP.
//
// # Contract
//
// Calculate(attrID, unfiltered, filtered, opts) produces the bins for one
// attribute, matching ClinicalDataBin lists returned by the Java endpoints
// /clinical-data-bin-counts/fetch, /genomic-data-bin-counts/fetch, etc.
//
//   - unfiltered holds the attribute's values for the study/sample selection
//     only (Java's "partial filter": study ids + sample identifiers), and
//     filtered holds the values for the full study-view filter (with the
//     attribute's own filter removed when the caller removes "self").
//   - Method STATIC (the frontend default): bins are computed from
//     unfiltered, then every bin is recounted against filtered. Bin edges thus
//     stay stable while the user filters. When unfiltered is empty the result
//     is empty.
//   - Method DYNAMIC: bins are computed from filtered alone; unfiltered is
//     ignored. When filtered is empty the result is empty.
//
// Each Value is a raw attribute value as stored ("42", "40.0", ">90", "<18",
// "NA", "Unknown") with the number of cases that carry it; Java expands
// clinical-data-counts rows into one datum per case, and so does this
// package. Cases without data must be passed as "NA" values: the NA bin
// counts values equal (ignoring case) to "NA", "NAN" or "N/A". The order of
// values matters the same way it does in Java: non-numeric special-value bins
// appear in first-seen order, and among numerically equal values with
// different scales ("40" vs "40.0") the first one seen supplies the bin edge.
//
// Edges are java.math.BigDecimal-exact Decimals whose JSON form is
// BigDecimal.toString, so bin starts/ends render exactly like Jackson renders
// them ("40.0", "1.0E+8"). Where Java throws (for example a value that
// commons-lang NumberUtils.isCreatable accepts but BigDecimal cannot parse,
// such as "0x1A" or "5L", or a degenerate range Guava rejects), Calculate
// returns an error; the Java endpoint would answer HTTP 500.
package databin

import (
	"fmt"
	"strings"
)

// Method selects how bins relate to the filtered data.
type Method string

const (
	// Static computes bins from the unfiltered data and counts the filtered
	// data into them.
	Static Method = "STATIC"
	// Dynamic computes bins and counts from the filtered data.
	Dynamic Method = "DYNAMIC"
)

// BinMethod is DataBinFilter.BinMethod.
type BinMethod string

const (
	// BinMethodCustom uses Options.CustomBins as boundaries when given and
	// otherwise falls back to automatic binning. It is Java's default.
	BinMethodCustom BinMethod = "CUSTOM"
	// BinMethodGenerate builds equal-width bins from BinsGeneratorConfig.
	BinMethodGenerate BinMethod = "GENERATE"
	// BinMethodMedian splits at the median into two outlier bins.
	BinMethodMedian BinMethod = "MEDIAN"
	// BinMethodQuartile uses Q1, Q2 and Q3 as boundaries.
	BinMethodQuartile BinMethod = "QUARTILE"
	// BinMethodNone is an explicit JSON null: automatic binning only.
	BinMethodNone BinMethod = "NONE"
)

// DefaultDistinctValueThreshold is the number of distinct values at or below
// which every distinct value gets its own bin.
const DefaultDistinctValueThreshold = 10

// BinsGeneratorConfig mirrors the Java class of the same name. A nil field is
// an error, as in Java.
type BinsGeneratorConfig struct {
	BinSize     *Decimal `json:"binSize"`
	AnchorValue *Decimal `json:"anchorValue"`
}

// Options carries the DataBinFilter fields that influence binning.
type Options struct {
	// Method is STATIC or DYNAMIC; empty means STATIC.
	Method Method
	// BinMethod is CUSTOM, GENERATE, MEDIAN, QUARTILE or NONE; empty means
	// CUSTOM, the Java default when the field is absent.
	BinMethod BinMethod
	// CustomBins are the user-chosen boundaries (nil when not provided).
	CustomBins []Decimal
	// BinsGeneratorConfig is used with BinMethodGenerate.
	BinsGeneratorConfig *BinsGeneratorConfig
	// DisableLogScale turns off log-scale binning for wide ranges.
	DisableLogScale bool
	// Start and End restrict binning to numeric values within [Start, End];
	// when either is set no special-value or NA bins are produced.
	Start, End *Decimal
	// DistinctValueThreshold defaults to DefaultDistinctValueThreshold.
	DistinctValueThreshold int
	// NonClinical marks genomic, generic-assay and other non-clinical
	// binning, which Java runs without an attribute id: the age-specific
	// first bin (see Calculate) is never applied.
	NonClinical bool
}

// Value is one raw attribute value and the number of cases carrying it.
type Value struct {
	Value string
	Count int
}

// Bin mirrors ClinicalDataBin. Nil fields are omitted from JSON, matching the
// Jackson NON_NULL configuration of the Java API.
type Bin struct {
	AttributeID  string   `json:"attributeId"`
	SpecialValue *string  `json:"specialValue,omitempty"`
	Start        *Decimal `json:"start,omitempty"`
	End          *Decimal `json:"end,omitempty"`
	Count        int      `json:"count"`
}

// Special returns the special value or "".
func (b Bin) Special() string {
	if b.SpecialValue == nil {
		return ""
	}
	return *b.SpecialValue
}

// javaError is raised (via panic) where the Java implementation throws; it
// is recovered by Calculate and returned as an error.
type javaError string

func (e javaError) Error() string { return "databin: " + string(e) }

// Calculate computes the bins for one attribute; see the package
// documentation for the meaning of unfiltered and filtered. attrID is copied
// into every Bin. For clinical attributes it also drives the age-specific
// first bin: automatic linear bins of an attribute whose id is AGE, AGE_* or
// *_AGE_* start at 18 when the data reaches below 18 and the range is wide.
func Calculate(attrID string, unfiltered, filtered []Value, opts Options) (bins []Bin, err error) {
	defer func() {
		if r := recover(); r != nil {
			bins = nil
			if je, ok := r.(javaError); ok {
				err = je
				return
			}
			err = fmt.Errorf("databin: %v", r)
		}
	}()

	threshold := opts.DistinctValueThreshold
	if threshold == 0 {
		threshold = DefaultDistinctValueThreshold
	}
	f := filter{
		attributeID:         &attrID,
		nonClinical:         opts.NonClinical,
		start:               opts.Start,
		end:                 opts.End,
		customBins:          opts.CustomBins,
		binMethod:           opts.BinMethod,
		binsGeneratorConfig: opts.BinsGeneratorConfig,
		disableLogScale:     opts.DisableLogScale,
	}
	if f.binMethod == "" {
		f.binMethod = BinMethodCustom
	}

	var raw []*dataBin
	switch opts.Method {
	case Dynamic:
		data := expand(filtered)
		if len(data) == 0 {
			return []Bin{}, nil
		}
		raw = calculateDataBins(f, data, calcNaDataBin(data), threshold)
	case Static, "":
		data := expand(unfiltered)
		if len(data) == 0 {
			return []Bin{}, nil
		}
		filteredData := expand(filtered)
		raw = calculateDataBins(f, data, calcNaDataBin(data), threshold)
		raw = recalcBinCount(raw, filteredData, countNAs(filteredData))
	default:
		return nil, fmt.Errorf("databin: unknown data bin method %q", opts.Method)
	}

	out := make([]Bin, 0, len(raw))
	for _, b := range raw {
		out = append(out, Bin{
			AttributeID:  attrID,
			SpecialValue: b.specialValue,
			Start:        b.start,
			End:          b.end,
			Count:        b.count,
		})
	}
	return out, nil
}

func expand(values []Value) []string {
	n := 0
	for _, v := range values {
		if v.Count > 0 {
			n += v.Count
		}
	}
	out := make([]string, 0, n)
	for _, v := range values {
		for i := 0; i < v.Count; i++ {
			out = append(out, v.Value)
		}
	}
	return out
}

// filter mirrors the DataBinFilter fields.
type filter struct {
	attributeID         *string
	start, end          *Decimal
	customBins          []Decimal
	binMethod           BinMethod
	binsGeneratorConfig *BinsGeneratorConfig
	disableLogScale     bool
	nonClinical         bool
}

// recalcBinCount mirrors DataBinner.recalcBinCount.
func recalcBinCount(bins []*dataBin, data []string, naCount int) []*dataBin {
	numericalValues := groupDistinct(filterNumericalValues(data))
	nonNumericalValues := filterNonNumericalValues(data)
	ranges := filterSpecialRanges(data)

	for _, b := range bins {
		b.count = 0
		r := calcRange(b)
		if r != nil {
			for _, w := range numericalValues {
				if r.contains(w.d) {
					b.count += w.n
				}
			}
			for _, sr := range ranges {
				if r.encloses(sr) {
					b.count++
				}
			}
		} else {
			for _, v := range nonNumericalValues {
				if b.specialValue != nil && equalsIgnoreCase(v, *b.specialValue) {
					b.count++
				}
			}
		}
		if b.specialValue != nil && equalsIgnoreCase("NA", *b.specialValue) {
			b.count = naCount
		}
	}
	return bins
}

// calculateDataBins mirrors DataBinner.calculateDataBins(filter, data,
// naDataBin, distinctValueThreshold).
func calculateDataBins(f filter, data []string, naDataBin *dataBin, threshold int) []*dataBin {
	numericalOnly := false

	if f.start != nil || f.end != nil {
		r := calcRangeBounds(f.start, true, f.end, true)
		if r.hasUpperBound() {
			data = filterSmallerThanUpperBound(data, r.upperEndpoint())
			numericalOnly = true
		}
		if r.hasLowerBound() {
			data = filterBiggerThanLowerBound(data, r.lowerEndpoint())
			numericalOnly = true
		}
	}

	upperOutlierBin := calcUpperOutlierBinForData(data)
	lowerOutlierBin := calcLowerOutlierBinForData(data)
	numericalBins := calcNumericalDataBins(f, filterNumericalValues(data), lowerOutlierBin, upperOutlierBin, threshold)

	var bins []*dataBin
	if lowerOutlierBin.count != 0 {
		bins = append(bins, lowerOutlierBin)
	}
	bins = append(bins, numericalBins...)
	if upperOutlierBin.count != 0 {
		bins = append(bins, upperOutlierBin)
	}

	bins = trim(bins)
	bins = convertToDistinctBins(bins, filterNumericalValues(data), filterSpecialRanges(data))

	if !numericalOnly {
		bins = append(bins, calcNonNumericalDataBins(filterNonNumericalValues(data))...)
		if naDataBin.count != 0 {
			bins = append(bins, naDataBin)
		}
	}
	return bins
}

// numericValue parses s as a number when NumberUtils.isCreatable accepts it,
// failing like new BigDecimal(s) for creatable strings BigDecimal rejects.
func numericValue(s string) (Decimal, bool) {
	if !isCreatable(s) {
		return Decimal{}, false
	}
	d, ok := parseJavaBigDecimal(s)
	if !ok {
		panic(javaError("NumberFormatException: Character array is missing \"exponent\" mark 'e' or 'E'. (" + s + ")"))
	}
	return d, true
}

// filterSpecialRanges mirrors DataBinner.filterSpecialRanges: values like
// ">90" or "<=18" become open ranges.
func filterSpecialRanges(data []string) []*interval {
	var out []*interval
	for _, s := range data {
		if !strings.Contains(s, ">") && !strings.Contains(s, "<") {
			continue
		}
		stripped := stripOperator(s)
		if !isCreatable(stripped) {
			continue
		}
		d, ok := parseJavaBigDecimal(stripped)
		if !ok {
			panic(javaError("NumberFormatException: " + stripped))
		}
		out = append(out, calcRangeForOperator(extractOperator(s)[:1], d))
	}
	return out
}

// filterNonNumericalValues mirrors DataBinner.filterNonNumericalValues.
func filterNonNumericalValues(data []string) []string {
	var out []string
	for _, s := range data {
		if !isCreatable(stripOperator(s)) && !isNA(s) {
			out = append(out, s)
		}
	}
	return out
}

// calcNonNumericalDataBins mirrors DataBinner.calcNonNumericalDataBins:
// values are grouped case-insensitively after trimming, keeping the first
// spelling and first-seen order.
func calcNonNumericalDataBins(values []string) []*dataBin {
	index := map[string]*dataBin{}
	var order []*dataBin
	for _, v := range values {
		key := strings.ToUpper(javaTrim(v))
		b, ok := index[key]
		if !ok {
			b = &dataBin{specialValue: strPtr(javaTrim(v))}
			index[key] = b
			order = append(order, b)
		}
		b.count++
	}
	return order
}

// filterNumericalValues mirrors DataBinner.filterNumericalValues. Each
// distinct string is parsed once.
func filterNumericalValues(data []string) []Decimal {
	type parsed struct {
		d  Decimal
		ok bool
	}
	cache := map[string]parsed{}
	var out []Decimal
	for _, s := range data {
		p, seen := cache[s]
		if !seen {
			p.d, p.ok = numericValue(s)
			cache[s] = p
		}
		if p.ok {
			out = append(out, p.d)
		}
	}
	return out
}

// calcNumericalDataBins mirrors DataBinner.calcNumericalDataBins. It mutates
// the outlier bins' limits and counts.
func calcNumericalDataBins(f filter, numericalValues []Decimal, lowerOutlierBin, upperOutlierBin *dataBin, threshold int) []*dataBin {
	isLowerOutlier := func(d Decimal) bool {
		if lowerOutlierBin.end == nil {
			return false
		}
		if lowerOutlierBin.specialValue != nil && strings.Contains(*lowerOutlierBin.specialValue, "=") {
			return d.Cmp(*lowerOutlierBin.end) != 1
		}
		return d.Cmp(*lowerOutlierBin.end) == -1
	}
	isUpperOutlier := func(d Decimal) bool {
		if upperOutlierBin.start == nil {
			return false
		}
		if upperOutlierBin.specialValue != nil && strings.Contains(*upperOutlierBin.specialValue, "=") {
			return d.Cmp(*upperOutlierBin.start) != -1
		}
		return d.Cmp(*upperOutlierBin.start) == 1
	}

	sorted := sortDecimals(numericalValues)
	boxRange := calcBoxRange(sorted)

	var withoutOutliers []Decimal
	for _, d := range sorted {
		if !isUpperOutlier(d) && !isLowerOutlier(d) {
			withoutOutliers = append(withoutOutliers, d)
		}
	}

	var bins []*dataBin
	binsSet := false
	customBins := f.customBins

	var uniqueValues []Decimal
	seen := map[decKey]bool{}
	for _, d := range withoutOutliers {
		if k := d.key(); !seen[k] {
			seen[k] = true
			uniqueValues = append(uniqueValues, d)
		}
	}

	if len(uniqueValues) > 0 && len(uniqueValues) <= threshold {
		bins = discreteDataBins(withoutOutliers, uniqueValues)
		binsSet = true
	} else if len(withoutOutliers) > 0 {
		switch {
		case f.binMethod == BinMethodCustom && customBins != nil:
			customBins = adjustCustomBins(customBins, lowerOutlierBin, upperOutlierBin)
			bins = linearCustomDataBins(customBins, numericalValues)
			binsSet = true
		case f.binMethod == BinMethodGenerate && f.binsGeneratorConfig != nil:
			boundaries := generateBins(sorted, f.binsGeneratorConfig.BinSize, f.binsGeneratorConfig.AnchorValue)
			bins = linearCustomDataBins(boundaries, numericalValues)
			binsSet = true
		case f.binMethod == BinMethodMedian:
			// handled below
		case f.binMethod == BinMethodQuartile:
			bins = linearCustomDataBins(calcQuartileBoundaries(sorted), numericalValues)
			binsSet = true
		case boxRange.upperEndpoint().sub(boxRange.lowerEndpoint()).Cmp(decimalFromInt(1000)) == 1 && !f.disableLogScale:
			bins = logScaleDataBins(boxRange, withoutOutliers, lowerOutlierBin.end, upperOutlierBin.start)
			binsSet = true
		case isSmallData(sorted):
			bins = scientificSmallDataBins(sorted, withoutOutliers, lowerOutlierBin.end, upperOutlierBin.start)
			binsSet = true
			if len(bins) > 0 {
				boxRange = closedRange(*bins[0].start, *bins[len(bins)-1].end)
			}
		default:
			allIntegers := areAllIntegers(uniqueValues)
			if allIntegers {
				boxRange = closedRange(
					decimalFromInt(boxRange.lowerEndpoint().longValue()),
					decimalFromInt(boxRange.upperEndpoint().longValue()))
			}
			lowerOutlier := boxRange.lowerEndpoint()
			if lowerOutlierBin.end != nil {
				lowerOutlier = lowerOutlier.maxOf(*lowerOutlierBin.end)
			}
			upperOutlier := boxRange.upperEndpoint()
			if upperOutlierBin.start != nil {
				upperOutlier = upperOutlier.minOf(*upperOutlierBin.start)
			}
			attributeID := f.attributeID
			if f.nonClinical {
				attributeID = nil
			}
			bins = linearDataBins(allIntegers, boxRange, withoutOutliers, &lowerOutlier, &upperOutlier, attributeID)
			binsSet = true
		}

		if f.binMethod == BinMethodMedian || (f.binMethod == BinMethodQuartile && len(bins) == 0) {
			median := calcMedian(sorted)
			lowerOutlierBin.end = decPtr(*median)
			upperOutlierBin.start = decPtr(*median)
		} else if f.binMethod == BinMethodCustom && customBins != nil && len(customBins) == 1 {
			lowerOutlierBin.end = decPtr(customBins[0])
			upperOutlierBin.start = decPtr(customBins[0])
		}

		// When the data has no special outlier values ("<=20", ">80") the
		// bin limits win over the box range; otherwise the special outlier
		// values win over the bin limits.
		if lowerOutlierBin.end == nil {
			if binsSet && len(bins) > 0 {
				lowerOutlierBin.end = bins[0].start
			} else {
				lowerOutlierBin.end = decPtr(boxRange.lowerEndpoint())
			}
		} else if binsSet && len(bins) > 0 {
			if bins[0].start.Cmp(*lowerOutlierBin.end) == 1 {
				lowerOutlierBin.end = bins[0].start
			} else {
				bins[0].start = lowerOutlierBin.end
			}
		}

		if upperOutlierBin.start == nil {
			if binsSet && len(bins) > 0 {
				upperOutlierBin.start = bins[len(bins)-1].end
			} else {
				upperOutlierBin.start = decPtr(boxRange.upperEndpoint())
			}
		} else if binsSet && len(bins) > 0 {
			last := bins[len(bins)-1]
			if last.end.Cmp(*upperOutlierBin.start) == -1 {
				upperOutlierBin.start = last.end
			} else {
				last.end = upperOutlierBin.start
			}
		}
	}

	upperCount, lowerCount := 0, 0
	for _, d := range sorted {
		if isUpperOutlier(d) {
			upperCount++
		}
		if isLowerOutlier(d) {
			lowerCount++
		}
	}
	upperOutlierBin.count += upperCount
	lowerOutlierBin.count += lowerCount

	return bins
}

// doubleValuesForSpecialOutliers mirrors the Java method of the same name:
// numeric parts of values starting with operator.
func doubleValuesForSpecialOutliers(data []string, operator string) []Decimal {
	var out []Decimal
	for _, s := range data {
		t := javaTrim(s)
		if !strings.HasPrefix(t, operator) {
			continue
		}
		rest := t[len(operator):]
		if !isCreatable(rest) {
			continue
		}
		d, ok := parseJavaBigDecimal(rest)
		if !ok {
			panic(javaError("NumberFormatException: " + rest))
		}
		out = append(out, d)
	}
	return out
}

func filterSmallerThanUpperBound(data []string, value Decimal) []string {
	var out []string
	for _, s := range data {
		if d, ok := numericValue(s); ok && d.Cmp(value) != 1 {
			out = append(out, s)
		}
	}
	return out
}

func filterBiggerThanLowerBound(data []string, value Decimal) []string {
	var out []string
	for _, s := range data {
		if d, ok := numericValue(s); ok && d.Cmp(value) != -1 {
			out = append(out, s)
		}
	}
	return out
}

// calcUpperOutlierBinForData mirrors DataBinner.calcUpperOutlierBin; the
// special value is always ">".
func calcUpperOutlierBinForData(data []string) *dataBin {
	b := calcUpperOutlierBin(doubleValuesForSpecialOutliers(data, ">="), doubleValuesForSpecialOutliers(data, ">"))
	b.specialValue = strPtr(">")
	return b
}

// calcLowerOutlierBinForData mirrors DataBinner.calcLowerOutlierBin; the
// special value is always "<=".
func calcLowerOutlierBinForData(data []string) *dataBin {
	b := calcLowerOutlierBin(doubleValuesForSpecialOutliers(data, "<="), doubleValuesForSpecialOutliers(data, "<"))
	b.specialValue = strPtr("<=")
	return b
}

// adjustCustomBins mirrors DataBinner.adjustCustomBins: boundaries outside
// the special outlier limits are replaced by the limits themselves.
func adjustCustomBins(customBins []Decimal, lowerOutlierBin, upperOutlierBin *dataBin) []Decimal {
	lowerBound := lowerOutlierBin.end
	upperBound := upperOutlierBin.start

	var within []Decimal
	belowLower, aboveUpper := false, false
	for _, b := range customBins {
		if lowerBound != nil && b.Cmp(*lowerBound) == -1 {
			belowLower = true
		}
		if upperBound != nil && b.Cmp(*upperBound) == 1 {
			aboveUpper = true
		}
		if (lowerBound == nil || b.Cmp(*lowerBound) != -1) && (upperBound == nil || b.Cmp(*upperBound) != 1) {
			within = append(within, b)
		}
	}
	if within == nil {
		within = []Decimal{}
	}
	if belowLower {
		within = append([]Decimal{*lowerBound}, within...)
	}
	if aboveUpper {
		within = append(within, *upperBound)
	}
	return within
}

// calcNaDataBin mirrors DataBinner.calcNaDataBin(clinicalData).
func calcNaDataBin(data []string) *dataBin {
	return &dataBin{specialValue: strPtr("NA"), count: countNAs(data)}
}

// countNAs counts values that are "NA", "NAN" or "N/A" (ignoring case).
func countNAs(data []string) int {
	n := 0
	for _, s := range data {
		if isNA(s) {
			n++
		}
	}
	return n
}
