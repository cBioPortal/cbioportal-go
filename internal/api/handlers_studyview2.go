package api

import (
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeStudyView2 registers the remaining study-view aggregation endpoints:
// numeric clinical bins, mutation/genomic data counts, treatments, and
// case-list (sample-list) counts.
func (s *State) routeStudyView2(mux *http.ServeMux) {
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
func (s *State) mutationDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f genomicDataCountFilter
	readBody(r, &f)
	summary := !strings.EqualFold(r.URL.Query().Get("projection"), "DETAILED")
	includeSampleIDs := r.URL.Query().Get("includeSampleIds") == "true"
	svf := f.StudyViewFilter
	if len(f.GenomicDataFilters) == 1 && summary {
		g := f.GenomicDataFilters[0]
		svf = svf.withoutMutation(g.HugoGeneSymbol, g.ProfileType, "MUTATED")
	}
	filtered := s.filterSQL(svf)
	out := []GenomicDataCountItem{}
	if summary {
		total := s.scopeSampleCount(svf)
		type countsRow struct {
			Mutated  uint64 `json:"mutated"`
			Profiled uint64 `json:"profiled"`
		}
		for _, g := range f.GenomicDataFilters {
			gene := sqlLit(g.HugoGeneSymbol)
			rows, err := ch.Query[countsRow](s.CH, fmt.Sprintf(
				"SELECT (SELECT uniqExact(sample_unique_id) FROM genomic_event_derived WHERE sample_unique_id IN (%[1]s) "+
					"AND hugo_gene_symbol = '%[2]s' AND variant_type = 'mutation') AS mutated, "+
					"(SELECT uniqExact(sgp.sample_unique_id) FROM sample_to_gene_panel_derived sgp "+
					"JOIN gene_panel_to_gene_derived gpg ON sgp.gene_panel_id = gpg.gene_panel_id "+
					"WHERE sgp.sample_unique_id IN (%[1]s) AND gpg.gene = '%[2]s' AND sgp.alteration_type = 'MUTATION_EXTENDED') AS profiled",
				filtered, gene))
			if err != nil || len(rows) == 0 {
				continue
			}
			mutated, profiled := rows[0].Mutated, rows[0].Profiled
			item := GenomicDataCountItem{HugoGeneSymbol: g.HugoGeneSymbol, ProfileType: "mutations", Counts: []GenomicDataCount{}}
			add := func(label, value string, n uint64) {
				if n > 0 {
					item.Counts = append(item.Counts, GenomicDataCount{Label: label, Value: value, Count: n, UniqueCount: n})
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
		writeJSON(w, out)
		return
	}
	if len(f.GenomicDataFilters) == 0 {
		writeJSON(w, out)
		return
	}
	genes := make([]string, len(f.GenomicDataFilters))
	for i, g := range f.GenomicDataFilters {
		genes[i] = g.HugoGeneSymbol
	}
	if includeSampleIDs {
		genes = genes[:1]
	}
	type typeRow struct {
		Gene   string `json:"gene"`
		Type   string `json:"type"`
		C      uint64 `json:"c"`
		U      uint64 `json:"u"`
		Sample string `json:"samples"`
	}
	rows, _ := ch.Query[typeRow](s.CH, fmt.Sprintf(
		"SELECT hugo_gene_symbol AS gene, mutation_type AS type, count() AS c, uniqExact(sample_unique_id) AS u, "+
			"arrayStringConcat(groupUniqArray(sample_unique_id), ',') AS samples FROM genomic_event_derived "+
			"WHERE sample_unique_id IN (%s) AND variant_type = 'mutation' AND hugo_gene_symbol IN (%s) "+
			"GROUP BY hugo_gene_symbol, mutation_type ORDER BY hugo_gene_symbol, mutation_type",
		filtered, inList(genes)))
	byGene := map[string]*GenomicDataCountItem{}
	var order []string
	for _, r := range rows {
		it, ok := byGene[r.Gene]
		if !ok {
			it = &GenomicDataCountItem{HugoGeneSymbol: r.Gene, ProfileType: "mutations", Counts: []GenomicDataCount{}}
			byGene[r.Gene] = it
			order = append(order, r.Gene)
		}
		c := GenomicDataCount{Label: strings.ReplaceAll(r.Type, "_", " "), Value: r.Type, Count: r.C, UniqueCount: r.U}
		if includeSampleIDs {
			c.SampleIDs = strings.Split(r.Sample, ",")
		}
		it.Counts = append(it.Counts, c)
	}
	for _, g := range order {
		out = append(out, *byGene[g])
	}
	if includeSampleIDs {
		gene := sqlLit(genes[0])
		type idsRow struct {
			N   uint64 `json:"n"`
			IDs string `json:"ids"`
		}
		profiled := fmt.Sprintf(
			"SELECT DISTINCT sgp.sample_unique_id FROM sample_to_gene_panel_derived sgp "+
				"JOIN gene_panel_to_gene_derived gpg ON sgp.gene_panel_id = gpg.gene_panel_id "+
				"WHERE sgp.sample_unique_id IN (%s) AND gpg.gene = '%s' AND sgp.alteration_type = 'MUTATION_EXTENDED'", filtered, gene)
		mutated := fmt.Sprintf(
			"SELECT sample_unique_id FROM genomic_event_derived WHERE sample_unique_id IN (%s) "+
				"AND variant_type = 'mutation' AND hugo_gene_symbol = '%s'", filtered, gene)
		extra := []struct{ label, value, sql string }{
			{"Not Mutated", "NOT_MUTATED", fmt.Sprintf("SELECT count() AS n, arrayStringConcat(groupUniqArray(sample_unique_id), ',') AS ids FROM (%s) WHERE sample_unique_id NOT IN (%s)", profiled, mutated)},
			{"Not Profiled", "NOT_PROFILED", fmt.Sprintf("SELECT count() AS n, arrayStringConcat(groupUniqArray(sample_unique_id), ',') AS ids FROM sample_derived WHERE cancer_study_identifier IN (%s) AND sample_unique_id NOT IN (%s)", inList(svf.studyIDs()), profiled)},
		}
		if len(out) == 0 {
			out = append(out, GenomicDataCountItem{HugoGeneSymbol: genes[0], ProfileType: "mutations", Counts: []GenomicDataCount{}})
		}
		for _, e := range extra {
			r, err := ch.Query[idsRow](s.CH, e.sql)
			if err != nil || len(r) == 0 {
				continue
			}
			c := GenomicDataCount{Label: e.label, Value: e.value, Count: r[0].N, UniqueCount: r[0].N}
			if r[0].IDs != "" {
				c.SampleIDs = strings.Split(r[0].IDs, ",")
			}
			out[0].Counts = append(out[0].Counts, c)
		}
	}
	writeJSON(w, out)
}

// cnaLabels names the discrete copy-number values.
var cnaLabels = map[string]string{
	"2": "Amplified", "1": "Gained", "0": "Diploid", "-1": "Heterozygously deleted", "-2": "Homozygously deleted",
}

// genomicDataCountsFetch counts discrete CNA values per gene over the filtered
// samples (the endpoint is CNA-specific), with the remainder reported as NA.
func (s *State) genomicDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f genomicDataCountFilter
	readBody(r, &f)
	out := []GenomicDataCountItem{}
	if len(f.GenomicDataFilters) == 0 {
		writeJSON(w, out)
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
	rows, _ := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT hugo_gene_symbol AS gene, toString(alteration_value) AS value, count() AS c FROM genetic_alteration_derived "+
			"WHERE NOT %s AND profile_type = '%s' AND sample_unique_id IN (%s) AND hugo_gene_symbol IN (%s) "+
			"GROUP BY hugo_gene_symbol, alteration_value ORDER BY hugo_gene_symbol, alteration_value",
		isNASQL("assumeNotNull(alteration_value)"), sqlLit(profileType), s.filterSQL(svf), inList(genes)))
	total := s.scopeSampleCount(svf)
	byGene := map[string]*GenomicDataCountItem{}
	var order []string
	var sum uint64
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
		sum += r.C
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
	if total > sum {
		na = total - sum
	}
	byGene[naGene].Counts = append(byGene[naGene].Counts, GenomicDataCount{Label: "NA", Value: "NA", Count: na})
	for _, g := range order {
		out = append(out, *byGene[g])
	}
	writeJSON(w, out)
}

// ---------- Treatments (no treatment data loaded) ----------

// ---------- Sample list (case list) counts ----------

// CaseListDataCount is the count of filtered samples in one case list.
type CaseListDataCount struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count uint64 `json:"count"`
}

func (s *State) sampleListsCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	scope := s.sampleScope(f)
	rows, err := ch.Query[CaseListDataCount](s.CH, fmt.Sprintf(
		"SELECT list_id AS value, any(name) AS label, uniqExact(sample_id) AS count "+
			"FROM sample_list WHERE %s GROUP BY list_id ORDER BY count DESC", scope))
	if err != nil {
		fail(w, err)
		return
	}
	if rows == nil {
		rows = []CaseListDataCount{}
	}
	writeJSON(w, rows)
}
