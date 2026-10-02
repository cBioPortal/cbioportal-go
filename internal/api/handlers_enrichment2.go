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

// GenericAssayBinaryEnrichment is the generic-assay-binary-enrichments element
// (Java leaves name unset, so it is not serialized).
type GenericAssayBinaryEnrichment struct {
	Counts                      []GenericAssayCountSummary `json:"counts"`
	GenericEntityMetaProperties map[string]string          `json:"genericEntityMetaProperties"`
	GroupsStatistics            []GroupStatistics          `json:"groupsStatistics"`
	PValue                      float64                    `json:"pValue"`
	QValue                      float64                    `json:"qValue"`
	StableID                    string                     `json:"stableId"`
}

// GenericAssayCategoricalEnrichment is the generic-assay-categorical-enrichments
// element (Java leaves name unset, so it is not serialized).
type GenericAssayCategoricalEnrichment struct {
	GenericEntityMetaProperties map[string]string            `json:"genericEntityMetaProperties"`
	GroupsStatistics            []CategoricalGroupStatistics `json:"groupsStatistics"`
	PValue                      float64                      `json:"pValue"`
	QValue                      float64                      `json:"qValue"`
	StableID                    string                       `json:"stableId"`
}

// ---------------------------------------------------------------------------
// Continuous enrichment core (expression + generic assay)
// ---------------------------------------------------------------------------

// contResult is a per-gene/entity accumulated result: the per-group mean/stddev
// statistics (already computed) plus the group-comparison p-value.
type contResult struct {
	hugo             string
	entrez           int64
	stable           string
	groupsStatistics []GroupStatistics
	pValue           float64
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

// contStatRow is one gene/entity's count, mean and sample variance of the
// numeric values within one group.
type contStatRow struct {
	Symbol string   `json:"symbol"`
	Entrez *int64   `json:"entrez"`
	Grp    uint32   `json:"grp"`
	N      uint64   `json:"n"`
	Mean   *float64 `json:"mean"`
	Var    *float64 `json:"var"`
}

// computeContinuous returns, per gene/entity, the per-group mean/stddev
// statistics and a group-comparison p-value, sorted ascending by p-value, as
// cbioportal's ExpressionEnrichmentUtil computes them: values of rna_seq
// profiles are log2(x+1) transformed (negatives clamped to 0), groups with
// fewer than two values are left out, and entities need two remaining groups.
// The p-value is Welch's t-test for two groups and a one-way ANOVA otherwise;
// entities with an undefined p-value are skipped. Only per-group summary
// statistics leave ClickHouse. keyCol is the matrix column that identifies a
// row (entity_stable_id for generic assay); withGene keys rows on hugo/entrez.
func (s *State) computeContinuous(groups []casesGroup, keyCol string, withGene bool) ([]contResult, error) {
	key := keyCol + " AS symbol, CAST(NULL AS Nullable(Int64)) AS entrez"
	if withGene {
		key = "hugo_symbol AS symbol, toInt64OrNull(entrez_gene_id) AS entrez"
	}
	metaCache := map[string]profileMeta{}
	var parts []string
	for gi, g := range groups {
		triples, table := s.resolveGroupTriples(g, metaCache)
		if table == "" || len(triples) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT %s, toUInt32(%d) AS grp, "+
				"if(position(concat(study_id, '_', profile_stable_id), 'rna_seq') > 0, log2(greatest(x, 0) + 1), x) AS v "+
				"FROM (SELECT *, toFloat64OrNull(value) AS x FROM cbioportal_raw.%s "+
				"WHERE (study_id, profile_stable_id, sample_id) IN (%s)) WHERE isFinite(x)",
			key, gi, table, tripleIn(triples)))
	}
	if len(parts) < 2 {
		return []contResult{}, nil
	}
	rows, err := ch.Query[contStatRow](s.CH, fmt.Sprintf(
		"SELECT symbol, entrez, grp, count() AS n, avg(v) AS mean, varSamp(v) AS var FROM (%s) "+
			"GROUP BY symbol, entrez, grp",
		strings.Join(parts, " UNION ALL ")))
	if err != nil {
		return nil, err
	}

	// Groups are reported in the iteration order of Java's HashMap keyed on
	// group name.
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = g.Name
	}
	rank := map[string]int{}
	for i, n := range javaHashMapOrder(names) {
		rank[n] = i
	}
	type acc struct {
		row   contStatRow
		stats []*contStatRow
	}
	bySymbol := map[string]*acc{}
	var order []string
	for i := range rows {
		r := &rows[i]
		a := bySymbol[r.Symbol]
		if a == nil {
			a = &acc{row: *r, stats: make([]*contStatRow, len(groups))}
			bySymbol[r.Symbol] = a
			order = append(order, r.Symbol)
		}
		a.stats[r.Grp] = r
	}

	out := make([]contResult, 0, len(order))
	for _, sym := range order {
		a := bySymbol[sym]
		idx := make([]int, 0, len(groups))
		for gi, st := range a.stats {
			if st == nil || st.N < 2 || st.Mean == nil || st.Var == nil ||
				math.IsNaN(*st.Mean) || math.IsNaN(*st.Var) {
				continue
			}
			idx = append(idx, gi)
		}
		if len(idx) < 2 {
			continue
		}
		sort.SliceStable(idx, func(i, j int) bool { return rank[groups[idx[i]].Name] < rank[groups[idx[j]].Name] })
		ns := make([]int, len(idx))
		means := make([]float64, len(idx))
		vars := make([]float64, len(idx))
		gs := make([]GroupStatistics, len(idx))
		for k, gi := range idx {
			st := a.stats[gi]
			ns[k], means[k], vars[k] = int(st.N), *st.Mean, *st.Var
			gs[k] = GroupStatistics{MeanExpression: means[k], Name: groups[gi].Name, StandardDeviation: math.Sqrt(vars[k])}
		}
		var p float64
		if len(idx) == 2 {
			p = stats.WelchTTest(ns[0], means[0], vars[0], ns[1], means[1], vars[1])
		} else {
			p = stats.OneWayAnovaP(ns, means, vars)
		}
		if math.IsNaN(p) {
			continue
		}
		var e int64
		if a.row.Entrez != nil {
			e = *a.row.Entrez
		}
		out = append(out, contResult{hugo: sym, entrez: e, stable: sym, groupsStatistics: gs, pValue: p})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].pValue < out[j].pValue })
	return out, nil
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

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// expressionEnrichmentsFetch computes per-gene expression enrichment across the
// posted groups: per-group mean/stddev and a Kruskal-Wallis p-value.
func (s *State) expressionEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var groups []casesGroup
	readBody(r, &groups)
	if strings.EqualFold(r.URL.Query().Get("enrichmentType"), "PATIENT") {
		groups = s.patientCasesToSamples(groups)
	}
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
	if strings.EqualFold(r.URL.Query().Get("enrichmentType"), "PATIENT") {
		groups = s.patientCasesToSamples(groups)
	}
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
	props := map[string]map[string]string{}
	if pairs := s.groupProfilePairs(groups); len(pairs) > 0 {
		props, _ = s.genericAssayMetaProperties(fmt.Sprintf("(study_id, profile_stable_id) IN (%s)", tupleIn(pairs)))
	}
	out := make([]GenericAssayEnrichment, len(results))
	for i, res := range results {
		meta := props[res.stable]
		if meta == nil {
			meta = map[string]string{}
		}
		out[i] = GenericAssayEnrichment{
			GenericEntityMetaProperties: meta,
			GroupsStatistics:            res.groupsStatistics,
			Name:                        res.stable,
			PValue:                      res.pValue,
			QValue:                      qs[i],
			StableID:                    res.stable,
		}
	}
	writeJSON(w, out)
}

// groupProfilePairs lists the (study, profile stable id) of every molecular
// profile the groups reference.
func (s *State) groupProfilePairs(groups []casesGroup) [][2]string {
	seen := map[string]bool{}
	var pairs [][2]string
	for _, g := range groups {
		for _, c := range g.MolecularProfileCaseIdentifiers {
			if seen[c.MolecularProfileID] {
				continue
			}
			seen[c.MolecularProfileID] = true
			if m, ok := s.profileMeta(c.MolecularProfileID); ok {
				pairs = append(pairs, [2]string{m.StudyID, m.StableID})
			}
		}
	}
	return pairs
}

// patientCasesToSamples rewrites groups whose case ids are patient ids
// (enrichmentType=PATIENT) to the samples of those patients, as the Java
// backend compares every sample of a patient.
func (s *State) patientCasesToSamples(groups []casesGroup) []casesGroup {
	metaCache := map[string]profileMeta{}
	var pairs [][2]string
	for _, g := range groups {
		for _, c := range g.MolecularProfileCaseIdentifiers {
			m, seen := metaCache[c.MolecularProfileID]
			if !seen {
				m, _ = s.profileMeta(c.MolecularProfileID)
				metaCache[c.MolecularProfileID] = m
			}
			pairs = append(pairs, [2]string{m.StudyID, c.CaseID})
		}
	}
	samplesOf := map[string][]string{}
	if len(pairs) > 0 {
		rows, _ := ch.Query[idRow](s.CH, fmt.Sprintf(
			"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM sample "+
				"WHERE (study_id, patient_id) IN (%s) ORDER BY study_id, sample_id", tupleIn(pairs)))
		for _, r := range rows {
			k := jkey(r.StudyID, r.PatientID)
			samplesOf[k] = append(samplesOf[k], r.SampleID)
		}
	}
	out := make([]casesGroup, len(groups))
	for i, g := range groups {
		out[i] = casesGroup{Name: g.Name}
		for _, c := range g.MolecularProfileCaseIdentifiers {
			for _, sid := range samplesOf[jkey(metaCache[c.MolecularProfileID].StudyID, c.CaseID)] {
				out[i].MolecularProfileCaseIdentifiers = append(out[i].MolecularProfileCaseIdentifiers,
					caseIdentifier{CaseID: sid, MolecularProfileID: c.MolecularProfileID})
			}
		}
	}
	return out
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

	// cna holds every discrete level; enrichments count CNA events only.
	altFilter := " AND alteration IN ('-2', '2')"
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
				p = stats.JavaFisherTwoTailed(a, b, c, d)
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
