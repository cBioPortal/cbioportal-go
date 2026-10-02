package contract

import "testing"

// Contracts for what the import transform stores, following cbioportal-core.

// AddCaseList: add_global_case_list adds "<study>_all" with category "other"
// and name/description "All cases in study"; a study's own _all list is kept
// as given, once.
func TestContractGlobalCaseListFromAddCaseList(t *testing.T) {
	var sl struct {
		Category, Name, Description string
		SampleCount                 int
	}
	get(t, "/api/sample-lists/study_es_0_all", &sl)
	wantEq(t, []any{sl.Category, sl.Name, sl.Description, sl.SampleCount},
		[]any{"other", "All cases in study", "All cases in study", 845})

	var own struct {
		Category    string
		SampleCount int
	}
	get(t, "/api/sample-lists/ascn_test_study_all", &own)
	wantEq(t, []any{own.Category, own.SampleCount}, []any{"all_cases_in_study", 6})
}

// ImportSampleList: an empty case_list_category is stored as "other".
func TestContractCaseListEmptyCategoryIsOther(t *testing.T) {
	var sl struct{ Category string }
	get(t, "/api/sample-lists/study_es_0_cna", &sl)
	wantEq(t, sl.Category, "other")
}

// StructuralVariantUtil: empty text fields are "NA", empty integer fields -1.
func TestContractStructuralVariantImportDefaults(t *testing.T) {
	var svs []map[string]any
	post(t, "/api/structural-variant/fetch", map[string]any{
		"molecularProfileIds": []string{"study_es_0_structural_variants"},
		"entrezGeneIds":       []int{238},
	}, &svs)
	for _, sv := range svs {
		if sv["sampleId"] != "TCGA-A2-A04P-01" || sv["site2HugoSymbol"] != "PTPN3" {
			continue
		}
		wantEq(t, []any{sv["site1Contig"], sv["normalReadCount"], sv["length"], sv["tumorReadCount"]},
			[]any{"NA", float64(-1), float64(-1), float64(1005)})
		return
	}
	t.Fatal("ALK-PTPN3 fusion of TCGA-A2-A04P-01 not returned")
}

// Discrete CNA keeps every level per gene x sample (genetic_alteration), so
// ALL includes diploid calls, while the default reads AMP/HOMDEL events only.
func TestContractDiscreteCopyNumberLevels(t *testing.T) {
	levels := func(eventType string) map[int]int {
		var rows []struct{ Alteration int }
		get(t, "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number?sampleListId=study_es_0_all&discreteCopyNumberEventType="+eventType, &rows)
		out := map[int]int{}
		for _, r := range rows {
			out[r.Alteration]++
		}
		return out
	}
	all := levels("ALL")
	if all[0] == 0 || all[1] == 0 || all[-1] == 0 {
		t.Fatalf("ALL lacks diploid/gain/hetloss calls: %v", all)
	}
	def := levels("HOMDEL_AND_AMP")
	wantEq(t, len(def), 2)
	wantEq(t, []int{def[-2], def[2]}, []int{all[-2], all[2]})
}

// ImportTabDelimData resolves a symbol without an Entrez id through the gene
// aliases and stores the line under the current gene (HIST1H3I -> H3C11).
func TestContractCnaAliasResolvedToCurrentGene(t *testing.T) {
	var rows []struct {
		EntrezGeneID int64 `json:"entrezGeneId"`
		Gene         struct {
			HugoGeneSymbol string `json:"hugoGeneSymbol"`
		} `json:"gene"`
	}
	get(t, "/api/molecular-profiles/ascn_test_study_cna/discrete-copy-number?sampleListId=ascn_test_study_all&projection=DETAILED", &rows)
	n := 0
	for _, r := range rows {
		if r.EntrezGeneID == 8354 {
			wantEq(t, r.Gene.HugoGeneSymbol, "H3C11")
			n++
		}
	}
	wantEq(t, n, 3)
}

// ReferenceGenomeGeneMapper: genes carry their chromosome and cytoband.
func TestContractReferenceGenomeGeneLocation(t *testing.T) {
	var g struct {
		Chromosome, Cytoband string
		ReferenceGenomeID    int `json:"referenceGenomeId"`
	}
	get(t, "/api/reference-genome-genes/hg19/7157", &g)
	if g.Chromosome == "" {
		t.Skip("reference DB lacks reference_genome_gene (re-run load-reference)")
	}
	wantEq(t, []any{g.Chromosome, g.Cytoband, g.ReferenceGenomeID}, []any{"17", "17p13.1", 1})
}
