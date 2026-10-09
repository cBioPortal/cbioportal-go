package studyview

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// routeStudyView2 registers the remaining study-view aggregation endpoints:
// numeric clinical bins, mutation/genomic data counts, treatments, and
// case-list (sample-list) counts.
func (s Server) routeStudyView2(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/clinical-data-bin-counts/fetch", s.clinicalDataBinCountsFetch)
	mux.HandleFunc("POST /api/mutation-data-counts/fetch", s.mutationDataCountsFetch)
	mux.HandleFunc("POST /api/genomic-data-counts/fetch", s.genomicDataCountsFetch)
	mux.HandleFunc("POST /api/genomic-data-bin-counts/fetch", s.genomicDataBinCountsFetch)
	mux.HandleFunc("POST /api/sample-lists-counts/fetch", s.sampleListsCountsFetch)
}

// ---------- Clinical data bin counts ----------

// ---------- Mutation data counts ----------

// GenomicDataCountItem groups the per-category counts for one gene/profile.
type GenomicDataCountItem struct {
	HugoGeneSymbol string             `json:"hugoGeneSymbol"`
	ProfileType    string             `json:"profileType"`
	Counts         []GenomicDataCount `json:"counts"`
}

type genomicDataCountFilter struct {
	GenomicDataFilters []genomicDataFilter `json:"genomicDataFilters"`
	StudyViewFilter    studyViewFilter     `json:"studyViewFilter"`
}

// mutationDataCountsFetch serves the per-gene mutation charts: the SUMMARY
// projection gives the Mutated / Not Mutated / Not Profiled pie, DETAILED the
// per-mutation-type table.
func (s Server) mutationDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f genomicDataCountFilter
	core.ReadBody(r, &f)
	// Only SUMMARY counts mutated / not mutated / not profiled samples; the
	// other projections count by mutation type.
	summary := core.ProjectionOf(r) == "SUMMARY"
	includeSampleIDs := r.URL.Query().Get("includeSampleIds") == "true"
	svf := f.StudyViewFilter
	if len(f.GenomicDataFilters) == 1 && summary {
		g := f.GenomicDataFilters[0]
		svf = svf.withoutMutation(g.HugoGeneSymbol, g.ProfileType, "MUTATED")
	}
	args := core.NewArgs()
	filtered := s.filterSQL(args, svf)
	out := []GenomicDataCountItem{}
	if summary {
		if len(f.GenomicDataFilters) == 0 {
			core.WriteJSON(w, out)
			return
		}
		total := s.scopeSampleCount(svf)
		genes := make([]string, len(f.GenomicDataFilters))
		for i, g := range f.GenomicDataFilters {
			genes[i] = g.HugoGeneSymbol
		}
		type countsRow struct {
			Gene     string `json:"gene"`
			Mutated  uint64 `json:"mutated"`
			Profiled uint64 `json:"profiled"`
		}
		rows, err := core.QueryA[countsRow](s.CH, fmt.Sprintf(
			"WITH scope AS (%[1]s) SELECT gene, sum(m) AS mutated, sum(p) AS profiled FROM ("+
				"SELECT hugo_gene_symbol AS gene, uniqExact(sample_unique_id) AS m, toUInt64(0) AS p FROM %[3]s "+
				"AND sample_unique_id IN (SELECT sample_unique_id FROM scope) AND hugo_gene_symbol IN %[2]s "+
				"AND variant_type = 'mutation' GROUP BY gene "+
				"UNION ALL SELECT gpg.gene AS gene, toUInt64(0) AS m, uniqExact(sgp.sample_unique_id) AS p FROM %[4]s sgp "+
				"JOIN gene_panel_to_gene_derived gpg ON sgp.gene_panel_id = gpg.gene_panel_id "+
				"WHERE sgp.sample_unique_id IN (SELECT sample_unique_id FROM scope) AND gpg.gene IN %[2]s "+
				"AND sgp.alteration_type = 'MUTATION_EXTENDED' GROUP BY gene"+
				") GROUP BY gene",
			filtered, args.Strs(genes), svf.scope().From(args, core.TGenomicEventDerived), svf.scope().Rows(args, core.TSampleToGenePanelDerived)), args)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		byGene := make(map[string]countsRow, len(rows))
		for _, r := range rows {
			byGene[r.Gene] = r
		}
		for _, g := range f.GenomicDataFilters {
			mutated, profiled := byGene[g.HugoGeneSymbol].Mutated, byGene[g.HugoGeneSymbol].Profiled
			item := GenomicDataCountItem{HugoGeneSymbol: g.HugoGeneSymbol, ProfileType: "mutations", Counts: []GenomicDataCount{}}
			add := func(label, value string, n uint64) {
				if n > 0 {
					item.Counts = append(item.Counts, GenomicDataCount{Label: label, Value: value, Count: n, UniqueCount: &n})
				}
			}
			add("Mutated", "MUTATED", mutated)
			if profiled > mutated {
				add("Not Mutated", "NOT_MUTATED", profiled-mutated)
			}
			if total > profiled {
				add("Not Profiled", "NOT_PROFILED", total-profiled)
			}
			out = append(out, item)
		}
		core.WriteJSON(w, out)
		return
	}
	if len(f.GenomicDataFilters) == 0 && includeSampleIDs {
		core.WriteJSON(w, out)
		return
	}
	genes := make([]string, len(f.GenomicDataFilters))
	for i, g := range f.GenomicDataFilters {
		genes[i] = g.HugoGeneSymbol
	}
	if includeSampleIDs {
		genes = genes[:1]
	}
	// Without genomicDataFilters the mapper's gene condition is empty
	// (getMutationCountsByType's foreach), so every mutated gene is counted.
	geneCond := ""
	if len(genes) > 0 {
		geneCond = "AND hugo_gene_symbol IN " + args.Strs(genes) + " "
	}
	type typeRow struct {
		Gene   string `json:"gene"`
		Type   string `json:"type"`
		C      uint64 `json:"c"`
		U      uint64 `json:"u"`
		Sample string `json:"samples"`
	}
	rows, _ := core.QueryA[typeRow](s.CH, fmt.Sprintf(
		"SELECT hugo_gene_symbol AS gene, mutation_type AS type, count() AS c, uniqExact(sample_unique_id) AS u, "+
			"arrayStringConcat(groupUniqArray(sample_unique_id), ',') AS samples FROM %s "+
			"AND sample_unique_id IN (%s) AND variant_type = 'mutation' %s"+
			"GROUP BY hugo_gene_symbol, mutation_type ORDER BY hugo_gene_symbol, mutation_type",
		svf.scope().From(args, core.TGenomicEventDerived), filtered, geneCond), args)
	byGene := map[string]*GenomicDataCountItem{}
	var order []string
	for _, r := range rows {
		it, ok := byGene[r.Gene]
		if !ok {
			it = &GenomicDataCountItem{HugoGeneSymbol: r.Gene, ProfileType: "mutations", Counts: []GenomicDataCount{}}
			byGene[r.Gene] = it
			order = append(order, r.Gene)
		}
		c := GenomicDataCount{Label: strings.ReplaceAll(r.Type, "_", " "), Value: r.Type, Count: r.C, UniqueCount: &r.U}
		if includeSampleIDs {
			c.SampleIDs = strings.Split(r.Sample, ",")
		}
		it.Counts = append(it.Counts, c)
	}
	for _, g := range order {
		out = append(out, *byGene[g])
	}
	if includeSampleIDs {
		gene := args.Str(genes[0])
		type idsRow struct {
			N   uint64 `json:"n"`
			IDs string `json:"ids"`
		}
		profiled := fmt.Sprintf(
			"SELECT DISTINCT sgp.sample_unique_id FROM %s sgp "+
				"JOIN gene_panel_to_gene_derived gpg ON sgp.gene_panel_id = gpg.gene_panel_id "+
				"WHERE sgp.sample_unique_id IN (%s) AND gpg.gene = %s AND sgp.alteration_type = 'MUTATION_EXTENDED'",
			svf.scope().Rows(args, core.TSampleToGenePanelDerived), filtered, gene)
		mutated := fmt.Sprintf(
			"SELECT sample_unique_id FROM %s AND sample_unique_id IN (%s) "+
				"AND variant_type = 'mutation' AND hugo_gene_symbol = %s", svf.scope().From(args, core.TGenomicEventDerived), filtered, gene)
		extra := []struct{ label, value, sql string }{
			{"Not Mutated", "NOT_MUTATED", fmt.Sprintf("SELECT count() AS n, arrayStringConcat(groupUniqArray(sample_unique_id), ',') AS ids FROM (%s) WHERE sample_unique_id NOT IN (%s)", profiled, mutated)},
			{"Not Profiled", "NOT_PROFILED", fmt.Sprintf("SELECT count() AS n, arrayStringConcat(groupUniqArray(sample_unique_id), ',') AS ids FROM %s AND sample_unique_id NOT IN (%s)", core.ByStudies(svf.studyIDs()).From(args, core.TSampleDerived), profiled)},
		}
		// The mapper's result map groups rows by gene and profile type: the
		// not-mutated and not-profiled rows (always present, counts possibly
		// 0) are items of their own, with the label as profile type.
		for _, e := range extra {
			r, err := core.QueryA[idsRow](s.CH, e.sql, args)
			if err != nil || len(r) == 0 {
				continue
			}
			n := r[0].N
			c := GenomicDataCount{Label: e.label, Value: e.value, Count: n, UniqueCount: &n}
			if r[0].IDs != "" {
				c.SampleIDs = strings.Split(r[0].IDs, ",")
			}
			out = append(out, GenomicDataCountItem{HugoGeneSymbol: genes[0], ProfileType: e.label, Counts: []GenomicDataCount{c}})
		}
	}
	core.WriteJSON(w, out)
}

// cnaLabels names the discrete copy-number values.
var cnaLabels = map[string]string{
	"2": "Amplified", "1": "Gained", "0": "Diploid", "-1": "Heterozygously deleted", "-2": "Homozygously deleted",
}

// genomicDataCountsFetch counts discrete CNA values per gene over the filtered
// samples (the endpoint is CNA-specific), with the remainder reported as NA.
func (s Server) genomicDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f genomicDataCountFilter
	core.ReadBody(r, &f)
	out := []GenomicDataCountItem{}
	if len(f.GenomicDataFilters) == 0 {
		core.WriteJSON(w, out)
		return
	}
	svf := f.StudyViewFilter
	if len(f.GenomicDataFilters) == 1 {
		g := f.GenomicDataFilters[0]
		svf = svf.withoutGenomic(g.HugoGeneSymbol, g.ProfileType)
	}
	profileType := f.GenomicDataFilters[0].ProfileType
	genes := make([]string, len(f.GenomicDataFilters))
	for i, g := range f.GenomicDataFilters {
		genes[i] = g.HugoGeneSymbol
	}
	type row struct {
		Gene  string `json:"gene"`
		Value string `json:"value"`
		C     uint64 `json:"c"`
	}
	args := core.NewArgs()
	rows, _ := core.QueryA[row](s.CH, fmt.Sprintf(
		"SELECT hugo_gene_symbol AS gene, toString(alteration_value) AS value, count() AS c FROM %s "+
			"AND NOT %s AND profile_type = %s AND sample_unique_id IN (%s) AND hugo_gene_symbol IN %s "+
			"GROUP BY hugo_gene_symbol, alteration_value ORDER BY hugo_gene_symbol, alteration_value",
		svf.scope().From(args, core.TGeneticAlterationDerived), isNASQL("assumeNotNull(alteration_value)"), args.Str(profileType),
		s.filterSQL(args, svf), args.Strs(genes)), args)
	total := s.scopeSampleCount(svf)
	byGene := map[string]*GenomicDataCountItem{}
	var order []string
	// As in ClickhouseGenomicDataMapper.getCNACounts, a single NA count is
	// reported, for the first gene: the filtered samples minus that gene's
	// non-NA counts.
	sum := map[string]uint64{}
	for _, r := range rows {
		it, ok := byGene[r.Gene]
		if !ok {
			it = &GenomicDataCountItem{HugoGeneSymbol: r.Gene, ProfileType: profileType, Counts: []GenomicDataCount{}}
			byGene[r.Gene] = it
			order = append(order, r.Gene)
		}
		label, ok := cnaLabels[r.Value]
		if !ok {
			label = "NA"
		}
		it.Counts = append(it.Counts, GenomicDataCount{Label: label, Value: r.Value, Count: r.C})
		sum[r.Gene] += r.C
	}
	naGene := genes[0]
	if len(order) > 0 {
		naGene = order[0]
	}
	if _, ok := byGene[naGene]; !ok {
		byGene[naGene] = &GenomicDataCountItem{HugoGeneSymbol: naGene, ProfileType: profileType, Counts: []GenomicDataCount{}}
		order = append(order, naGene)
	}
	var na uint64
	if total > sum[naGene] {
		na = total - sum[naGene]
	}
	byGene[naGene].Counts = append(byGene[naGene].Counts, GenomicDataCount{Label: "NA", Value: "NA", Count: na})
	for _, g := range order {
		out = append(out, *byGene[g])
	}
	core.WriteJSON(w, out)
}

// ---------- Treatments (no treatment data loaded) ----------

// ---------- Sample list (case list) counts ----------

// CaseListDataCount is the count of filtered samples in one case list.
type CaseListDataCount struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Count uint64 `json:"count"`
}

// mergeCaseListCounts is StudyViewColumnarServiceUtil.mergeCaseListCounts:
// the per-study lists of one type (stable id without the study prefix) are
// summed under the first list's label, in the type's HashMap order.
func mergeCaseListCounts(rows []CaseListDataCount) []CaseListDataCount {
	merged := map[string]*CaseListDataCount{}
	var values []string
	for _, r := range rows {
		if c, ok := merged[r.Value]; ok {
			c.Count += r.Count
			continue
		}
		c := r
		merged[r.Value] = &c
		values = append(values, r.Value)
	}
	out := make([]CaseListDataCount, 0, len(values))
	for _, v := range core.JavaHashMapOrder(values) {
		out = append(out, *merged[v])
	}
	return out
}

// sampleListsCountsFetch counts the filtered samples in each case list
// (ClickhousePatientMapper.getCaseListDataCounts), keyed by list type.
func (s Server) sampleListsCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	core.ReadBody(r, &f)
	args := core.NewArgs()
	rows, err := core.QueryA[CaseListDataCount](s.CH, fmt.Sprintf(
		"SELECT replaceAll(list_id, concat(study_id, '_'), '') AS value, any(name) AS label, count() AS count "+
			"FROM %s AND name != '' GROUP BY study_id, list_id ORDER BY study_id, list_id", s.sampleScopeFrom(args, f, core.TSampleList)), args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, mergeCaseListCounts(rows))
}
