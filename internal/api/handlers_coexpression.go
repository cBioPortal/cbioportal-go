package api

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"net/http"
	"sort"
	"strconv"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/stats"
)

// routeCoexpression registers co-expression and clinical/genomic plot-data
// endpoints (density scatter + violin plots).
func (s *State) routeCoexpression(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/molecular-profiles/co-expressions/fetch", s.coExpressionsFetch)
	mux.HandleFunc("POST /api/clinical-data-density-plot/fetch", s.clinicalDataDensityPlotFetch)
	mux.HandleFunc("POST /api/clinical-data-violin-plots/fetch", s.clinicalDataViolinPlotsFetch)
}

// ---------- shared numeric helpers ----------

// ranks returns fractional (average-tie) ranks for v, 1-based.
func ranks(v []float64) []float64 {
	n := len(v)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
	out := make([]float64, n)
	for i := 0; i < n; {
		j := i + 1
		for j < n && v[idx[j]] == v[idx[i]] {
			j++
		}
		avg := float64(i+j+1) / 2.0 // average of 1-based ranks i+1..j
		for k := i; k < j; k++ {
			out[idx[k]] = avg
		}
		i = j
	}
	return out
}

// pearson returns the Pearson correlation of a and b, or NaN if undefined.
func pearson(a, b []float64) float64 {
	n := len(a)
	if n == 0 || n != len(b) {
		return math.NaN()
	}
	var sa, sb float64
	for i := range n {
		sa += a[i]
		sb += b[i]
	}
	ma, mb := sa/float64(n), sb/float64(n)
	var num, da, db float64
	for i := range n {
		xa, xb := a[i]-ma, b[i]-mb
		num += xa * xb
		da += xa * xa
		db += xb * xb
	}
	denom := math.Sqrt(da * db)
	if denom == 0 {
		return math.NaN()
	}
	return num / denom
}

// spearman returns the Spearman rank correlation of a and b, or NaN.
func spearman(a, b []float64) float64 {
	return pearson(ranks(a), ranks(b))
}

// ---------- 1. co-expression ----------

// CoExpression is CoExpressionDTO. A gene too sparse or too constant to
// correlate is reported without a correlation and p-value.
type CoExpression struct {
	GeneticEntityID      string   `json:"geneticEntityId"`
	GeneticEntityType    string   `json:"geneticEntityType"`
	SpearmansCorrelation *float64 `json:"spearmansCorrelation,omitempty"`
	PValue               *float64 `json:"pValue,omitempty"`
}

type coExpressionFilter struct {
	EntrezGeneID *int64   `json:"entrezGeneId"`
	SampleIDs    []string `json:"sampleIds"`
	SampleListID *string  `json:"sampleListId"`
}

// coExpressionsFetch ports ColumnStoreCoExpressionController and
// FetchCoExpressionsUseCase: the Spearman correlation (ClickHouse rankCorr)
// between the query gene in profile A and every other gene of profile B over
// the requested samples of study A, as ClickhouseCoExpressionMapper computes
// it. Genes with at least 3 samples, more than one value and more than n/2
// distinct values are kept when |r| reaches the threshold; the others are
// listed without a correlation.
func (s *State) coExpressionsFetch(w http.ResponseWriter, r *http.Request) {
	var f coExpressionFilter
	readBody(r, &f)
	q := r.URL.Query()
	profileA, profileB := q.Get("molecularProfileIdA"), q.Get("molecularProfileIdB")
	threshold := 0.3
	if t := q.Get("threshold"); t != "" {
		v, err := strconv.ParseFloat(t, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, `Request parameter type mismatch: java.lang.NumberFormatException: For input string: "`+t+`"`)
			return
		}
		threshold = v
	}
	out := []CoExpression{}
	if f.EntrezGeneID == nil {
		writeJSON(w, out)
		return
	}
	sampleIDs := f.SampleIDs
	if f.SampleListID != nil {
		type row struct {
			SampleID string `json:"sampleId"`
		}
		p := ch.Params{}
		rows, err := ch.QueryP[row](s.CH, "SELECT sample_id AS sampleId FROM sample_list WHERE "+s.sampleListCondP(p, *f.SampleListID), p)
		if err != nil {
			fail(w, err)
			return
		}
		sampleIDs = sampleIDs[:0]
		for _, rw := range rows {
			sampleIDs = append(sampleIDs, rw.SampleID)
		}
	}
	if len(sampleIDs) == 0 {
		writeJSON(w, out)
		return
	}
	metaA, ok := s.profileMeta(profileA)
	if !ok {
		writeError(w, http.StatusNotFound, msgMolecularProfileNotFound(profileA))
		return
	}
	metaB := metaA
	if profileB != profileA {
		if metaB, ok = s.profileMeta(profileB); !ok {
			writeError(w, http.StatusNotFound, msgMolecularProfileNotFound(profileB))
			return
		}
	}
	type geneRow struct {
		Hugo string `json:"hugo"`
	}
	genes, err := ch.Query[geneRow](s.CH, fmt.Sprintf(
		"SELECT hugo_gene_symbol AS hugo FROM cbioportal_ref.gene WHERE entrez_gene_id = %d", *f.EntrezGeneID))
	if err != nil {
		fail(w, err)
		return
	}
	if len(genes) == 0 {
		writeError(w, http.StatusNotFound, msgGeneNotFound(strconv.FormatInt(*f.EntrezGeneID, 10)))
		return
	}
	hugo := genes[0].Hugo
	typeA := strings.TrimPrefix(profileA, metaA.StudyID+"_")
	typeB := strings.TrimPrefix(profileB, metaB.StudyID+"_")
	uniqueIDs := make([]string, len(sampleIDs))
	for i, id := range sampleIDs {
		uniqueIDs[i] = metaA.StudyID + "_" + id
	}
	type coexpRow struct {
		EntrezGeneID int64    `json:"entrezGeneId"`
		Corr         *float64 `json:"corr"`
		N            int      `json:"n"`
	}
	rows, err := ch.QueryP[coexpRow](s.CH, fmt.Sprintf(
		"WITH ref_values AS ("+
			"SELECT sample_unique_id, toFloat64OrNull(alteration_value) AS ref_val FROM genetic_alteration_derived "+
			"WHERE cancer_study_identifier = {studyA:String} AND profile_type = {typeA:String} AND hugo_gene_symbol = {hugo:String} "+
			"AND alteration_value != '' AND alteration_value != 'NA' AND toFloat64OrNull(alteration_value) IS NOT NULL "+
			"AND sample_unique_id IN {samples:Array(String)}), "+
			"per_gene AS (SELECT g.entrez_gene_id AS entrezGeneId, count(*) AS numSamples, "+
			"uniqExact(toFloat64OrNull(d.alteration_value)) AS exactDistinct, uniq(toFloat64OrNull(d.alteration_value)) AS approxDistinct, "+
			"groupArray(r.ref_val) AS refValues, groupArray(toFloat64OrNull(d.alteration_value)) AS geneValues "+
			"FROM genetic_alteration_derived d INNER JOIN ref_values r ON d.sample_unique_id = r.sample_unique_id "+
			"INNER JOIN cbioportal_ref.gene g ON d.hugo_gene_symbol = g.hugo_gene_symbol "+
			"WHERE d.cancer_study_identifier = {studyB:String} AND d.profile_type = {typeB:String} AND d.hugo_gene_symbol != {hugo:String} "+
			"AND d.alteration_value != '' AND d.alteration_value != 'NA' AND toFloat64OrNull(d.alteration_value) IS NOT NULL "+
			"GROUP BY g.entrez_gene_id), "+
			"scored AS (SELECT entrezGeneId, numSamples, "+
			"(numSamples >= 3 AND exactDistinct > 1 AND approxDistinct > numSamples / 2) AS validNumeric, "+
			"arrayReduce('rankCorr', if(exactDistinct > 1, refValues, [0., 1.]), if(exactDistinct > 1, geneValues, [0., 1.])) AS corr "+
			"FROM per_gene) "+
			"SELECT entrezGeneId, if(validNumeric AND isFinite(corr), corr, NULL) AS corr, toInt64(numSamples) AS n FROM scored "+
			"WHERE (validNumeric AND isFinite(corr) AND abs(corr) >= %v) OR (NOT validNumeric) ORDER BY entrezGeneId",
		threshold), ch.Params{"studyA": metaA.StudyID, "typeA": typeA, "hugo": hugo, "samples": uniqueIDs,
		"studyB": metaB.StudyID, "typeB": typeB})
	if err != nil {
		fail(w, err)
		return
	}
	for _, row := range rows {
		ce := CoExpression{GeneticEntityID: strconv.FormatInt(row.EntrezGeneID, 10), GeneticEntityType: "GENE"}
		if row.Corr != nil {
			p := stats.CommonsCorrelationPValue(*row.Corr, row.N)
			ce.SpearmansCorrelation, ce.PValue = row.Corr, &p
		}
		out = append(out, ce)
	}
	writeJSON(w, out)
}

// ---------- clinical value resolution (shared by density + violin) ----------

// scopedAttrRow is one scoped sample with the raw sample- and patient-level
// values (nil when absent) of two clinical attributes A and B.
type scopedAttrRow struct {
	StudyID  string  `json:"studyId"`
	SampleID string  `json:"sampleId"`
	SampleA  *string `json:"sa"`
	PatientA *string `json:"pa"`
	SampleB  *string `json:"sb"`
	PatientB *string `json:"pb"`
}

// scopedAttrPairs joins, inside ClickHouse, every sample in the study-view
// scope to its sample- and patient-level values of attributes A and B, and
// returns only the samples that have some value for both, ordered by study and
// sample id. Precedence between the two levels is resolved by the caller.
func (s *State) scopedAttrPairs(f studyViewFilter, attrA, attrB string) []scopedAttrRow {
	if attrA == "" || attrB == "" {
		return nil
	}
	args := newArgs()
	studyClause := ""
	if studies := f.studyIDs(); len(studies) > 0 {
		studyClause = " AND study_id IN " + args.strs(studies)
	}
	attr := func(view, keyCol, attrID string) string {
		return fmt.Sprintf("(SELECT study_id, %s, toNullable(attr_value) AS v FROM %s WHERE attr_id = %s%s)",
			keyCol, view, args.str(attrID), studyClause)
	}
	rows, _ := queryA[scopedAttrRow](s.CH, fmt.Sprintf(
		"SELECT s.study_id AS studyId, s.sample_id AS sampleId, sa.v AS sa, pa.v AS pa, sb.v AS sb, pb.v AS pb "+
			"FROM (SELECT study_id, sample_id, patient_id FROM sample WHERE %s) AS s "+
			"LEFT JOIN %s AS sa ON sa.study_id = s.study_id AND sa.sample_id = s.sample_id "+
			"LEFT JOIN %s AS pa ON pa.study_id = s.study_id AND pa.patient_id = s.patient_id "+
			"LEFT JOIN %s AS sb ON sb.study_id = s.study_id AND sb.sample_id = s.sample_id "+
			"LEFT JOIN %s AS pb ON pb.study_id = s.study_id AND pb.patient_id = s.patient_id "+
			"WHERE (sa IS NOT NULL OR pa IS NOT NULL) AND (sb IS NOT NULL OR pb IS NOT NULL) "+
			"ORDER BY studyId, sampleId",
		s.sampleScope(args, f),
		attr("clinical_sample", "sample_id", attrA), attr("clinical_patient", "patient_id", attrA),
		attr("clinical_sample", "sample_id", attrB), attr("clinical_patient", "patient_id", attrB)), args)
	return rows
}

// clinicalNumber resolves a sample's numeric attribute value: the sample-level
// value when it parses as a number, else the patient-level one.
func clinicalNumber(sampleVal, patientVal *string) (float64, bool) {
	for _, v := range []*string{sampleVal, patientVal} {
		if v == nil {
			continue
		}
		if f, err := strconv.ParseFloat(*v, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// clinicalCategory resolves a sample's categorical attribute value: the
// sample-level value when present, else the patient-level one.
func clinicalCategory(sampleVal, patientVal *string) (string, bool) {
	if sampleVal != nil {
		return *sampleVal, true
	}
	if patientVal != nil {
		return *patientVal, true
	}
	return "", false
}

// ---------- 2. clinical data density plot ----------

type DensityPlotBin struct {
	Count int     `json:"count"`
	BinX  float64 `json:"binX"`
	BinY  float64 `json:"binY"`
	MinX  float64 `json:"minX"`
	MaxX  float64 `json:"maxX"`
	MinY  float64 `json:"minY"`
	MaxY  float64 `json:"maxY"`
}

// DensityPlotData is the density plot; the correlations are unset (and not
// serialized) when no sample has both values, as in
// ClinicalDataDensityPlotServiceImpl.
type DensityPlotData struct {
	Bins         []DensityPlotBin `json:"bins"`
	PearsonCorr  *jacksonDouble   `json:"pearsonCorr,omitempty"`
	SpearmanCorr *jacksonDouble   `json:"spearmanCorr,omitempty"`
}

func queryFloat(r *http.Request, name string) (float64, bool) {
	if v := r.URL.Query().Get(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// logScale returns the value mapping of a plot axis: ln(1 + v) when the
// boolean query param is true and the attribute is MUTATION_COUNT, the only
// attribute the Java backend log-scales in density and violin plots, else the
// identity.
func logScale(r *http.Request, param, attrID string) func(float64) float64 {
	if !strings.EqualFold(r.URL.Query().Get(param), "true") || attrID != "MUTATION_COUNT" {
		return func(v float64) float64 { return v }
	}
	return func(v float64) float64 { return math.Log(1 + v) }
}

func queryIntDefault(r *http.Request, name string, def int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func (s *State) clinicalDataDensityPlotFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)

	xAttr := r.URL.Query().Get("xAxisAttributeId")
	yAttr := r.URL.Query().Get("yAxisAttributeId")
	xBins := queryIntDefault(r, "xAxisBinCount", 50)
	yBins := queryIntDefault(r, "yAxisBinCount", 50)

	result := DensityPlotData{Bins: []DensityPlotBin{}}

	xScale := logScale(r, "xAxisLogScale", xAttr)
	yScale := logScale(r, "yAxisLogScale", yAttr)
	var xs, ys []float64
	for _, row := range s.scopedAttrPairs(f, xAttr, yAttr) {
		xv, okX := clinicalNumber(row.SampleA, row.PatientA)
		yv, okY := clinicalNumber(row.SampleB, row.PatientB)
		if okX && okY {
			xs = append(xs, xScale(xv))
			ys = append(ys, yScale(yv))
		}
	}
	if len(xs) == 0 {
		writeJSON(w, result)
		return
	}

	xStart, ok := queryFloat(r, "xAxisStart")
	if !ok {
		xStart = minFloat(xs)
	}
	xEnd, ok := queryFloat(r, "xAxisEnd")
	if !ok {
		xEnd = maxFloat(xs)
	}
	yStart, ok := queryFloat(r, "yAxisStart")
	if !ok {
		yStart = minFloat(ys)
	}
	yEnd, ok := queryFloat(r, "yAxisEnd")
	if !ok {
		yEnd = maxFloat(ys)
	}

	xInterval := (xEnd - xStart) / float64(xBins)
	yInterval := (yEnd - yStart) / float64(yBins)

	bins := make([]DensityPlotBin, xBins*yBins)
	filled := make([]bool, xBins*yBins)
	for i := range xBins {
		for j := range yBins {
			b := &bins[i*yBins+j]
			b.BinX = xStart + float64(i)*xInterval
			b.BinY = yStart + float64(j)*yInterval
		}
	}

	for k := range xs {
		xv, yv := xs[k], ys[k]
		xi := 0
		yi := 0
		if xInterval > 0 {
			xi = int((xv - xStart) / xInterval)
		}
		if yInterval > 0 {
			yi = int((yv - yStart) / yInterval)
		}
		if xi < 0 {
			xi = 0
		}
		if yi < 0 {
			yi = 0
		}
		if xi >= xBins {
			xi = xBins - 1
		}
		if yi >= yBins {
			yi = yBins - 1
		}
		idx := xi*yBins + yi
		b := &bins[idx]
		b.Count++
		if !filled[idx] {
			b.MinX, b.MaxX, b.MinY, b.MaxY = xv, xv, yv, yv
			filled[idx] = true
		} else {
			b.MinX = math.Min(b.MinX, xv)
			b.MaxX = math.Max(b.MaxX, xv)
			b.MinY = math.Min(b.MinY, yv)
			b.MaxY = math.Max(b.MaxY, yv)
		}
	}

	for i := range bins {
		if bins[i].Count > 0 {
			result.Bins = append(result.Bins, bins[i])
		}
	}
	// Constant values give NaN correlations, which Java writes as "NaN".
	var pearsonCorr, spearmanCorr jacksonDouble
	if len(xs) > 1 {
		pearsonCorr, spearmanCorr = jacksonDouble(pearson(xs, ys)), jacksonDouble(spearman(xs, ys))
	}
	result.PearsonCorr, result.SpearmanCorr = &pearsonCorr, &spearmanCorr
	writeJSON(w, result)
}

// ---------- 3. clinical data violin plots ----------

type ClinicalViolinPlotBoxData struct {
	WhiskerLower float64 `json:"whiskerLower"`
	WhiskerUpper float64 `json:"whiskerUpper"`
	Median       float64 `json:"median"`
	Q1           float64 `json:"q1"`
	Q3           float64 `json:"q3"`
}

type ClinicalViolinPlotIndividualPoint struct {
	SampleID string  `json:"sampleId"`
	StudyID  string  `json:"studyId"`
	Value    float64 `json:"value"`
}

type ClinicalViolinPlotRowData struct {
	Category         string                              `json:"category"`
	NumSamples       int                                 `json:"numSamples"`
	CurveData        []float64                           `json:"curveData"`
	BoxData          ClinicalViolinPlotBoxData           `json:"boxData"`
	IndividualPoints []ClinicalViolinPlotIndividualPoint `json:"individualPoints"`
}

// ClinicalViolinPlotData is the violin plot; without data its axis runs
// from Infinity to -Infinity, as ViolinPlotServiceImpl leaves it.
type ClinicalViolinPlotData struct {
	Rows      []ClinicalViolinPlotRowData `json:"rows"`
	AxisStart jacksonDouble               `json:"axisStart"`
	AxisEnd   jacksonDouble               `json:"axisEnd"`
}

const violinShowOnlyPointsThreshold = 7

func (s *State) clinicalDataViolinPlotsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)

	numAttr := r.URL.Query().Get("numericalAttributeId")
	catAttr := r.URL.Query().Get("categoricalAttributeId")
	numCurvePoints := queryIntDefault(r, "numCurvePoints", 100)
	sigmaMultiplier := 1.0
	if v, ok := queryFloat(r, "sigmaMultiplier"); ok {
		sigmaMultiplier = v
	}

	result := ClinicalViolinPlotData{Rows: []ClinicalViolinPlotRowData{}}

	scale := logScale(r, "logScale", numAttr)
	type point struct {
		studyID, sampleID string
		val               float64
	}
	// As the Java controller does, the plot shows the numerical attribute's
	// whole distribution (its own filter removed) while each row counts only
	// the samples of the full filter.
	type sampleKey struct {
		K string `json:"k"`
	}
	inFilter := map[string]bool{}
	args := newArgs()
	keys, _ := queryA[sampleKey](s.CH, "SELECT sample_unique_id AS k FROM ("+s.filterSQL(args, f)+")", args)
	for _, k := range keys {
		inFilter[k.K] = true
	}
	countInFilter := func(lists ...[]point) int {
		n := 0
		for _, l := range lists {
			for _, p := range l {
				if inFilter[p.studyID+"_"+p.sampleID] {
					n++
				}
			}
		}
		return n
	}
	// The service walks its sampleId-keyed HashMap of clinical data and adds
	// categories to another HashMap as it meets them, so both maps' Java
	// iteration orders decide the row order.
	pairs := s.scopedAttrPairs(f.withoutClinical(numAttr), numAttr, catAttr)
	bySample := map[string][]int{}
	sampleIDs := make([]string, 0, len(pairs))
	for i, row := range pairs {
		if _, ok := bySample[row.SampleID]; !ok {
			sampleIDs = append(sampleIDs, row.SampleID)
		}
		bySample[row.SampleID] = append(bySample[row.SampleID], i)
	}
	sort.Strings(sampleIDs)
	byCategory := map[string][]point{}
	var categoryOrder []string
	for _, sid := range javaHashMapOrder(sampleIDs) {
		for _, i := range bySample[sid] {
			row := pairs[i]
			v, okV := clinicalNumber(row.SampleA, row.PatientA)
			cat, okC := clinicalCategory(row.SampleB, row.PatientB)
			if !okV || !okC {
				continue
			}
			if _, ok := byCategory[cat]; !ok {
				categoryOrder = append(categoryOrder, cat)
			}
			byCategory[cat] = append(byCategory[cat], point{studyID: row.StudyID, sampleID: row.SampleID, val: scale(v)})
		}
	}
	axisStart := math.Inf(1)
	axisEnd := math.Inf(-1)
	if len(byCategory) == 0 {
		result.AxisStart, result.AxisEnd = jacksonDouble(axisStart), jacksonDouble(axisEnd)
		writeJSON(w, result)
		return
	}

	type catBox struct {
		box                   ClinicalViolinPlotBoxData
		nonOutliers, outliers []point
		min, max              float64
	}
	boxes := map[string]*catBox{}
	// Rows follow the iteration order of the service's category HashMap.
	categories := javaHashMapOrder(categoryOrder)

	for _, cat := range categories {
		data := byCategory[cat]
		values := make([]float64, len(data))
		for i, d := range data {
			values[i] = d.val
		}
		mn, mx := minFloat(values), maxFloat(values)
		q1 := percentile(values, 25)
		q3 := percentile(values, 75)
		median := percentile(values, 50)
		iqr := q3 - q1
		outLower := q1 - 3*iqr
		outUpper := q3 + 3*iqr
		suspLower := q1 - 1.5*iqr
		suspUpper := q3 + 1.5*iqr

		cb := &catBox{min: mn, max: mx}
		numSuspected := 0
		for _, d := range data {
			isOutlier := false
			if d.val <= suspLower {
				numSuspected++
				if d.val <= outLower {
					isOutlier = true
				}
			} else if d.val >= suspUpper {
				numSuspected++
				if d.val >= outUpper {
					isOutlier = true
				}
			}
			if isOutlier {
				cb.outliers = append(cb.outliers, d)
			} else {
				cb.nonOutliers = append(cb.nonOutliers, d)
			}
		}
		cb.box = ClinicalViolinPlotBoxData{Median: median, Q1: q1, Q3: q3}
		if numSuspected > 0 {
			cb.box.WhiskerLower = suspLower
			cb.box.WhiskerUpper = suspUpper
		} else {
			cb.box.WhiskerLower = mn
			cb.box.WhiskerUpper = mx
		}
		boxes[cat] = cb
		axisStart = math.Min(axisStart, mn)
		axisEnd = math.Max(axisEnd, mx)
	}

	if v, ok := queryFloat(r, "axisStart"); ok {
		axisStart = v
	}
	if v, ok := queryFloat(r, "axisEnd"); ok {
		axisEnd = v
	}
	result.AxisStart, result.AxisEnd = jacksonDouble(axisStart), jacksonDouble(axisEnd)

	// Curve sample points across the axis.
	var stepSize float64
	if numCurvePoints > 1 {
		stepSize = (axisEnd - axisStart) / float64(numCurvePoints-1)
	}
	curvePoints := make([]float64, numCurvePoints)
	for i := range curvePoints {
		curvePoints[i] = axisStart + float64(i)*stepSize
	}
	sigma := sigmaMultiplier * stepSize

	for _, cat := range categories {
		cb := boxes[cat]
		row := ClinicalViolinPlotRowData{
			Category:         cat,
			BoxData:          clampWhiskers(cb.box, axisStart, axisEnd),
			CurveData:        []float64{},
			IndividualPoints: []ClinicalViolinPlotIndividualPoint{},
			NumSamples:       countInFilter(cb.nonOutliers, cb.outliers),
		}
		var pointsToRender []point
		if len(cb.nonOutliers)+len(cb.outliers) <= violinShowOnlyPointsThreshold {
			pointsToRender = append(pointsToRender, cb.nonOutliers...)
			pointsToRender = append(pointsToRender, cb.outliers...)
		} else {
			row.CurveData = violinCurve(curvePoints, cb.nonOutliers, sigma, func(d point) float64 { return d.val })
			pointsToRender = cb.outliers
		}
		for _, d := range pointsToRender {
			row.IndividualPoints = append(row.IndividualPoints, ClinicalViolinPlotIndividualPoint{
				SampleID: d.sampleID, StudyID: d.studyID, Value: d.val,
			})
		}
		result.Rows = append(result.Rows, row)
	}
	writeJSON(w, result)
}

// violinCurve is the kernel density of ViolinPlotServiceImpl: at each curve
// point, the sum over the values of a normalized Gaussian (commons-math
// Gaussian(value, sigma)) evaluated there, summed as the service does.
func violinCurve[T any](curvePoints []float64, data []T, sigma float64, value func(T) float64) []float64 {
	curve := make([]float64, len(curvePoints))
	if sigma <= 0 || math.IsNaN(sigma) {
		return curve
	}
	norm := 1 / (sigma * math.Sqrt(2*math.Pi))
	i2s2 := 1 / (2 * sigma * sigma)
	terms := make([]float64, len(data))
	for i, p := range curvePoints {
		for j, d := range data {
			diff := p - value(d)
			terms[j] = norm * math.Exp(-diff*diff*i2s2)
		}
		curve[i] = bigDecimalSum(terms)
	}
	return curve
}

// bigDecimalSum is the sum of BigDecimal.valueOf(term) over the terms,
// converted back with doubleValue(): each term contributes its shortest
// decimal representation (Double.toString) and the sum is exact, rounded
// once to the nearest double. Terms below 2^-200 of the largest are left out:
// together they are far below the last bit of the sum.
func bigDecimalSum(terms []float64) float64 {
	largest := 0.0
	for _, t := range terms {
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return t
		}
		largest = max(largest, math.Abs(t))
	}
	cutoff := math.Ldexp(largest, -200)

	// Significand sums (128-bit, with sign) per decimal exponent: a term is
	// digits * 10^exp with at most 17 digits.
	byExp := map[int]*[2]uint64{}
	negByExp := map[int]*[2]uint64{}
	var buf []byte
	for _, t := range terms {
		if t == 0 || math.Abs(t) < cutoff {
			continue
		}
		buf = strconv.AppendFloat(buf[:0], math.Abs(t), 'e', -1, 64)
		var digits uint64
		nFrac := -1
		i := 0
		for ; i < len(buf) && buf[i] != 'e'; i++ {
			switch c := buf[i]; {
			case c == '.':
				nFrac = 0
			default:
				digits = digits*10 + uint64(c-'0')
				if nFrac >= 0 {
					nFrac++
				}
			}
		}
		e, _ := strconv.Atoi(string(buf[i+1:]))
		exp := e - max(nFrac, 0)
		m := byExp
		if t < 0 {
			m = negByExp
		}
		a := m[exp]
		if a == nil {
			a = &[2]uint64{}
			m[exp] = a
		}
		var carry uint64
		a[1], carry = bits.Add64(a[1], digits, 0)
		a[0] += carry
	}
	if len(byExp)+len(negByExp) == 0 {
		return 0
	}
	scale := 0
	for e := range byExp {
		scale = max(scale, -e)
	}
	for e := range negByExp {
		scale = max(scale, -e)
	}
	ten := big.NewInt(10)
	sum := new(big.Int)
	var v, hi, pow big.Int
	add := func(m map[int]*[2]uint64, sign int) {
		for e, a := range m {
			v.SetUint64(a[1])
			hi.SetUint64(a[0])
			v.Add(&v, hi.Lsh(&hi, 64))
			pow.Exp(ten, big.NewInt(int64(e+scale)), nil)
			v.Mul(&v, &pow)
			if sign < 0 {
				sum.Sub(sum, &v)
			} else {
				sum.Add(sum, &v)
			}
		}
	}
	add(byExp, 1)
	add(negByExp, -1)
	den := new(big.Int).Exp(ten, big.NewInt(int64(scale)), nil)
	f, _ := new(big.Rat).SetFrac(sum, den).Float64()
	return f
}

func clampWhiskers(b ClinicalViolinPlotBoxData, lo, hi float64) ClinicalViolinPlotBoxData {
	b.WhiskerLower = math.Max(b.WhiskerLower, lo)
	b.WhiskerUpper = math.Min(b.WhiskerUpper, hi)
	return b
}

// ---------- small numeric utilities ----------

func minFloat(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func maxFloat(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

// jacksonDouble is a double as Jackson writes it: NaN and the infinities as the
// strings "NaN", "Infinity" and "-Infinity".
type jacksonDouble float64

func (d jacksonDouble) MarshalJSON() ([]byte, error) {
	v := float64(d)
	switch {
	case math.IsNaN(v):
		return []byte(`"NaN"`), nil
	case math.IsInf(v, 1):
		return []byte(`"Infinity"`), nil
	case math.IsInf(v, -1):
		return []byte(`"-Infinity"`), nil
	}
	return json.Marshal(v)
}

func safeFloat(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// percentile computes the p-th percentile (0-100) using Apache-commons-style
// linear interpolation, matching the Java Percentile default.
func percentile(values []float64, p float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if n == 1 {
		return sorted[0]
	}
	pos := p / 100.0 * float64(n+1)
	if pos < 1 {
		return sorted[0]
	}
	if pos >= float64(n) {
		return sorted[n-1]
	}
	fpos := math.Floor(pos)
	d := pos - fpos
	lower := sorted[int(fpos)-1]
	upper := sorted[int(fpos)]
	return lower + d*(upper-lower)
}

// spearmanSQL computes, per entity of rows (columns entity, hugo, x, y), the
// Spearman correlation sc of x and y over n pairs, keeping entities with
// n > 2 that also pass keep and whose |sc| reaches threshold. An entity whose
// x or y values are all equal has no rank correlation and is dropped before
// it is computed (ClickHouse's rankCorr rejects such input).
func spearmanSQL(rows, keep string, threshold float64) string {
	return fmt.Sprintf(
		"SELECT entity, hugo, n, arrayReduce('rankCorr', xs, ys) AS sc FROM ("+
			"SELECT entity, any(hugo) AS hugo, toInt64(count()) AS n, groupArray(x) AS xs, groupArray(y) AS ys "+
			"FROM (%s) GROUP BY entity "+
			"HAVING n > 2 AND %s AND arrayMin(xs) != arrayMax(xs) AND arrayMin(ys) != arrayMax(ys)) "+
			"WHERE abs(sc) >= %g",
		rows, keep, threshold)
}
