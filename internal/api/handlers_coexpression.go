package api

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/stats"
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
	for i := 0; i < n; i++ {
		sa += a[i]
		sb += b[i]
	}
	ma, mb := sa/float64(n), sb/float64(n)
	var num, da, db float64
	for i := 0; i < n; i++ {
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

type CoExpression struct {
	GeneticEntityID      string  `json:"geneticEntityId"`
	GeneticEntityType    string  `json:"geneticEntityType"`
	EntrezGeneID         *int64  `json:"entrezGeneId,omitempty"`
	HugoGeneSymbol       string  `json:"hugoGeneSymbol,omitempty"`
	Cytoband             string  `json:"cytoband,omitempty"`
	SpearmansCorrelation float64 `json:"spearmansCorrelation"`
	PValue               float64 `json:"pValue"`
	QValue               float64 `json:"qValue"`
	MolecularProfileID   string  `json:"molecularProfileId,omitempty"`
}

type coExpressionFilter struct {
	EntrezGeneID int64    `json:"entrezGeneId"`
	GenesetID    string   `json:"genesetId"`
	SampleIDs    []string `json:"sampleIds"`
	SampleListID string   `json:"sampleListId"`
}

// coExpressionsFetch computes Spearman correlations between a query genetic
// entity in profile A and every other entity in profile B over shared samples.
func (s *State) coExpressionsFetch(w http.ResponseWriter, r *http.Request) {
	var f coExpressionFilter
	readBody(r, &f)

	profileA := r.URL.Query().Get("molecularProfileIdA")
	profileB := r.URL.Query().Get("molecularProfileIdB")
	if profileB == "" {
		profileB = profileA
	}
	threshold := 0.3
	if t := r.URL.Query().Get("threshold"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil {
			threshold = v
		}
	}

	metaA, okA := s.profileMeta(profileA)
	metaB, okB := s.profileMeta(profileB)
	if !okA || !okB {
		writeJSON(w, []CoExpression{})
		return
	}
	tableA, tableB := metaA.dataTable(), metaB.dataTable()
	if tableA == "" || tableB == "" {
		writeJSON(w, []CoExpression{})
		return
	}

	sampleFilter := coexpSampleFilter(f)

	// Query entity match (profile A). genesetId identifies GENESET entities.
	queryEntity := fmt.Sprintf("%d", f.EntrezGeneID)
	queryMatch := fmt.Sprintf("toString(entrez_gene_id) = '%s'", sqlLit(queryEntity))
	if f.GenesetID != "" {
		queryEntity = f.GenesetID
		queryMatch = fmt.Sprintf("geneset_id = '%s'", sqlLit(f.GenesetID))
	}

	hugoCol := "hugo_symbol"
	entityCol := "toString(entrez_gene_id)"
	if tableB == "geneset_score_matrix" {
		hugoCol = "''"
		entityCol = "geneset_id"
	}

	// Push the correlation into ClickHouse: rankCorr computes Spearman per entity
	// over the samples shared with the query gene, and HAVING applies the
	// threshold — so only the entities above it cross the wire (dozens–hundreds),
	// not the whole profile-B matrix (millions of rows).
	querySub := fmt.Sprintf(
		"SELECT sample_id, toFloat64OrZero(value) AS qval FROM cbioportal_raw.%s "+
			"WHERE study_id = '%s' AND profile_stable_id = '%s' AND %s "+
			"AND toFloat64OrNull(value) IS NOT NULL%s",
		tableA, sqlLit(metaA.StudyID), sqlLit(metaA.StableID), queryMatch, sampleFilter)

	type coexpRow struct {
		Entity string   `json:"entity"`
		Hugo   string   `json:"hugo"`
		N      int      `json:"n"`
		SC     *float64 `json:"sc"`
	}
	rows, err := ch.Query[coexpRow](s.CH, fmt.Sprintf(
		"SELECT %s AS entity, any(%s) AS hugo, toInt64(count()) AS n, "+
			"rankCorr(toFloat64OrZero(b.value), q.qval) AS sc "+
			"FROM cbioportal_raw.%s AS b INNER JOIN (%s) AS q USING (sample_id) "+
			"WHERE b.study_id = '%s' AND b.profile_stable_id = '%s' "+
			"AND toFloat64OrNull(b.value) IS NOT NULL "+
			"GROUP BY entity "+
			"HAVING n > 2 AND entity != '%s' AND isNotNull(sc) AND abs(sc) >= %g",
		entityCol, hugoCol, tableB, querySub,
		sqlLit(metaB.StudyID), sqlLit(metaB.StableID), sqlLit(queryEntity), threshold))
	if err != nil {
		fail(w, err)
		return
	}

	out := []CoExpression{}
	for _, row := range rows {
		if row.SC == nil {
			continue
		}
		sc := *row.SC
		ce := CoExpression{
			GeneticEntityID:      row.Entity,
			GeneticEntityType:    "GENE",
			HugoGeneSymbol:       row.Hugo,
			SpearmansCorrelation: sc,
			PValue:               stats.SpearmanPValue(sc, row.N),
			MolecularProfileID:   profileB,
		}
		if f.GenesetID != "" {
			ce.GeneticEntityType = "GENESET"
		} else if id, e := strconv.ParseInt(row.Entity, 10, 64); e == nil {
			ce.EntrezGeneID = &id
		}
		out = append(out, ce)
	}
	writeJSON(w, out)
}

func coexpSampleFilter(f coExpressionFilter) string {
	if len(f.SampleIDs) > 0 {
		return fmt.Sprintf(" AND sample_id IN (%s)", inList(f.SampleIDs))
	}
	if f.SampleListID != "" {
		return fmt.Sprintf(" AND sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(f.SampleListID))
	}
	return ""
}

// ---------- clinical value resolution (shared by density + violin) ----------

// scopedSample is one sample in the study-view scope.
type scopedSample struct {
	StudyID   string `json:"studyId"`
	SampleID  string `json:"sampleId"`
	PatientID string `json:"patientId"`
}

func (s *State) scopedSamples(f studyViewFilter) []scopedSample {
	rows, _ := ch.Query[scopedSample](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM sample WHERE %s",
		s.sampleScope(f)))
	return rows
}

// attrValuesBySample resolves a clinical attribute to a per-sample numeric value
// over the scoped samples, checking both sample- and patient-level clinical data.
// Keys are studyId+"\x00"+sampleId.
func (s *State) attrValuesBySample(samples []scopedSample, attrID string) map[string]float64 {
	out := map[string]float64{}
	if attrID == "" || len(samples) == 0 {
		return out
	}
	sampleKeys := map[string]bool{}
	studySet := map[string]bool{}
	patientToSamples := map[string][]scopedSample{}
	for _, sm := range samples {
		sampleKeys[sm.StudyID+"\x00"+sm.SampleID] = true
		studySet[sm.StudyID] = true
		pk := sm.StudyID + "\x00" + sm.PatientID
		patientToSamples[pk] = append(patientToSamples[pk], sm)
	}
	studies := make([]string, 0, len(studySet))
	for st := range studySet {
		studies = append(studies, st)
	}

	type sampleRow struct {
		StudyID  string `json:"studyId"`
		SampleID string `json:"sampleId"`
		Value    string `json:"attr_value"`
	}
	sRows, _ := ch.Query[sampleRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS sampleId, attr_value FROM clinical_sample "+
			"WHERE study_id IN (%s) AND attr_id = '%s'", inList(studies), sqlLit(attrID)))
	for _, r := range sRows {
		key := r.StudyID + "\x00" + r.SampleID
		if !sampleKeys[key] {
			continue
		}
		if v, e := strconv.ParseFloat(r.Value, 64); e == nil {
			out[key] = v
		}
	}

	type patientRow struct {
		StudyID   string `json:"studyId"`
		PatientID string `json:"patientId"`
		Value     string `json:"attr_value"`
	}
	pRows, _ := ch.Query[patientRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, patient_id AS patientId, attr_value FROM clinical_patient "+
			"WHERE study_id IN (%s) AND attr_id = '%s'", inList(studies), sqlLit(attrID)))
	for _, r := range pRows {
		v, e := strconv.ParseFloat(r.Value, 64)
		if e != nil {
			continue
		}
		for _, sm := range patientToSamples[r.StudyID+"\x00"+r.PatientID] {
			key := sm.StudyID + "\x00" + sm.SampleID
			if _, seen := out[key]; !seen { // sample-level takes precedence
				out[key] = v
			}
		}
	}
	return out
}

// attrCategoriesBySample resolves a clinical attribute to a per-sample string
// category over the scoped samples (sample- and patient-level).
func (s *State) attrCategoriesBySample(samples []scopedSample, attrID string) map[string]string {
	out := map[string]string{}
	if attrID == "" || len(samples) == 0 {
		return out
	}
	sampleKeys := map[string]bool{}
	studySet := map[string]bool{}
	patientToSamples := map[string][]scopedSample{}
	for _, sm := range samples {
		sampleKeys[sm.StudyID+"\x00"+sm.SampleID] = true
		studySet[sm.StudyID] = true
		pk := sm.StudyID + "\x00" + sm.PatientID
		patientToSamples[pk] = append(patientToSamples[pk], sm)
	}
	studies := make([]string, 0, len(studySet))
	for st := range studySet {
		studies = append(studies, st)
	}

	type sampleRow struct {
		StudyID  string `json:"studyId"`
		SampleID string `json:"sampleId"`
		Value    string `json:"attr_value"`
	}
	sRows, _ := ch.Query[sampleRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS sampleId, attr_value FROM clinical_sample "+
			"WHERE study_id IN (%s) AND attr_id = '%s'", inList(studies), sqlLit(attrID)))
	for _, r := range sRows {
		key := r.StudyID + "\x00" + r.SampleID
		if sampleKeys[key] {
			out[key] = r.Value
		}
	}

	type patientRow struct {
		StudyID   string `json:"studyId"`
		PatientID string `json:"patientId"`
		Value     string `json:"attr_value"`
	}
	pRows, _ := ch.Query[patientRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, patient_id AS patientId, attr_value FROM clinical_patient "+
			"WHERE study_id IN (%s) AND attr_id = '%s'", inList(studies), sqlLit(attrID)))
	for _, r := range pRows {
		for _, sm := range patientToSamples[r.StudyID+"\x00"+r.PatientID] {
			key := sm.StudyID + "\x00" + sm.SampleID
			if _, seen := out[key]; !seen {
				out[key] = r.Value
			}
		}
	}
	return out
}

// ---------- 2. clinical data density plot ----------

type DensityPlotBin struct {
	BinX  float64 `json:"binX"`
	BinY  float64 `json:"binY"`
	Count int     `json:"count"`
	MinX  float64 `json:"minX"`
	MaxX  float64 `json:"maxX"`
	MinY  float64 `json:"minY"`
	MaxY  float64 `json:"maxY"`
}

type DensityPlotData struct {
	Bins         []DensityPlotBin `json:"bins"`
	PearsonCorr  float64          `json:"pearsonCorr"`
	SpearmanCorr float64          `json:"spearmanCorr"`
}

func queryFloat(r *http.Request, name string) (float64, bool) {
	if v := r.URL.Query().Get(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// logScaleValues maps every value to ln(1 + v) when the boolean query param
// is true and the attribute is MUTATION_COUNT, the only attribute the Java
// backend log-scales in density and violin plots.
func logScaleValues(r *http.Request, param, attrID string, vals map[string]float64) {
	if !strings.EqualFold(r.URL.Query().Get(param), "true") || attrID != "MUTATION_COUNT" {
		return
	}
	for k, v := range vals {
		vals[k] = math.Log(1 + v)
	}
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

	samples := s.scopedSamples(f)
	xVals := s.attrValuesBySample(samples, xAttr)
	yVals := s.attrValuesBySample(samples, yAttr)
	logScaleValues(r, "xAxisLogScale", xAttr, xVals)
	logScaleValues(r, "yAxisLogScale", yAttr, yVals)

	var xs, ys []float64
	for key, xv := range xVals {
		if yv, ok := yVals[key]; ok {
			xs = append(xs, xv)
			ys = append(ys, yv)
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
	for i := 0; i < xBins; i++ {
		for j := 0; j < yBins; j++ {
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
	if len(xs) > 1 {
		result.PearsonCorr = safeFloat(pearson(xs, ys))
		result.SpearmanCorr = safeFloat(spearman(xs, ys))
	}
	writeJSON(w, result)
}

// ---------- 3. clinical data violin plots ----------

type ClinicalViolinPlotBoxData struct {
	Median       float64 `json:"median"`
	Q1           float64 `json:"q1"`
	Q3           float64 `json:"q3"`
	WhiskerLower float64 `json:"whiskerLower"`
	WhiskerUpper float64 `json:"whiskerUpper"`
}

type ClinicalViolinPlotIndividualPoint struct {
	SampleID string  `json:"sampleId"`
	StudyID  string  `json:"studyId"`
	Value    float64 `json:"value"`
}

type ClinicalViolinPlotRowData struct {
	BoxData          ClinicalViolinPlotBoxData           `json:"boxData"`
	Category         string                              `json:"category"`
	CurveData        []float64                           `json:"curveData"`
	IndividualPoints []ClinicalViolinPlotIndividualPoint `json:"individualPoints"`
	NumSamples       int                                 `json:"numSamples"`
}

type ClinicalViolinPlotData struct {
	AxisStart float64                     `json:"axisStart"`
	AxisEnd   float64                     `json:"axisEnd"`
	Rows      []ClinicalViolinPlotRowData `json:"rows"`
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

	samples := s.scopedSamples(f)
	numVals := s.attrValuesBySample(samples, numAttr)
	logScaleValues(r, "logScale", numAttr, numVals)
	catVals := s.attrCategoriesBySample(samples, catAttr)

	sampleByKey := map[string]scopedSample{}
	for _, sm := range samples {
		sampleByKey[sm.StudyID+"\x00"+sm.SampleID] = sm
	}

	type point struct {
		sm  scopedSample
		val float64
	}
	byCategory := map[string][]point{}
	for key, v := range numVals {
		cat, ok := catVals[key]
		if !ok {
			continue
		}
		byCategory[cat] = append(byCategory[cat], point{sm: sampleByKey[key], val: v})
	}
	if len(byCategory) == 0 {
		writeJSON(w, result)
		return
	}

	axisStart := math.Inf(1)
	axisEnd := math.Inf(-1)

	type catBox struct {
		box                   ClinicalViolinPlotBoxData
		nonOutliers, outliers []point
		min, max              float64
	}
	boxes := map[string]*catBox{}
	categories := make([]string, 0, len(byCategory))
	for cat := range byCategory {
		categories = append(categories, cat)
	}
	sort.Strings(categories)

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
	result.AxisStart = safeFloat(axisStart)
	result.AxisEnd = safeFloat(axisEnd)

	// Curve sample points across the axis.
	var stepSize float64
	if numCurvePoints > 1 {
		stepSize = (result.AxisEnd - result.AxisStart) / float64(numCurvePoints-1)
	}
	curvePoints := make([]float64, numCurvePoints)
	for i := range curvePoints {
		curvePoints[i] = result.AxisStart + float64(i)*stepSize
	}
	sigma := sigmaMultiplier * stepSize

	for _, cat := range categories {
		cb := boxes[cat]
		row := ClinicalViolinPlotRowData{
			Category:         cat,
			BoxData:          clampWhiskers(cb.box, result.AxisStart, result.AxisEnd),
			CurveData:        []float64{},
			IndividualPoints: []ClinicalViolinPlotIndividualPoint{},
			NumSamples:       len(cb.nonOutliers) + len(cb.outliers),
		}
		var pointsToRender []point
		if len(cb.nonOutliers)+len(cb.outliers) <= violinShowOnlyPointsThreshold {
			pointsToRender = append(pointsToRender, cb.nonOutliers...)
			pointsToRender = append(pointsToRender, cb.outliers...)
		} else {
			curve := make([]float64, numCurvePoints)
			if sigma > 0 {
				for _, d := range cb.nonOutliers {
					for i, p := range curvePoints {
						z := (p - d.val) / sigma
						curve[i] += math.Exp(-0.5 * z * z)
					}
				}
			}
			row.CurveData = curve
			pointsToRender = cb.outliers
		}
		for _, d := range pointsToRender {
			row.IndividualPoints = append(row.IndividualPoints, ClinicalViolinPlotIndividualPoint{
				SampleID: d.sm.SampleID, StudyID: d.sm.StudyID, Value: d.val,
			})
		}
		result.Rows = append(result.Rows, row)
	}
	writeJSON(w, result)
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
