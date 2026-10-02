package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// The study-view gene tables (mutated, CNA and structural-variant genes) count
// genomic_event_derived over the filtered samples, honouring the alteration
// filter, and add each gene's profiled-sample count: samples on a panel that
// covers the gene (first profile of the type per study) plus all whole-exome
// profiled samples. Ported from the column-store ClickhouseAlterationMapper.

type GeneAlterationCount struct {
	EntrezGeneID                *int64   `json:"entrezGeneId"`
	HugoGeneSymbol              string   `json:"hugoGeneSymbol"`
	Alteration                  *int64   `json:"alteration,omitempty"`
	Cytoband                    string   `json:"cytoband,omitempty"`
	NumberOfAlteredCases        uint64   `json:"numberOfAlteredCases"`
	NumberOfAlteredCasesOnPanel uint64   `json:"numberOfAlteredCasesOnPanel"`
	NumberOfProfiledCases       uint64   `json:"numberOfProfiledCases"`
	TotalCount                  uint64   `json:"totalCount"`
	MatchingGenePanelIDs        []string `json:"matchingGenePanelIds"`
}

// alterationSQL renders the alteration filter (AlterationFilterHelper semantics)
// as predicates over genomic_event_derived for the given event kind.
type alterationSQL struct{ af alterationFilter }

func (a alterationSQL) driverClause() string {
	af := a.af
	d, v, u := inc(af.IncludeDriver), inc(af.IncludeVUS), inc(af.IncludeUnknownOncogenicity)
	var out []string
	switch {
	case d && v && u:
	case !d && !v && !u:
		out = append(out, "driver_filter = 'NA'")
	default:
		var ors []string
		if d {
			ors = append(ors, "lower(driver_filter) = 'putative_driver'")
		}
		if v {
			ors = append(ors, "lower(driver_filter) = 'putative_passenger'")
		}
		if u {
			ors = append(ors, "driver_filter IS NULL OR lower(driver_filter) IN ('unknown', 'na', '')")
		}
		out = append(out, "("+strings.Join(ors, " OR ")+")")
	}
	var tiers []string
	allTiers := true
	for _, k := range sortedKeys(af.TiersBooleanMap) {
		if af.TiersBooleanMap[k] {
			tiers = append(tiers, k)
		} else {
			allTiers = false
		}
	}
	// An absent, empty or all-true tier map selects all tiers.
	unknownTier := inc(af.IncludeUnknownTier)
	switch {
	case allTiers && unknownTier:
	case len(tiers) == 0 && !unknownTier:
		out = append(out, "NULL")
	default:
		var ors []string
		if len(tiers) > 0 {
			ors = append(ors, fmt.Sprintf("driver_tiers_filter IN (%s)", inList(tiers)))
		}
		if unknownTier {
			ors = append(ors, "driver_tiers_filter IS NULL OR lower(driver_tiers_filter) IN ('', 'na', 'unknown')")
		}
		out = append(out, "("+strings.Join(ors, " OR ")+")")
	}
	return strings.Join(out, " AND ")
}

func (a alterationSQL) statusClause() string {
	af := a.af
	g, s, u := inc(af.IncludeGermline), inc(af.IncludeSomatic), inc(af.IncludeUnknownStatus)
	switch {
	case g && s && u:
		return ""
	case !g && !s && !u:
		return "mutation_status = 'NA'"
	}
	var ors []string
	if g {
		ors = append(ors, "lower(mutation_status) LIKE '%germline%'")
	}
	if s {
		ors = append(ors, "lower(mutation_status) = 'somatic'")
	}
	if u {
		ors = append(ors, "(lower(mutation_status) != 'somatic' AND lower(mutation_status) NOT LIKE '%germline%')")
	}
	return "(" + strings.Join(ors, " OR ") + ")"
}

// mutationTypeClause applies mutationEventTypes: "any" or all-true selects all;
// with "other" selected the unselected types are excluded, otherwise only the
// selected ones are kept.
func (a alterationSQL) mutationTypeClause() string {
	m := a.af.MutationEventTypes
	if len(m) == 0 || m["any"] {
		return ""
	}
	all := true
	var on, off []string
	for _, k := range sortedKeys(m) {
		if k == "any" {
			continue
		}
		if m[k] {
			on = append(on, strings.ToLower(k))
		} else {
			all = false
			off = append(off, strings.ToLower(k))
		}
	}
	if all {
		return ""
	}
	if m["other"] {
		return fmt.Sprintf("lower(mutation_type) NOT IN (%s)", inList(off))
	}
	if len(on) == 0 {
		return "mutation_type = 'NA'"
	}
	return fmt.Sprintf("lower(mutation_type) IN (%s, 'na')", inList(on))
}

func (a alterationSQL) cnaTypeClause() string {
	m := a.af.CopyNumberAlterationEventTypes
	var codes []string
	all := true
	for _, k := range sortedKeys(m) {
		if m[k] {
			if c, ok := cnaCodes[k]; ok {
				codes = append(codes, c)
			}
		} else {
			all = false
		}
	}
	switch {
	case all:
		return ""
	case len(codes) == 0:
		return "cna_alteration IS NULL"
	}
	return fmt.Sprintf("(cna_alteration IN (%s) OR cna_alteration IS NULL)", strings.Join(codes, ", "))
}

func joinPreds(preds ...string) string {
	var out []string
	for _, p := range preds {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return "1 = 1"
	}
	return strings.Join(out, " AND ")
}

func (s *State) alterationFilterOf(f studyViewFilter) alterationSQL {
	if f.AlterationFilter == nil {
		return alterationSQL{}
	}
	return alterationSQL{*f.AlterationFilter}
}

// geneCounts runs the per-gene event count for one variant type and attaches
// profiled counts and matching panels.
func (s *State) geneCounts(f studyViewFilter, variantType, alterationType, extra string, cna bool) ([]GeneAlterationCount, error) {
	filtered := s.filterSQL(f)
	groupCols := "hugo_gene_symbol, entrez_gene_id"
	selectCNA := ""
	if cna {
		groupCols += ", cna_alteration, cna_cytoband"
		selectCNA = "toInt64(cna_alteration) AS alteration, ifNull(cna_cytoband, '') AS cytoband, "
	}
	type row struct {
		Hugo       string `json:"hugo"`
		Entrez     *int64 `json:"entrez"`
		Alteration *int64 `json:"alteration"`
		Cytoband   string `json:"cytoband"`
		Altered    uint64 `json:"altered"`
		OnPanel    uint64 `json:"onPanel"`
		Total      uint64 `json:"total"`
	}
	rows, err := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT hugo_gene_symbol AS hugo, toInt64OrNull(toString(entrez_gene_id)) AS entrez, %s"+
			"uniqExact(sample_unique_id) AS altered, uniqExactIf(sample_unique_id, off_panel = 0) AS onPanel, count() AS total "+
			"FROM genomic_event_derived WHERE variant_type = '%s' AND sample_unique_id IN (%s) AND %s "+
			"GROUP BY %s ORDER BY total DESC, hugo ASC",
		selectCNA, variantType, filtered, extra, groupCols))
	if err != nil {
		return nil, err
	}
	// Merge rows that share a symbol (and, for CNA, the alteration).
	var out []GeneAlterationCount
	index := map[string]int{}
	for _, r := range rows {
		key := r.Hugo
		if cna && r.Alteration != nil {
			key += fmt.Sprintf("\x00%d", *r.Alteration)
		}
		if i, ok := index[key]; ok {
			out[i].NumberOfAlteredCases += r.Altered
			out[i].NumberOfAlteredCasesOnPanel += r.OnPanel
			out[i].TotalCount += r.Total
			continue
		}
		index[key] = len(out)
		out = append(out, GeneAlterationCount{
			EntrezGeneID: r.Entrez, HugoGeneSymbol: r.Hugo, Alteration: r.Alteration, Cytoband: r.Cytoband,
			NumberOfAlteredCases: r.Altered, NumberOfAlteredCasesOnPanel: r.OnPanel, TotalCount: r.Total,
		})
	}
	if len(out) == 0 {
		return []GeneAlterationCount{}, nil
	}
	s.populateProfiled(f, filtered, alterationType, out)
	return out, nil
}

func (s *State) populateProfiled(f studyViewFilter, filtered, alterationType string, counts []GeneAlterationCount) {
	// First non-continuous profile of the type per study.
	type prof struct {
		ID    string `json:"id"`
		Study string `json:"study"`
	}
	profs, _ := ch.Query[prof](s.CH, fmt.Sprintf(
		"SELECT molecular_profile_id AS id, study_id AS study FROM molecular_profile "+
			"WHERE molecular_alteration_type = '%s' AND datatype NOT IN ('CONTINUOUS', 'LOG2-VALUE') AND study_id IN (%s) "+
			"ORDER BY study_id, molecular_profile_id",
		alterationType, orEmpty(f.studyIDs())))
	first := map[string]string{}
	for _, p := range profs {
		if _, ok := first[p.Study]; !ok {
			first[p.Study] = p.ID
		}
	}
	var profileIDs []string
	for _, st := range sortedKeys(first) {
		profileIDs = append(profileIDs, first[st])
	}
	type geneCount struct {
		Gene string `json:"gene"`
		N    uint64 `json:"n"`
	}
	panelCounts, _ := ch.Query[geneCount](s.CH, fmt.Sprintf(
		"SELECT gptg.gene AS gene, sum(p.n) AS n FROM ("+
			"SELECT gene_panel_id, count() AS n FROM sample_to_gene_panel_derived "+
			"WHERE alteration_type = '%s' AND gene_panel_id != 'WES' AND genetic_profile_id IN (%s) "+
			"AND sample_unique_id IN (%s) GROUP BY gene_panel_id) p "+
			"INNER JOIN gene_panel_to_gene_derived gptg ON p.gene_panel_id = gptg.gene_panel_id GROUP BY gptg.gene",
		alterationType, orEmpty(profileIDs), filtered))
	profiled := map[string]uint64{}
	for _, c := range panelCounts {
		profiled[c.Gene] = c.N
	}
	type n struct {
		N uint64 `json:"n"`
	}
	wes, _ := ch.Query[n](s.CH, fmt.Sprintf(
		"SELECT uniqExact(sample_unique_id) AS n FROM sample_to_gene_panel_derived "+
			"WHERE alteration_type = '%s' AND gene_panel_id = 'WES' AND sample_unique_id IN (%s)",
		alterationType, filtered))
	var wesCount uint64
	if len(wes) > 0 {
		wesCount = wes[0].N
	}
	type panel struct {
		Gene  string `json:"gene"`
		Panel string `json:"panel"`
	}
	panels, _ := ch.Query[panel](s.CH, fmt.Sprintf(
		"SELECT gene, gene_panel_id AS panel FROM gene_panel_to_gene_derived WHERE gene_panel_id IN "+
			"(SELECT DISTINCT gene_panel_id FROM sample_to_gene_panel_derived WHERE alteration_type = '%s' AND sample_unique_id IN (%s)) "+
			"AND gene IN (%s) GROUP BY gene, gene_panel_id",
		alterationType, filtered, inList(func() []string {
			g := make([]string, len(counts))
			for i, c := range counts {
				g[i] = c.HugoGeneSymbol
			}
			return g
		}())))
	matching := map[string][]string{}
	for _, p := range panels {
		matching[p.Gene] = append(matching[p.Gene], p.Panel)
	}
	for i := range counts {
		g := counts[i].HugoGeneSymbol
		counts[i].NumberOfProfiledCases = profiled[g] + wesCount
		m := matching[g]
		sort.Strings(m)
		if m == nil {
			m = []string{}
		}
		counts[i].MatchingGenePanelIDs = m
	}
}

func (s *State) mutatedGenesFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	a := s.alterationFilterOf(f)
	out, err := s.geneCounts(f, "mutation", "MUTATION_EXTENDED",
		joinPreds("mutation_status != 'UNCALLED'", a.mutationTypeClause(), a.driverClause(), a.statusClause()), false)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *State) cnaGenesFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	a := s.alterationFilterOf(f)
	out, err := s.geneCounts(f, "cna", "COPY_NUMBER_ALTERATION", joinPreds(a.cnaTypeClause(), a.driverClause()), true)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *State) svGenesFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	a := s.alterationFilterOf(f)
	out, err := s.geneCounts(f, "structural_variant", "STRUCTURAL_VARIANT", joinPreds(a.driverClause(), a.statusClause()), false)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// molecularProfileSampleCountsFetch counts the filtered samples in each
// molecular profile, keyed by the profile's stable-id suffix and labelled with
// its name.
func (s *State) molecularProfileSampleCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	type row struct {
		Value string `json:"value"`
		Label string `json:"label"`
		Count uint64 `json:"count"`
	}
	rows, err := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT replaceOne(sp.molecular_profile_id, concat(sp.study_id, '_'), '') AS value, any(mp.name) AS label, count() AS count "+
			"FROM sample_profile sp LEFT JOIN molecular_profile mp ON mp.molecular_profile_id = sp.molecular_profile_id "+
			"WHERE concat(sp.study_id, '_', sp.sample_id) IN (%s) GROUP BY sp.molecular_profile_id, sp.study_id",
		s.filterSQL(f)))
	if err != nil {
		fail(w, err)
		return
	}
	// Merge the same profile suffix across studies.
	byValue := map[string]*GenomicDataCount{}
	for _, r := range rows {
		if c, ok := byValue[r.Value]; ok {
			c.Count += r.Count
			c.UniqueCount += r.Count
			continue
		}
		byValue[r.Value] = &GenomicDataCount{Value: r.Value, Label: r.Label, Count: r.Count, UniqueCount: r.Count}
	}
	out := []GenomicDataCount{}
	for _, k := range sortedKeys(byValue) {
		out = append(out, *byValue[k])
	}
	writeJSON(w, out)
}

// enrichmentClause is the alteration filter over mutation, CNA and SV events
// together (getAlterationCountByGeneGiven): mutation-type, CNA-type and
// driver/tier/status predicates leave rows of the other kinds untouched.
func (a alterationSQL) enrichmentClause() string {
	var preds []string
	if mt := a.mutationTypeClause(); mt != "" {
		preds = append(preds, "(variant_type != 'mutation' OR "+mt+")")
	}
	if ct := a.cnaTypeClause(); ct != "" {
		preds = append(preds, ct)
	}
	if st := a.statusClause(); st != "" {
		preds = append(preds, "(variant_type = 'cna' OR "+st+")")
	}
	if d := a.driverClause(); d != "" {
		preds = append(preds, d)
	}
	if a.af.StructuralVariants != nil && !*a.af.StructuralVariants {
		preds = append(preds, "variant_type != 'structural_variant'")
	}
	return joinPreds(preds...)
}

// StructuralVariantCount is one gene1::gene2 pair of the study-view
// structural-variant table (AlterationCountByStructuralVariant).
type StructuralVariantCount struct {
	Gene1EntrezGeneID     *int64   `json:"gene1EntrezGeneId"`
	Gene1HugoGeneSymbol   string   `json:"gene1HugoGeneSymbol"`
	Gene2EntrezGeneID     *int64   `json:"gene2EntrezGeneId"`
	Gene2HugoGeneSymbol   string   `json:"gene2HugoGeneSymbol"`
	NumberOfAlteredCases  uint64   `json:"numberOfAlteredCases"`
	NumberOfProfiledCases uint64   `json:"numberOfProfiledCases"`
	TotalCount            uint64   `json:"totalCount"`
	MatchingGenePanelIDs  []string `json:"matchingGenePanelIds"`
}

// svCountsFetch serves POST /api/structuralvariant-counts/fetch: structural
// variants of the filtered samples counted per (gene1, gene2) pair under the
// alteration filter's driver and status settings. A pair's profiled count is
// the samples on panels covering either gene plus the whole-exome samples.
func (s *State) svCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	a := s.alterationFilterOf(f)
	filtered := s.filterSQL(f)
	type row struct {
		Entrez1 *int64 `json:"entrez1"`
		Hugo1   string `json:"hugo1"`
		Entrez2 *int64 `json:"entrez2"`
		Hugo2   string `json:"hugo2"`
		Altered uint64 `json:"altered"`
		Total   uint64 `json:"total"`
	}
	rows, err := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT site1_entrez_gene_id AS entrez1, any(site1_hugo_symbol) AS hugo1, site2_entrez_gene_id AS entrez2, "+
			"any(site2_hugo_symbol) AS hugo2, uniqExact(study_id, sample_id) AS altered, count() AS total FROM ("+
			"SELECT *, cbp_driver AS driver_filter, cbp_driver_tiers AS driver_tiers_filter, sv_status AS mutation_status "+
			"FROM ("+svResolvedSQL+")) WHERE concat(study_id, '_', sample_id) IN (%s) AND %s "+
			"GROUP BY site1_entrez_gene_id, site2_entrez_gene_id ORDER BY total DESC, hugo1, hugo2",
		filtered, joinPreds(a.driverClause(), a.statusClause())))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]StructuralVariantCount, 0, len(rows))
	if len(rows) == 0 {
		writeJSON(w, out)
		return
	}
	type prof struct {
		ID string `json:"id"`
	}
	profs, _ := ch.Query[prof](s.CH, fmt.Sprintf(
		"SELECT min(molecular_profile_id) AS id FROM molecular_profile WHERE molecular_alteration_type = 'STRUCTURAL_VARIANT' "+
			"AND study_id IN (%s) GROUP BY study_id", orEmpty(f.studyIDs())))
	profileIDs := make([]string, len(profs))
	for i, p := range profs {
		profileIDs[i] = p.ID
	}
	type panelCount struct {
		Panel string `json:"panel"`
		N     uint64 `json:"n"`
	}
	panelCounts, _ := ch.Query[panelCount](s.CH, fmt.Sprintf(
		"SELECT gene_panel_id AS panel, count() AS n FROM sample_to_gene_panel_derived "+
			"WHERE alteration_type = 'STRUCTURAL_VARIANT' AND gene_panel_id != 'WES' AND genetic_profile_id IN (%s) "+
			"AND sample_unique_id IN (%s) GROUP BY gene_panel_id", orEmpty(profileIDs), filtered))
	perPanel := map[string]uint64{}
	var panelIDs []string
	for _, p := range panelCounts {
		perPanel[p.Panel] = p.N
		panelIDs = append(panelIDs, p.Panel)
	}
	type n struct {
		N uint64 `json:"n"`
	}
	var wesCount uint64
	if wes, _ := ch.Query[n](s.CH, fmt.Sprintf(
		"SELECT uniqExact(sample_unique_id) AS n FROM sample_to_gene_panel_derived "+
			"WHERE alteration_type = 'STRUCTURAL_VARIANT' AND gene_panel_id = 'WES' AND sample_unique_id IN (%s)",
		filtered)); len(wes) > 0 {
		wesCount = wes[0].N
	}
	genes := map[string]bool{}
	for _, r := range rows {
		genes[r.Hugo1], genes[r.Hugo2] = true, true
	}
	type panelGene struct {
		Gene  string `json:"gene"`
		Panel string `json:"panel"`
	}
	panelGenes, _ := ch.Query[panelGene](s.CH, fmt.Sprintf(
		"SELECT DISTINCT gene, gene_panel_id AS panel FROM gene_panel_to_gene_derived WHERE gene_panel_id IN (%s) AND gene IN (%s)",
		orEmpty(panelIDs), orEmpty(sortedKeys(genes))))
	panelsOf := map[string][]string{}
	for _, pg := range panelGenes {
		panelsOf[pg.Gene] = append(panelsOf[pg.Gene], pg.Panel)
	}
	for _, r := range rows {
		panels := map[string]bool{}
		for _, p := range append(panelsOf[r.Hugo1], panelsOf[r.Hugo2]...) {
			panels[p] = true
		}
		matching := sortedKeys(panels)
		profiled := wesCount
		for _, p := range matching {
			profiled += perPanel[p]
		}
		out = append(out, StructuralVariantCount{
			Gene1EntrezGeneID: r.Entrez1, Gene1HugoGeneSymbol: r.Hugo1, Gene2EntrezGeneID: r.Entrez2, Gene2HugoGeneSymbol: r.Hugo2,
			NumberOfAlteredCases: r.Altered, NumberOfProfiledCases: profiled, TotalCount: r.Total,
			MatchingGenePanelIDs: matching,
		})
	}
	writeJSON(w, out)
}
