package comparison

import (
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/molecular"
	"github.com/cbioportal/cbioportal-go/internal/api/studies"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/stats"
)

// ---------------------------------------------------------------------------
// Response types (camelCase shapes from CBioPortalAPIInternal.ts)
// ---------------------------------------------------------------------------

// GroupStatistics mirrors the client GroupStatistics: per-group mean/stddev of a
// continuous molecular value.
type GroupStatistics struct {
	Name              string  `json:"name"`
	MeanExpression    float64 `json:"meanExpression"`
	StandardDeviation float64 `json:"standardDeviation"`
}

// GenomicEnrichment is the expression-enrichments response element. As in
// ExpressionEnrichmentUtil, neither the cytoband nor a q-value is set (the
// frontend computes q-values itself).
type GenomicEnrichment struct {
	GroupsStatistics []GroupStatistics `json:"groupsStatistics"`
	EntrezGeneID     int64             `json:"entrezGeneId"`
	HugoGeneSymbol   string            `json:"hugoGeneSymbol"`
	PValue           float64           `json:"pValue"`
}

// GenericAssayEnrichment is the generic-assay-enrichments response element
// (Java sets neither name nor qValue on numeric enrichments).
type GenericAssayEnrichment struct {
	GroupsStatistics            []GroupStatistics  `json:"groupsStatistics"`
	StableID                    string             `json:"stableId"`
	GenericEntityMetaProperties core.JavaStringMap `json:"genericEntityMetaProperties"`
	PValue                      float64            `json:"pValue"`
}

// GenericAssayCountSummary mirrors the client type for binary enrichments.
type GenericAssayCountSummary struct {
	Name       string `json:"name"`
	Count      int    `json:"count"`
	TotalCount int    `json:"totalCount"`
}

// GenericAssayBinaryEnrichment is the generic-assay-binary-enrichments element
// (Java leaves name unset, so it is not serialized).
type GenericAssayBinaryEnrichment struct {
	GroupsStatistics            []GroupStatistics          `json:"groupsStatistics"`
	StableID                    string                     `json:"stableId"`
	GenericEntityMetaProperties core.JavaStringMap         `json:"genericEntityMetaProperties"`
	Counts                      []GenericAssayCountSummary `json:"counts"`
	PValue                      float64                    `json:"pValue"`
	QValue                      float64                    `json:"qValue"`
}

// GenericAssayCategoricalEnrichment is the generic-assay-categorical-enrichments
// element (Java leaves name unset, so it is not serialized).
type GenericAssayCategoricalEnrichment struct {
	GroupsStatistics            []CategoricalGroupStatistics `json:"groupsStatistics"`
	StableID                    string                       `json:"stableId"`
	GenericEntityMetaProperties core.JavaStringMap           `json:"genericEntityMetaProperties"`
	PValue                      float64                      `json:"pValue"`
	QValue                      float64                      `json:"qValue"`
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
func (s Server) resolveGroupTriples(g casesGroup, metaCache map[string]core.ProfileMeta) ([][3]string, string) {
	triples := make([][3]string, 0, len(g.MolecularProfileCaseIdentifiers))
	table := ""
	for _, c := range g.MolecularProfileCaseIdentifiers {
		meta, seen := metaCache[c.MolecularProfileID]
		if !seen {
			m, ok := s.ProfileMeta(c.MolecularProfileID)
			if !ok {
				m = core.ProfileMeta{}
			}
			metaCache[c.MolecularProfileID] = m
			meta = m
		}
		if meta.StudyID == "" {
			continue
		}
		if table == "" {
			table = meta.DataTable()
		}
		triples = append(triples, [3]string{meta.StudyID, meta.StableID, c.CaseID})
	}
	return triples, table
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

// contGroup is one group's (study_id, profile_stable_id, sample_id)
// triples and the staging table they are read from; table is empty for a
// group none of whose profiles exist.
type contGroup struct {
	triples [][3]string
	table   string
}

// rnaSeqValueSQL is the value Java compares for a cell x of a profile: for
// rna_seq profiles log2(x+1) with negatives clamped to 0, otherwise x.
func rnaSeqValueSQL(x string, rnaSeq bool) string {
	if rnaSeq {
		return "log2(greatest(" + x + ", 0) + 1)"
	}
	return x
}

// computeContinuous returns, per gene/entity, the per-group mean/stddev
// statistics and a group-comparison p-value as cbioportal's
// ExpressionEnrichmentUtil computes them, in the order Java iterates the
// profile's rows: genes by genetic entity id, generic assay entities in
// storage (file) order. As in Java: values of rna_seq
// profiles are log2(x+1) transformed (negatives clamped to 0), groups with
// fewer than two values are left out, and entities need two remaining groups.
// The p-value is Welch's t-test for two groups and a one-way ANOVA otherwise;
// entities with an undefined p-value are skipped. Only per-group summary
// statistics leave ClickHouse; the variance is the numerically stable
// varSampStable, which agrees with commons-math's two-pass variance where the
// one-pass varSamp loses precision (equal values give exactly 0). keyCol is the matrix column that identifies a
// row (entity_stable_id for generic assay); withGene keys rows on hugo/entrez.
//
// The per-group statistics come from one read of the profile for all
// groups: of production's per-profile genetic_alteration rows where the
// reference database holds them (profileRowStats), else of the staging
// matrices (matrixStats).
func (s Server) computeContinuous(groups []casesGroup, keyCol string, withGene bool) ([]contResult, error) {
	metaCache := map[string]core.ProfileMeta{}
	cg := make([]contGroup, len(groups))
	present := 0
	for gi, g := range groups {
		triples, table := s.resolveGroupTriples(g, metaCache)
		if table == "" || len(triples) == 0 {
			continue
		}
		cg[gi] = contGroup{triples: triples, table: table}
		present++
	}
	if present < 2 {
		return []contResult{}, nil
	}
	args := core.NewArgs()
	agg, ok, err := s.profileRowStats(args, groups, cg, withGene)
	if err != nil {
		return nil, err
	}
	if !ok {
		agg = s.matrixStats(args, cg, keyCol, withGene)
	}
	query := agg + " ORDER BY pos, symbol, grp"
	if withGene {
		query = "SELECT a.symbol AS symbol, a.entrez AS entrez, a.grp AS grp, a.n AS n, a.mean AS mean, a.var AS var FROM (" + agg + ") a " +
			"LEFT JOIN (SELECT entrez_gene_id, genetic_entity_id FROM cbioportal_ref.gene) g ON g.entrez_gene_id = a.entrez " +
			"ORDER BY g.genetic_entity_id, a.symbol, a.grp"
	}
	rows, err := core.QueryA[contStatRow](s.CH, query, args)
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
	for i, n := range core.JavaHashMapOrder(names) {
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
	return out, nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// expressionEnrichmentsFetch computes per-gene expression enrichment across the
// posted groups: per-group mean/stddev and a Kruskal-Wallis p-value.
func (s Server) expressionEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var groups []casesGroup
	core.ReadBody(r, &groups)
	if id, ok := s.enrichmentProfileValid(groups, expressionAlterationTypes); !ok {
		core.WriteError(w, http.StatusNotFound, core.MsgMolecularProfileNotFound(id))
		return
	}
	if strings.EqualFold(r.URL.Query().Get("enrichmentType"), "PATIENT") {
		groups = s.patientCasesToSamples(groups)
	}
	if len(groups) < 2 {
		core.WriteJSON(w, []GenomicEnrichment{})
		return
	}
	results, err := s.computeContinuous(groups, "hugo_symbol", true)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]GenomicEnrichment, len(results))
	for i, res := range results {
		out[i] = GenomicEnrichment{
			EntrezGeneID:     res.entrez,
			GroupsStatistics: res.groupsStatistics,
			HugoGeneSymbol:   res.hugo,
			PValue:           res.pValue,
		}
	}
	core.WriteJSON(w, out)
}

// genericAssayEnrichmentsFetch computes per-entity continuous enrichment over the
// generic-assay matrix (keyed by entity_stable_id).
func (s Server) genericAssayEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var groups []casesGroup
	core.ReadBody(r, &groups)
	if id, ok := s.enrichmentProfileValid(groups, map[string]bool{"GENERIC_ASSAY": true}); !ok {
		core.WriteError(w, http.StatusNotFound, core.MsgMolecularProfileNotFound(id))
		return
	}
	if strings.EqualFold(r.URL.Query().Get("enrichmentType"), "PATIENT") {
		groups = s.patientCasesToSamples(groups)
	}
	if len(groups) < 2 {
		core.WriteJSON(w, []GenericAssayEnrichment{})
		return
	}
	results, err := s.computeContinuous(groups, "entity_stable_id", false)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	props := map[string]core.JavaStringMap{}
	if pairs := s.groupProfilePairs(groups); len(pairs) > 0 {
		args := core.NewArgs()
		props, _ = s.GenericAssayMetaProperties("(study_id, profile_stable_id) IN "+args.Pairs(pairs), args)
		props = molecular.SummaryMetaProperties(props)
	}
	out := make([]GenericAssayEnrichment, len(results))
	for i, res := range results {
		out[i] = GenericAssayEnrichment{
			GenericEntityMetaProperties: props[res.stable],
			GroupsStatistics:            res.groupsStatistics,
			PValue:                      res.pValue,
			StableID:                    res.stable,
		}
	}
	core.WriteJSON(w, out)
}

// expressionAlterationTypes are the profile types ExpressionEnrichmentService
// computes genomic enrichments for.
var expressionAlterationTypes = map[string]bool{
	"MICRO_RNA_EXPRESSION": true, "MRNA_EXPRESSION": true, "MRNA_EXPRESSION_NORMALS": true,
	"RNA_EXPRESSION": true, "METHYLATION": true, "METHYLATION_BINARY": true, "PHOSPHORYLATION": true,
	"PROTEIN_LEVEL": true, "PROTEIN_ARRAY_PROTEIN_LEVEL": true, "PROTEIN_ARRAY_PHOSPHORYLATION": true,
}

// enrichmentProfileValid checks the groups' molecular profile as Java's
// validateMolecularProfile does: an unknown profile or one of another
// alteration type is reported (as not found) with its id.
func (s Server) enrichmentProfileValid(groups []casesGroup, types map[string]bool) (string, bool) {
	for _, g := range groups {
		for _, c := range g.MolecularProfileCaseIdentifiers {
			m, ok := s.ProfileMeta(c.MolecularProfileID)
			if !ok || !types[strings.ToUpper(m.AlterationType)] {
				return c.MolecularProfileID, false
			}
			return "", true
		}
	}
	return "", true
}

// groupProfilePairs lists the (study, profile stable id) of every molecular
// profile the groups reference.
func (s Server) groupProfilePairs(groups []casesGroup) [][2]string {
	seen := map[string]bool{}
	var pairs [][2]string
	for _, g := range groups {
		for _, c := range g.MolecularProfileCaseIdentifiers {
			if seen[c.MolecularProfileID] {
				continue
			}
			seen[c.MolecularProfileID] = true
			if m, ok := s.ProfileMeta(c.MolecularProfileID); ok {
				pairs = append(pairs, [2]string{m.StudyID, m.StableID})
			}
		}
	}
	return pairs
}

// patientCasesToSamples rewrites groups whose case ids are patient ids
// (enrichmentType=PATIENT) to the samples of those patients, as the Java
// backend compares every sample of a patient.
func (s Server) patientCasesToSamples(groups []casesGroup) []casesGroup {
	metaCache := map[string]core.ProfileMeta{}
	var pairs [][2]string
	for _, g := range groups {
		for _, c := range g.MolecularProfileCaseIdentifiers {
			m, seen := metaCache[c.MolecularProfileID]
			if !seen {
				m, _ = s.ProfileMeta(c.MolecularProfileID)
				metaCache[c.MolecularProfileID] = m
			}
			pairs = append(pairs, [2]string{m.StudyID, c.CaseID})
		}
	}
	samplesOf := map[string][]string{}
	if len(pairs) > 0 {
		p := ch.Params{"ids": pairs}
		rows, _ := ch.QueryP[studies.IDRow](s.CH,
			"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM "+core.ByStudies(core.PairStudies(pairs)).From(core.ParamsBinder(p), core.TSample)+
				" AND (study_id, patient_id) IN {ids:Array(Tuple(String, String))} ORDER BY study_id, sample_id", p)
		for _, r := range rows {
			k := molecular.JoinKey(r.StudyID, r.PatientID)
			samplesOf[k] = append(samplesOf[k], r.SampleID)
		}
	}
	out := make([]casesGroup, len(groups))
	for i, g := range groups {
		out[i] = casesGroup{Name: g.Name}
		for _, c := range g.MolecularProfileCaseIdentifiers {
			for _, sid := range samplesOf[molecular.JoinKey(metaCache[c.MolecularProfileID].StudyID, c.CaseID)] {
				out[i].MolecularProfileCaseIdentifiers = append(out[i].MolecularProfileCaseIdentifiers,
					caseIdentifier{CaseID: sid, MolecularProfileID: c.MolecularProfileID})
			}
		}
	}
	return out
}

func (s Server) routeEnrichment2(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/expression-enrichments/fetch", s.Bulk().expressionEnrichmentsFetch)
	mux.HandleFunc("POST /api/generic-assay-enrichments/fetch", s.Bulk().genericAssayEnrichmentsFetch)
	mux.HandleFunc("POST /api/generic-assay-binary-enrichments/fetch", s.Bulk().genericAssayBinaryEnrichmentsFetch)
	mux.HandleFunc("POST /api/generic-assay-categorical-enrichments/fetch", s.Bulk().genericAssayCategoricalEnrichmentsFetch)
}
