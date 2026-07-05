package api

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/stats"
)

// ---------------------------------------------------------------------------
// Response types (camelCase shapes from CBioPortalAPIInternal.ts)
// ---------------------------------------------------------------------------

// GroupStatistics mirrors the client GroupStatistics: per-group mean/stddev of a
// continuous molecular value.
type GroupStatistics struct {
	MeanExpression    float64 `json:"meanExpression"`
	Name              string  `json:"name"`
	StandardDeviation float64 `json:"standardDeviation"`
}

// GenomicEnrichment is the expression-enrichments response element.
type GenomicEnrichment struct {
	Cytoband         string            `json:"cytoband"`
	EntrezGeneID     int64             `json:"entrezGeneId"`
	GroupsStatistics []GroupStatistics `json:"groupsStatistics"`
	HugoGeneSymbol   string            `json:"hugoGeneSymbol"`
	PValue           float64           `json:"pValue"`
	QValue           float64           `json:"qValue"`
}

// GenericAssayEnrichment is the generic-assay-enrichments response element.
type GenericAssayEnrichment struct {
	GenericEntityMetaProperties map[string]string `json:"genericEntityMetaProperties"`
	GroupsStatistics            []GroupStatistics `json:"groupsStatistics"`
	Name                        string            `json:"name"`
	PValue                      float64           `json:"pValue"`
	QValue                      float64           `json:"qValue"`
	StableID                    string            `json:"stableId"`
}

// GenericAssayCountSummary mirrors the client type for binary enrichments.
type GenericAssayCountSummary struct {
	Count      int    `json:"count"`
	Name       string `json:"name"`
	TotalCount int    `json:"totalCount"`
}

// GenericAssayBinaryEnrichment is the generic-assay-binary-enrichments element.
type GenericAssayBinaryEnrichment struct {
	Counts                      []GenericAssayCountSummary `json:"counts"`
	GenericEntityMetaProperties map[string]string          `json:"genericEntityMetaProperties"`
	GroupsStatistics            []GroupStatistics          `json:"groupsStatistics"`
	Name                        string                     `json:"name"`
	PValue                      float64                    `json:"pValue"`
	QValue                      float64                    `json:"qValue"`
	StableID                    string                     `json:"stableId"`
}

// GenericAssayCategoricalEnrichment is the generic-assay-categorical-enrichments
// element.
type GenericAssayCategoricalEnrichment struct {
	GenericEntityMetaProperties map[string]string `json:"genericEntityMetaProperties"`
	GroupsStatistics            []GroupStatistics `json:"groupsStatistics"`
	Name                        string            `json:"name"`
	PValue                      float64           `json:"pValue"`
	QValue                      float64           `json:"qValue"`
	StableID                    string            `json:"stableId"`
}

// ---------------------------------------------------------------------------
// Continuous enrichment core (expression + generic assay)
// ---------------------------------------------------------------------------

// contRow is one gene/entity's collected numeric values for a single group.
type contRow struct {
	Symbol string    `json:"symbol"`
	Entrez *int64    `json:"entrez"`
	Vals   []float64 `json:"vals"`
}

// contResult is a per-gene/entity accumulated result: the per-group mean/stddev
// statistics (already computed) plus the group-comparison p-value.
type contResult struct {
	hugo             string
	entrez           int64
	stable           string
	groupsStatistics []GroupStatistics
	pValue           float64
}

// contMWRow is one gene/entity's ClickHouse-side statistics for the 2-group
// Mann-Whitney path. mean/stddev are nullable (nan -> JSON null for empty or
// singleton groups); pval is null only when a group is empty (treated as 1.0).
type contMWRow struct {
	Symbol string   `json:"symbol"`
	Entrez *int64   `json:"entrez"`
	Mean0  *float64 `json:"mean0"`
	Sd0    *float64 `json:"sd0"`
	Mean1  *float64 `json:"mean1"`
	Sd1    *float64 `json:"sd1"`
	Pval   *float64 `json:"pval"`
}

// resolveGroupTriples turns a group's case identifiers into
// (study_id, profile_stable_id, sample_id) triples plus the staging table the
// referenced profiles live in. metaCache memoises molecular-profile lookups.
func (s *State) resolveGroupTriples(g casesGroup, metaCache map[string]profileMeta) ([][3]string, string) {
	triples := make([][3]string, 0, len(g.MolecularProfileCaseIdentifiers))
	table := ""
	for _, c := range g.MolecularProfileCaseIdentifiers {
		meta, seen := metaCache[c.MolecularProfileID]
		if !seen {
			m, ok := s.profileMeta(c.MolecularProfileID)
			if !ok {
				m = profileMeta{}
			}
			metaCache[c.MolecularProfileID] = m
			meta = m
		}
		if meta.StudyID == "" {
			continue
		}
		if table == "" {
			table = meta.dataTable()
		}
		triples = append(triples, [3]string{meta.StudyID, meta.StableID, c.CaseID})
	}
	return triples, table
}

// tripleIn renders a ClickHouse tuple-membership list for 3-tuples.
func tripleIn(triples [][3]string) string {
	q := make([]string, len(triples))
	for i, t := range triples {
		q[i] = fmt.Sprintf("('%s', '%s', '%s')", sqlLit(t[0]), sqlLit(t[1]), sqlLit(t[2]))
	}
	return strings.Join(q, ", ")
}

// computeContinuous returns, per gene/entity, the per-group mean/stddev
// statistics and a group-comparison p-value, sorted ascending by p-value. keyCol
// is the matrix column that identifies a row (hugo_symbol for expression,
// entity_stable_id for generic assay); withGene selects whether hugo/entrez are
// projected.
//
// For the common 2-group case the whole statistic is pushed into ClickHouse via
// the native mannWhitneyUTest aggregate (Kruskal-Wallis with two groups is the
// Mann-Whitney / Wilcoxon rank-sum test, giving the equivalent p-value), so only
// a few thousand small per-gene result rows return to Go instead of every gene's
// raw values across every sample. For the rare >2-group case (ClickHouse has no
// Kruskal-Wallis aggregate) it falls back to pulling values and running
// Kruskal-Wallis in Go.
func (s *State) computeContinuous(groups []casesGroup, keyCol string, withGene bool) ([]contResult, error) {
	if len(groups) == 2 {
		return s.computeContinuousMW(groups, keyCol, withGene)
	}
	return s.computeContinuousKW(groups, keyCol, withGene)
}

// computeContinuousMW computes, per gene/entity, the two group means/stddevs and
// a Mann-Whitney p-value entirely inside ClickHouse. Each sample's value is
// tagged with its group index (0 or 1); avgIf/stddevSampIf give the per-group
// statistics and mannWhitneyUTest(value, group) gives (u, p) — only per-gene
// result rows cross the wire. Genes present in only one group can't be tested
// (the aggregate errors on an empty group), so the p-value is guarded to 1.0.
func (s *State) computeContinuousMW(groups []casesGroup, keyCol string, withGene bool) ([]contResult, error) {
	metaCache := map[string]profileMeta{}
	g0, table0 := s.resolveGroupTriples(groups[0], metaCache)
	g1, table1 := s.resolveGroupTriples(groups[1], metaCache)
	table := table0
	if table == "" {
		table = table1
	}
	if table == "" || len(g0) == 0 || len(g1) == 0 {
		return []contResult{}, nil
	}
	all := append(append([][3]string{}, g0...), g1...)

	var innerCols, midKey, groupBy string
	if withGene {
		innerCols = "hugo_symbol, entrez_gene_id"
		midKey = "hugo_symbol AS symbol, toInt64OrNull(entrez_gene_id) AS entrez"
		groupBy = "hugo_symbol, entrez_gene_id"
	} else {
		innerCols = keyCol
		midKey = keyCol + " AS symbol"
		groupBy = keyCol
	}

	sql := fmt.Sprintf(
		"SELECT symbol, entrez, mean0, sd0, mean1, sd1, "+
			"if(n0 > 0 AND n1 > 0, "+
			"arrayReduce('mannWhitneyUTest', arrayConcat(a0, a1), "+
			"arrayConcat(arrayResize(CAST([], 'Array(UInt8)'), n0, toUInt8(0)), "+
			"arrayResize(CAST([], 'Array(UInt8)'), n1, toUInt8(1)))).2, 1.0) AS pval "+
			"FROM ("+
			"SELECT %s, "+
			"groupArrayIf(v, grp = 0) AS a0, groupArrayIf(v, grp = 1) AS a1, "+
			"length(a0) AS n0, length(a1) AS n1, "+
			"avgIf(v, grp = 0) AS mean0, stddevSampIf(v, grp = 0) AS sd0, "+
			"avgIf(v, grp = 1) AS mean1, stddevSampIf(v, grp = 1) AS sd1 "+
			"FROM ("+
			"SELECT %s, toFloat64OrNull(value) AS v, "+
			"if((study_id, profile_stable_id, sample_id) IN (%s), 0, 1) AS grp "+
			"FROM cbioportal_raw.%s "+
			"WHERE (study_id, profile_stable_id, sample_id) IN (%s) "+
			"AND toFloat64OrNull(value) IS NOT NULL"+
			") GROUP BY %s"+
			")",
		midKey, innerCols, tripleIn(g0), table, tripleIn(all), groupBy)

	rows, err := ch.Query[contMWRow](s.CH, sql)
	if err != nil {
		return nil, err
	}
	out := make([]contResult, 0, len(rows))
	for _, row := range rows {
		var e int64
		if row.Entrez != nil {
			e = *row.Entrez
		}
		p := 1.0
		if row.Pval != nil && !math.IsNaN(*row.Pval) {
			p = *row.Pval
		}
		gs := []GroupStatistics{
			{MeanExpression: derefFloat(row.Mean0), Name: groups[0].Name, StandardDeviation: derefFloat(row.Sd0)},
			{MeanExpression: derefFloat(row.Mean1), Name: groups[1].Name, StandardDeviation: derefFloat(row.Sd1)},
		}
		out = append(out, contResult{
			hugo:             row.Symbol,
			entrez:           e,
			stable:           row.Symbol,
			groupsStatistics: gs,
			pValue:           p,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pValue < out[j].pValue })
	return out, nil
}

// derefFloat returns *p, or 0 for a nil / nan / inf value.
func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return safeFloat(*p)
}

// computeContinuousKW gathers, per gene/entity and per group, the numeric values
// across the group's samples, then computes a Kruskal-Wallis p-value in Go. Used
// only for the rare >2-group case that ClickHouse cannot aggregate directly.
func (s *State) computeContinuousKW(groups []casesGroup, keyCol string, withGene bool) ([]contResult, error) {
	n := len(groups)
	metaCache := map[string]profileMeta{}

	type acc struct {
		hugo   string
		entrez int64
		stable string
		vals   [][]float64
	}
	m := map[string]*acc{}

	for gi, g := range groups {
		triples, table := s.resolveGroupTriples(g, metaCache)
		if table == "" || len(triples) == 0 {
			continue
		}
		var sel, grp string
		if withGene {
			sel = "hugo_symbol AS symbol, toInt64OrNull(entrez_gene_id) AS entrez, groupArray(toFloat64OrNull(value)) AS vals"
			grp = "hugo_symbol, entrez_gene_id"
		} else {
			sel = keyCol + " AS symbol, groupArray(toFloat64OrNull(value)) AS vals"
			grp = keyCol
		}
		rows, err := ch.Query[contRow](s.CH, fmt.Sprintf(
			"SELECT %s FROM cbioportal_raw.%s WHERE (study_id, profile_stable_id, sample_id) IN (%s) "+
				"AND toFloat64OrNull(value) IS NOT NULL GROUP BY %s",
			sel, table, tripleIn(triples), grp))
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			a := m[row.Symbol]
			if a == nil {
				a = &acc{stable: row.Symbol, hugo: row.Symbol, vals: make([][]float64, n)}
				if row.Entrez != nil {
					a.entrez = *row.Entrez
				}
				m[row.Symbol] = a
			}
			a.vals[gi] = row.Vals
		}
	}

	out := make([]contResult, 0, len(m))
	for _, a := range m {
		out = append(out, contResult{
			hugo:             a.hugo,
			entrez:           a.entrez,
			stable:           a.stable,
			groupsStatistics: groupStatistics(groups, a.vals),
			pValue:           stats.KruskalWallisTest(a.vals),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pValue < out[j].pValue })
	return out, nil
}

// meanStddev returns the mean and sample (n-1) standard deviation of v.
func meanStddev(v []float64) (float64, float64) {
	n := len(v)
	if n == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	mean := sum / float64(n)
	if n < 2 {
		return mean, 0
	}
	var ss float64
	for _, x := range v {
		d := x - mean
		ss += d * d
	}
	return mean, math.Sqrt(ss / float64(n-1))
}

// benjaminiHochberg returns BH-adjusted q-values for p-values sorted ascending.
func benjaminiHochberg(sortedP []float64) []float64 {
	n := len(sortedP)
	q := make([]float64, n)
	prev := 1.0
	for i := n - 1; i >= 0; i-- {
		v := sortedP[i] * float64(n) / float64(i+1)
		if v > prev {
			v = prev
		}
		if v > 1 {
			v = 1
		}
		q[i] = v
		prev = v
	}
	return q
}

func groupStatistics(groups []casesGroup, groupVals [][]float64) []GroupStatistics {
	gs := make([]GroupStatistics, len(groups))
	for gi, g := range groups {
		var vals []float64
		if gi < len(groupVals) {
			vals = groupVals[gi]
		}
		mean, sd := meanStddev(vals)
		gs[gi] = GroupStatistics{MeanExpression: mean, Name: g.Name, StandardDeviation: sd}
	}
	return gs
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// expressionEnrichmentsFetch computes per-gene expression enrichment across the
// posted groups: per-group mean/stddev and a Kruskal-Wallis p-value.
func (s *State) expressionEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var groups []casesGroup
	readBody(r, &groups)
	if len(groups) < 2 {
		writeJSON(w, []GenomicEnrichment{})
		return
	}
	results, err := s.computeContinuous(groups, "hugo_symbol", true)
	if err != nil {
		fail(w, err)
		return
	}
	ps := make([]float64, len(results))
	for i, res := range results {
		ps[i] = res.pValue
	}
	qs := benjaminiHochberg(ps)
	out := make([]GenomicEnrichment, len(results))
	for i, res := range results {
		out[i] = GenomicEnrichment{
			EntrezGeneID:     res.entrez,
			GroupsStatistics: res.groupsStatistics,
			HugoGeneSymbol:   res.hugo,
			PValue:           res.pValue,
			QValue:           qs[i],
		}
	}
	writeJSON(w, out)
}

// genericAssayEnrichmentsFetch computes per-entity continuous enrichment over the
// generic-assay matrix (keyed by entity_stable_id).
func (s *State) genericAssayEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var groups []casesGroup
	readBody(r, &groups)
	if len(groups) < 2 {
		writeJSON(w, []GenericAssayEnrichment{})
		return
	}
	results, err := s.computeContinuous(groups, "entity_stable_id", false)
	if err != nil {
		fail(w, err)
		return
	}
	ps := make([]float64, len(results))
	for i, res := range results {
		ps[i] = res.pValue
	}
	qs := benjaminiHochberg(ps)
	out := make([]GenericAssayEnrichment, len(results))
	for i, res := range results {
		out[i] = GenericAssayEnrichment{
			GenericEntityMetaProperties: map[string]string{},
			GroupsStatistics:            res.groupsStatistics,
			Name:                        res.stable,
			PValue:                      res.pValue,
			QValue:                      qs[i],
			StableID:                    res.stable,
		}
	}
	writeJSON(w, out)
}

// genericAssayBinaryEnrichmentsFetch returns a correctly-typed empty array; the
// binary categorical value semantics are not yet modelled in the staging tables.
func (s *State) genericAssayBinaryEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	readBody(r, nil)
	writeJSON(w, []GenericAssayBinaryEnrichment{})
}

// genericAssayCategoricalEnrichmentsFetch returns a correctly-typed empty array.
func (s *State) genericAssayCategoricalEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	readBody(r, nil)
	writeJSON(w, []GenericAssayCategoricalEnrichment{})
}

// copyNumberEnrichmentsFetch computes per-gene discrete copy-number enrichment
// across the posted groups, optionally scoped to a single CNA event type
// (copyNumberEventType=AMP -> 2, HOMDEL -> -2), with a Fisher-exact p-value.
func (s *State) copyNumberEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Groups []casesGroup `json:"molecularProfileCasesGroupFilter"`
	}
	readBody(r, &body)
	if len(body.Groups) < 2 {
		writeJSON(w, []AlterationEnrichment{})
		return
	}

	altFilter := ""
	switch strings.ToUpper(r.URL.Query().Get("copyNumberEventType")) {
	case "AMP", "2":
		altFilter = " AND alteration = '2'"
	case "HOMDEL", "-2":
		altFilter = " AND alteration = '-2'"
	}

	type geneKey struct {
		symbol string
		entrez int64
	}
	altered := make([]map[geneKey]int, len(body.Groups))
	profiled := make([]int, len(body.Groups))
	geneSet := map[geneKey]bool{}

	for gi, g := range body.Groups {
		altered[gi] = map[geneKey]int{}
		pairs := g.pairs()
		profiled[gi] = len(pairs)
		if len(pairs) == 0 {
			continue
		}
		rows, err := ch.Query[geneAlteredRow](s.CH, fmt.Sprintf(
			"SELECT hugo_symbol AS hugoGeneSymbol, toInt64OrNull(entrez_gene_id) AS entrezGeneId, uniqExact(sample_id) AS altered "+
				"FROM cna WHERE (study_id, sample_id) IN (%s)%s GROUP BY hugo_symbol, entrez_gene_id",
			tupleIn(pairs), altFilter))
		if err != nil {
			fail(w, err)
			return
		}
		for _, row := range rows {
			var e int64
			if row.EntrezGeneID != nil {
				e = *row.EntrezGeneID
			}
			k := geneKey{row.HugoGeneSymbol, e}
			altered[gi][k] = row.Altered
			geneSet[k] = true
		}
	}

	out := make([]AlterationEnrichment, 0, len(geneSet))
	for k := range geneSet {
		counts := make([]CountSummary, len(body.Groups))
		for gi, g := range body.Groups {
			counts[gi] = CountSummary{AlteredCount: altered[gi][k], Name: g.Name, ProfiledCount: profiled[gi]}
		}
		p := 1.0
		if len(body.Groups) == 2 {
			a := counts[0].AlteredCount
			b := counts[0].ProfiledCount - a
			c := counts[1].AlteredCount
			d := counts[1].ProfiledCount - c
			if b >= 0 && d >= 0 {
				p = stats.FisherExactTest(a, b, c, d)
			}
		}
		out = append(out, AlterationEnrichment{
			Counts: counts, EntrezGeneID: k.entrez, HugoGeneSymbol: k.symbol, PValue: p,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PValue < out[j].PValue })
	writeJSON(w, out)
}

func (s *State) routeEnrichment2(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/expression-enrichments/fetch", s.expressionEnrichmentsFetch)
	mux.HandleFunc("POST /api/copy-number-enrichments/fetch", s.copyNumberEnrichmentsFetch)
	mux.HandleFunc("POST /api/generic-assay-enrichments/fetch", s.genericAssayEnrichmentsFetch)
	mux.HandleFunc("POST /api/generic-assay-binary-enrichments/fetch", s.genericAssayBinaryEnrichmentsFetch)
	mux.HandleFunc("POST /api/generic-assay-categorical-enrichments/fetch", s.genericAssayCategoricalEnrichmentsFetch)
}
