package api

import (
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// studyViewFilter is the subset of cBioPortal's StudyViewFilter we act on: the
// study/sample scope. Richer sub-filters are accepted but not yet applied.
type studyViewFilter struct {
	StudyIDs          []string          `json:"studyIds"`
	SampleIdentifiers []identifier       `json:"sampleIdentifiers"`
	AlterationFilter  *alterationFilter `json:"alterationFilter"`
}

func (f studyViewFilter) sampleScope() string {
	if len(f.SampleIdentifiers) > 0 {
		pairs := make([][2]string, len(f.SampleIdentifiers))
		for i, id := range f.SampleIdentifiers {
			pairs[i] = [2]string{id.StudyID, id.id()}
		}
		return fmt.Sprintf("(study_id, sample_id) IN (%s)", tupleIn(pairs))
	}
	if len(f.StudyIDs) > 0 {
		return fmt.Sprintf("study_id IN (%s)", inList(f.StudyIDs))
	}
	return "1 = 0"
}

// sampleDerivedScope expresses the same sample-selection semantics as
// sampleScope but over a `<x>_unique_id` column of the prod-aligned `*_derived`
// views, where `sample_unique_id = concat(study_id,'_',sample_id)`. When only
// studies are given it scopes by `cancer_study_identifier`. Pass
// "sample_unique_id" for the sample scope and "patient_unique_id" for the
// patient scope; the identifier-based branch reuses the same id values in both
// positions, matching the legacy `ReplaceAll(scope,"sample_id","patient_id")`.
func (f studyViewFilter) sampleDerivedScope(uniqueCol string) string {
	if len(f.SampleIdentifiers) > 0 {
		ids := make([]string, len(f.SampleIdentifiers))
		for i, id := range f.SampleIdentifiers {
			ids[i] = id.StudyID + "_" + id.id()
		}
		return fmt.Sprintf("%s IN (%s)", uniqueCol, inList(ids))
	}
	if len(f.StudyIDs) > 0 {
		return fmt.Sprintf("cancer_study_identifier IN (%s)", inList(f.StudyIDs))
	}
	return "1 = 0"
}

func (s *State) scopeSampleCount(f studyViewFilter) uint64 {
	type c struct {
		N uint64 `json:"n"`
	}
	rows, _ := ch.Query[c](s.CH, fmt.Sprintf(
		"SELECT count() AS n FROM sample_derived WHERE %s", f.sampleDerivedScope("sample_unique_id")))
	if len(rows) > 0 {
		return rows[0].N
	}
	return 0
}

func (s *State) filteredSamplesFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	rows, err := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT cancer_study_identifier AS studyId, sample_stable_id AS sampleId, patient_stable_id AS patientId "+
			"FROM sample_derived WHERE %s ORDER BY cancer_study_identifier, sample_stable_id",
		f.sampleDerivedScope("sample_unique_id")))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]Sample, len(rows))
	for i, r := range rows {
		out[i] = Sample{
			SampleID: r.SampleID, PatientID: r.PatientID, StudyID: r.StudyID,
			UniqueSampleKey: uniqueKey(r.StudyID, r.SampleID), UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
		}
	}
	writeJSON(w, out)
}

// ---------- Clinical data counts ----------

type ClinicalDataCount struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}
type ClinicalDataCountItem struct {
	AttributeID string              `json:"attributeId"`
	Counts      []ClinicalDataCount `json:"counts"`
}

func (s *State) clinicalDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Attributes []struct {
			AttributeID      string `json:"attributeId"`
			ClinicalDataType string `json:"clinicalDataType"`
		} `json:"attributes"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	readBody(r, &f)
	var sampleAttrs, patientAttrs []string
	for _, a := range f.Attributes {
		if strings.EqualFold(a.ClinicalDataType, "PATIENT") {
			patientAttrs = append(patientAttrs, a.AttributeID)
		} else {
			sampleAttrs = append(sampleAttrs, a.AttributeID)
		}
	}
	items := map[string][]ClinicalDataCount{}
	type countRow struct {
		AttrID string `json:"attr_id"`
		Value  string `json:"value"`
		C      uint64 `json:"c"`
	}
	queries := []struct {
		attrs []string
		typ   string
		scope string
	}{
		{sampleAttrs, "sample", f.StudyViewFilter.sampleDerivedScope("sample_unique_id")},
		{patientAttrs, "patient", f.StudyViewFilter.sampleDerivedScope("patient_unique_id")},
	}
	for _, q := range queries {
		if len(q.attrs) == 0 {
			continue
		}
		rows, _ := ch.Query[countRow](s.CH, fmt.Sprintf(
			"SELECT attribute_name AS attr_id, attribute_value AS value, count() AS c FROM clinical_data_derived "+
				"WHERE type = '%s' AND %s AND attribute_name IN (%s) GROUP BY attribute_name, attribute_value",
			q.typ, q.scope, inList(q.attrs)))
		for _, r := range rows {
			items[r.AttrID] = append(items[r.AttrID], ClinicalDataCount{Value: r.Value, Count: r.C})
		}
	}
	out := make([]ClinicalDataCountItem, 0, len(items))
	for id, counts := range items {
		out = append(out, ClinicalDataCountItem{AttributeID: id, Counts: counts})
	}
	writeJSON(w, out)
}

// ---------- Gene alteration counts ----------

type geneCountRow struct {
	HugoGeneSymbol       string `json:"hugoGeneSymbol"`
	EntrezGeneID         *int64 `json:"entrezGeneId"`
	Alteration           *int64 `json:"alteration"`
	NumberOfAlteredCases uint64 `json:"numberOfAlteredCases"`
	TotalCount           uint64 `json:"totalCount"`
}

type AlterationCountByGene struct {
	EntrezGeneID          *int64 `json:"entrezGeneId"`
	HugoGeneSymbol        string `json:"hugoGeneSymbol"`
	Alteration            *int64 `json:"alteration,omitempty"`
	NumberOfAlteredCases  uint64 `json:"numberOfAlteredCases"`
	NumberOfProfiledCases uint64 `json:"numberOfProfiledCases"`
	TotalCount            uint64 `json:"totalCount"`
}

func (s *State) mutatedGenesFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	scope := andClause(f.sampleScope(), f.AlterationFilter.mutationFilterClause())
	profiled := s.scopeSampleCount(f)
	rows, err := ch.Query[geneCountRow](s.CH, fmt.Sprintf(
		"SELECT hugo_symbol AS hugoGeneSymbol, toInt64OrNull(entrez_gene_id) AS entrezGeneId, "+
			"uniqExact(sample_id) AS numberOfAlteredCases, count() AS totalCount FROM mutation WHERE %s "+
			"GROUP BY hugo_symbol, entrez_gene_id ORDER BY numberOfAlteredCases DESC", scope))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toAlterationCounts(rows, profiled, false))
}

func (s *State) cnaGenesFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	scope := andClause(f.sampleScope(), f.AlterationFilter.cnaFilterClause())
	profiled := s.scopeSampleCount(f)
	rows, err := ch.Query[geneCountRow](s.CH, fmt.Sprintf(
		"SELECT hugo_symbol AS hugoGeneSymbol, toInt64OrNull(entrez_gene_id) AS entrezGeneId, "+
			"toInt64OrNull(alteration) AS alteration, uniqExact(sample_id) AS numberOfAlteredCases, count() AS totalCount "+
			"FROM cna WHERE %s GROUP BY hugo_symbol, entrez_gene_id, alteration ORDER BY numberOfAlteredCases DESC", scope))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toAlterationCounts(rows, profiled, true))
}

func toAlterationCounts(rows []geneCountRow, profiled uint64, keepAlteration bool) []AlterationCountByGene {
	out := make([]AlterationCountByGene, len(rows))
	for i, r := range rows {
		a := AlterationCountByGene{
			EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol,
			NumberOfAlteredCases: r.NumberOfAlteredCases, NumberOfProfiledCases: profiled, TotalCount: r.TotalCount,
		}
		if keepAlteration {
			a.Alteration = r.Alteration
		}
		out[i] = a
	}
	return out
}

// ---------- Data types + structural variants ----------

type GenomicDataCount struct {
	Value       string   `json:"value"`
	Label       string   `json:"label"`
	Count       uint64   `json:"count"`
	UniqueCount uint64   `json:"uniqueCount"`
	SampleIDs   []string `json:"sampleIds"`
}

func (s *State) molecularProfileSampleCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	scope := f.sampleScope()
	sources := []struct{ stable, label, view string }{
		{"mutations", "MUTATION_EXTENDED", "mutation"},
		{"cna", "COPY_NUMBER_ALTERATION", "cna"},
		{"structural_variants", "STRUCTURAL_VARIANT", "structural_variant"},
	}
	out := []GenomicDataCount{}
	type c struct {
		Value string `json:"value"`
		Count uint64 `json:"count"`
	}
	for _, src := range sources {
		rows, _ := ch.Query[c](s.CH, fmt.Sprintf(
			"SELECT '%s' AS value, uniqExact(sample_id) AS count FROM %s WHERE %s", src.stable, src.view, scope))
		if len(rows) > 0 && rows[0].Count > 0 {
			out = append(out, GenomicDataCount{Value: rows[0].Value, Label: src.label, Count: rows[0].Count, UniqueCount: rows[0].Count, SampleIDs: []string{}})
		}
	}
	writeJSON(w, out)
}

type svRow struct {
	Gene1                string `json:"gene1"`
	Gene1Entrez          *int64 `json:"gene1Entrez"`
	Gene2                string `json:"gene2"`
	Gene2Entrez          *int64 `json:"gene2Entrez"`
	NumberOfAlteredCases uint64 `json:"numberOfAlteredCases"`
	TotalCount           uint64 `json:"totalCount"`
}

type AlterationCountByStructuralVariant struct {
	Gene1HugoGeneSymbol   string   `json:"gene1HugoGeneSymbol"`
	Gene1EntrezGeneID     *int64   `json:"gene1EntrezGeneId"`
	Gene2HugoGeneSymbol   string   `json:"gene2HugoGeneSymbol"`
	Gene2EntrezGeneID     *int64   `json:"gene2EntrezGeneId"`
	HugoGeneSymbols       []string `json:"hugoGeneSymbols"`
	EntrezGeneIDs         []int64  `json:"entrezGeneIds"`
	NumberOfAlteredCases  uint64   `json:"numberOfAlteredCases"`
	NumberOfProfiledCases uint64   `json:"numberOfProfiledCases"`
	TotalCount            uint64   `json:"totalCount"`
	UniqueEventKey        string   `json:"uniqueEventKey"`
}

func (s *State) svGenesFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	scope := andClause(f.sampleScope(), f.AlterationFilter.svFilterClause())
	profiled := s.scopeSampleCount(f)
	rows, _ := ch.Query[svRow](s.CH, fmt.Sprintf(
		"SELECT site1_hugo_symbol AS gene1, site1_entrez_gene_id AS gene1Entrez, site2_hugo_symbol AS gene2, "+
			"site2_entrez_gene_id AS gene2Entrez, uniqExact(sample_id) AS numberOfAlteredCases, count() AS totalCount "+
			"FROM structural_variant WHERE %s GROUP BY site1_hugo_symbol, site1_entrez_gene_id, site2_hugo_symbol, site2_entrez_gene_id "+
			"ORDER BY numberOfAlteredCases DESC", scope))
	out := make([]AlterationCountByStructuralVariant, len(rows))
	for i, r := range rows {
		var symbols []string
		var entrez []int64
		for _, g := range []string{r.Gene1, r.Gene2} {
			if g != "" {
				symbols = append(symbols, g)
			}
		}
		for _, e := range []*int64{r.Gene1Entrez, r.Gene2Entrez} {
			if e != nil {
				entrez = append(entrez, *e)
			}
		}
		out[i] = AlterationCountByStructuralVariant{
			Gene1HugoGeneSymbol: r.Gene1, Gene1EntrezGeneID: r.Gene1Entrez,
			Gene2HugoGeneSymbol: r.Gene2, Gene2EntrezGeneID: r.Gene2Entrez,
			HugoGeneSymbols: symbols, EntrezGeneIDs: entrez,
			NumberOfAlteredCases: r.NumberOfAlteredCases, NumberOfProfiledCases: profiled, TotalCount: r.TotalCount,
			UniqueEventKey: r.Gene1 + "::" + r.Gene2,
		}
	}
	writeJSON(w, out)
}
