package molecular

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/stats"
)

// routeCoexpression registers the co-expression endpoint.
func (s Server) routeCoexpression(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/molecular-profiles/co-expressions/fetch", s.coExpressionsFetch)
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
	type coexpRow struct {
		EntrezGeneID int64    `json:"entrezGeneId"`
		Corr         *float64 `json:"corr"`
		N            int      `json:"n"`
	}
	p := ch.Params{"typeA": typeA, "hugo": hugo, "samples": uniqueIDs, "typeB": typeB}
	rows, err := ch.QueryP[coexpRow](s.CH, fmt.Sprintf(
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
