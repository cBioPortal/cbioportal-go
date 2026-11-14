package molecular

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/stats"
)

// routeCoexpression registers the co-expression endpoint.
func (s Server) routeCoexpression(mux *http.ServeMux) {
	core.Handle(mux, "POST /api/molecular-profiles/co-expressions/fetch", s.Bulk(), Server.coExpressionsFetch)
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

// Pearson returns the Pearson correlation of a and b, or NaN if undefined.
func Pearson(a, b []float64) float64 {
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

// Spearman returns the Spearman rank correlation of a and b, or NaN.
func Spearman(a, b []float64) float64 {
	return Pearson(ranks(a), ranks(b))
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
func (s Server) coExpressionsFetch(w http.ResponseWriter, r *http.Request) {
	var f coExpressionFilter
	core.ReadBody(r, &f)
	q := r.URL.Query()
	profileA, profileB := q.Get("molecularProfileIdA"), q.Get("molecularProfileIdB")
	threshold := 0.3
	if t := q.Get("threshold"); t != "" {
		v, err := strconv.ParseFloat(t, 64)
		if err != nil {
			core.WriteError(w, http.StatusBadRequest, `Request parameter type mismatch: java.lang.NumberFormatException: For input string: "`+t+`"`)
			return
		}
		threshold = v
	}
	out := []CoExpression{}
	if f.EntrezGeneID == nil {
		core.WriteJSON(w, out)
		return
	}
	sampleIDs := f.SampleIDs
	if f.SampleListID != nil {
		type row struct {
			SampleID string `json:"sampleId"`
		}
		p := ch.Params{}
		rows, err := ch.QueryP[row](s.CH, "SELECT sample_id AS sampleId FROM "+s.BySampleList(*f.SampleListID).From(core.ParamsBinder(p), core.TSampleList), p)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		sampleIDs = sampleIDs[:0]
		for _, rw := range rows {
			sampleIDs = append(sampleIDs, rw.SampleID)
		}
	}
	if len(sampleIDs) == 0 {
		core.WriteJSON(w, out)
		return
	}
	metaA, ok := s.ProfileMeta(profileA)
	if !ok {
		core.WriteError(w, http.StatusNotFound, core.MsgMolecularProfileNotFound(profileA))
		return
	}
	metaB := metaA
	if profileB != profileA {
		if metaB, ok = s.ProfileMeta(profileB); !ok {
			core.WriteError(w, http.StatusNotFound, core.MsgMolecularProfileNotFound(profileB))
			return
		}
	}
	type geneRow struct {
		Hugo string `json:"hugo"`
	}
	genes, err := ch.Query[geneRow](s.CH, fmt.Sprintf(
		"SELECT hugo_gene_symbol AS hugo FROM cbioportal_ref.gene WHERE entrez_gene_id = %d", *f.EntrezGeneID))
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(genes) == 0 {
		core.WriteError(w, http.StatusNotFound, core.MsgGeneNotFound(strconv.FormatInt(*f.EntrezGeneID, 10)))
		return
	}
	hugo := genes[0].Hugo
	typeA := strings.TrimPrefix(profileA, metaA.StudyID+"_")
	typeB := strings.TrimPrefix(profileB, metaB.StudyID+"_")
	uniqueIDs := make([]string, len(sampleIDs))
	for i, id := range sampleIDs {
		uniqueIDs[i] = metaA.StudyID + "_" + id
	}
	rows, packed, err := s.coExpressionByProfileRow(metaA, metaB, profileA, profileB, *f.EntrezGeneID, sampleIDs, threshold)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if !packed {
		p := ch.Params{"typeA": typeA, "hugo": hugo, "samples": uniqueIDs, "typeB": typeB}
		rows, err = ch.QueryP[coexpRow](s.CH, fmt.Sprintf(
			"WITH ref_values AS ("+
				"SELECT sample_unique_id, toFloat64OrNull(alteration_value) AS ref_val FROM %s "+
				"AND profile_type = {typeA:String} AND hugo_gene_symbol = {hugo:String} "+
				"AND alteration_value != '' AND alteration_value != 'NA' AND toFloat64OrNull(alteration_value) IS NOT NULL "+
				"AND sample_unique_id IN {samples:Array(String)}), "+
				"per_gene AS (SELECT g.entrez_gene_id AS entrezGeneId, count(*) AS numSamples, "+
				"uniqExact(toFloat64OrNull(d.alteration_value)) AS exactDistinct, uniq(toFloat64OrNull(d.alteration_value)) AS approxDistinct, "+
				"groupArray(r.ref_val) AS refValues, groupArray(toFloat64OrNull(d.alteration_value)) AS geneValues "+
				"FROM %s d INNER JOIN ref_values r ON d.sample_unique_id = r.sample_unique_id "+
				"INNER JOIN cbioportal_ref.gene g ON d.hugo_gene_symbol = g.hugo_gene_symbol "+
				"WHERE d.profile_type = {typeB:String} AND d.hugo_gene_symbol != {hugo:String} "+
				"AND d.alteration_value != '' AND d.alteration_value != 'NA' AND toFloat64OrNull(d.alteration_value) IS NOT NULL "+
				"GROUP BY g.entrez_gene_id), "+
				"scored AS (SELECT entrezGeneId, numSamples, "+
				"(numSamples >= 3 AND exactDistinct > 1 AND approxDistinct > numSamples / 2) AS validNumeric, "+
				"arrayReduce('rankCorr', if(exactDistinct > 1, refValues, [0., 1.]), if(exactDistinct > 1, geneValues, [0., 1.])) AS corr "+
				"FROM per_gene) "+
				"SELECT entrezGeneId, if(validNumeric AND isFinite(corr), corr, NULL) AS corr, toInt64(numSamples) AS n FROM scored "+
				"WHERE (validNumeric AND isFinite(corr) AND abs(corr) >= %v) OR (NOT validNumeric) ORDER BY entrezGeneId",
			core.ByStudy(metaA.StudyID).From(core.ParamsBinder(p), core.TGeneticAlterationDerived),
			core.ByStudy(metaB.StudyID).Rows(core.ParamsBinder(p), core.TGeneticAlterationDerived), threshold), p)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
	}
	for _, row := range rows {
		ce := CoExpression{GeneticEntityID: strconv.FormatInt(row.EntrezGeneID, 10), GeneticEntityType: "GENE"}
		if row.Corr != nil {
			p := stats.CommonsCorrelationPValue(*row.Corr, row.N)
			ce.SpearmansCorrelation, ce.PValue = row.Corr, &p
		}
		out = append(out, ce)
	}
	core.WriteJSON(w, out)
}

// coexpRow is one gene's co-expression with the query gene: its Spearman
// correlation over n samples, nil for a gene too sparse or too constant.
type coexpRow struct {
	EntrezGeneID int64    `json:"entrezGeneId"`
	Corr         *float64 `json:"corr"`
	N            int      `json:"n"`
}

// coExpressionByProfileRow is the co-expression over production's
// genetic_alteration, where the reference database holds it (a database
// built from production): one row per gene of a profile, with the values of
// the profile's samples in genetic_profile_samples order. It reads only
// profile B's rows, one per gene, where genetic_alteration_derived is read
// for every profile of the study and regrouped by gene. The query gene's
// values are lined up with profile B's samples first, so each gene's
// correlation is computed from its own row. false when the database holds
// no genetic_alteration.
func (s Server) coExpressionByProfileRow(metaA, metaB core.ProfileMeta, profileA, profileB string, entrez int64,
	sampleIDs []string, threshold float64) ([]coexpRow, bool, error) {
	if !slices.Contains(s.RefColumns("genetic_alteration"), "values") || !slices.Contains(s.RefColumns("genetic_profile"), "stable_id") ||
		!slices.Contains(s.RawColumns("profile_sample"), "position") {
		return nil, false, nil
	}
	type idRow struct {
		ID int64 `json:"id"`
	}
	profileIDs := map[string]int64{}
	for _, mp := range []string{profileA, profileB} {
		ids, err := core.CachedQueryP[idRow](s.State,
			"SELECT genetic_profile_id AS id FROM cbioportal_ref.genetic_profile WHERE stable_id = {id:String}", ch.Params{"id": mp})
		if err != nil {
			return nil, false, err
		}
		if len(ids) != 1 {
			return nil, false, nil
		}
		profileIDs[mp] = ids[0].ID
	}
	// The samples are matched by study and sample id: profile B's samples
	// of another study match none.
	if metaA.StudyID != metaB.StudyID {
		return []coexpRow{}, true, nil
	}
	type posRow struct {
		SampleID string `json:"sampleId"`
		Position uint64 `json:"position"`
	}
	positions := func(meta core.ProfileMeta) ([]posRow, error) {
		return ch.QueryP[posRow](s.CH, "SELECT sample_id AS sampleId, toUInt64(position) AS position FROM cbioportal_raw.profile_sample "+
			"WHERE study_id = {study:String} AND profile_stable_id = {profile:String} AND kind = 'header'",
			ch.Params{"study": meta.StudyID, "profile": meta.StableID})
	}
	samplesA, err := positions(metaA)
	if err != nil {
		return nil, false, err
	}
	samplesB, err := positions(metaB)
	if err != nil {
		return nil, false, err
	}
	type refRow struct {
		Values []string `json:"values"`
	}
	refs, err := ch.QueryP[refRow](s.CH, "SELECT splitByChar(',', substring(values, 1, -1)) AS values "+
		"FROM cbioportal_ref.genetic_alteration FINAL WHERE genetic_profile_id = {pid:Int64} AND genetic_entity_id IN "+
		"(SELECT genetic_entity_id FROM cbioportal_ref.gene WHERE entrez_gene_id = {entrez:Int64}) LIMIT 1",
		ch.Params{"pid": profileIDs[profileA], "entrez": entrez})
	if err != nil {
		return nil, false, err
	}
	if len(refs) == 0 {
		return []coexpRow{}, true, nil
	}
	requested := map[string]bool{}
	for _, id := range sampleIDs {
		requested[id] = true
	}
	refBySample := map[string]string{}
	for _, r := range samplesA {
		if requested[r.SampleID] && r.Position >= 1 && int(r.Position) <= len(refs[0].Values) {
			refBySample[r.SampleID] = refs[0].Values[r.Position-1]
		}
	}
	// The query gene's value at each of profile B's positions; '' (no
	// number) where the sample is not requested.
	width := uint64(0)
	for _, r := range samplesB {
		width = max(width, r.Position)
	}
	ref := make([]string, width)
	for _, r := range samplesB {
		if r.Position >= 1 {
			ref[r.Position-1] = refBySample[r.SampleID]
		}
	}
	// Profile B's rows in ranges of about equal size, read as the parts of a
	// UNION ALL: a profile's rows are few (one per gene) and each is large,
	// so ClickHouse would otherwise read and score them in one or two
	// streams. Small blocks keep the query gene's values, which ClickHouse
	// repeats for every row of a block, small.
	type boundsRow struct {
		Bounds []int64 `json:"bounds"`
	}
	bounds, err := ch.QueryP[boundsRow](s.CH, fmt.Sprintf("SELECT quantilesExact(%s)(genetic_entity_id) AS bounds "+
		"FROM cbioportal_ref.genetic_alteration WHERE genetic_profile_id = {pid:Int64}", coexpRangeQuantiles()),
		ch.Params{"pid": profileIDs[profileB]})
	if err != nil {
		return nil, false, err
	}
	var cuts []int64
	if len(bounds) == 1 {
		for _, b := range bounds[0].Bounds {
			if len(cuts) == 0 || b > cuts[len(cuts)-1] {
				cuts = append(cuts, b)
			}
		}
	}
	var ranges []string
	for i := 0; i <= len(cuts); i++ {
		cond := ""
		if i > 0 {
			cond += fmt.Sprintf(" AND genetic_entity_id >= %d", cuts[i-1])
		}
		if i < len(cuts) {
			cond += fmt.Sprintf(" AND genetic_entity_id < %d", cuts[i])
		}
		ranges = append(ranges, "SELECT genetic_entity_id, values FROM cbioportal_ref.genetic_alteration FINAL "+
			"WHERE genetic_profile_id = {pid:Int64}"+cond)
	}
	rowsSQL := strings.Join(ranges, " UNION ALL ")

	// The query gene's numbers once (a scalar subquery): which positions
	// hold one, and their values. Each gene's row is then cut to those
	// positions and to its own numbers, as the derived tables' join of the
	// two genes' numeric values over the samples does.
	rows, err := ch.QueryP[coexpRow](s.CH,
		"WITH (SELECT tuple(arrayMap(x -> toUInt8(isNotNull(x)), p), arrayMap(x -> assumeNotNull(x), arrayFilter(x -> isNotNull(x), p))) "+
			"FROM (SELECT arrayMap(x -> toFloat64OrNull(x), {ref:Array(String)}) AS p)) AS q "+
			"SELECT g.entrez_gene_id AS entrezGeneId, if(validNumeric AND isFinite(c), c, NULL) AS corr, toInt64(numSamples) AS n "+
			"FROM (SELECT genetic_entity_id, numSamples, (numSamples >= 3 AND exactDistinct > 1 AND approxDistinct > numSamples / 2) AS validNumeric, "+
			"arrayReduce('rankCorr', if(exactDistinct > 1, refValues, [0., 1.]), if(exactDistinct > 1, geneValues, [0., 1.])) AS c "+
			"FROM (SELECT genetic_entity_id, length(geneValues) AS numSamples, arrayReduce('uniqExact', geneValues) AS exactDistinct, "+
			"arrayReduce('uniq', geneValues) AS approxDistinct, refValues, geneValues "+
			"FROM (SELECT genetic_entity_id, arrayMap(x -> toFloat64OrNull(x), arrayFilter((x, m) -> m = 1, "+
			"arrayResize(splitByChar(',', substring(values, 1, -1)), {width:UInt64}), tupleElement(q, 1))) AS gv, "+
			"arrayMap(x -> assumeNotNull(x), arrayFilter(x -> isNotNull(x), gv)) AS geneValues, "+
			"arrayFilter((r, x) -> isNotNull(x), tupleElement(q, 2), gv) AS refValues "+
			"FROM ("+rowsSQL+"))) WHERE numSamples > 0) t "+
			"INNER JOIN (SELECT genetic_entity_id, entrez_gene_id FROM cbioportal_ref.gene) g ON g.genetic_entity_id = t.genetic_entity_id "+
			fmt.Sprintf("WHERE g.entrez_gene_id != {entrez:Int64} AND ((validNumeric AND isFinite(c) AND abs(c) >= %v) OR (NOT validNumeric)) "+
				"ORDER BY entrezGeneId SETTINGS max_block_size = 128", threshold),
		ch.Params{"ref": ref, "width": width, "pid": profileIDs[profileB], "entrez": entrez})
	if err != nil {
		return nil, false, err
	}
	return rows, true, nil
}

// coexpRanges is the number of ranges co-expression reads a profile's rows
// in: the cores of one ClickHouse server.
const coexpRanges = 8

// coexpRangeQuantiles lists the quantiles that cut a profile's genes into
// coexpRanges ranges.
func coexpRangeQuantiles() string {
	q := make([]string, coexpRanges-1)
	for i := range q {
		q[i] = strconv.FormatFloat(float64(i+1)/coexpRanges, 'f', -1, 64)
	}
	return strings.Join(q, ", ")
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
