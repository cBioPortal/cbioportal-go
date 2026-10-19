package contract

import (
	"net/http"
	"slices"
	"sort"
	"testing"
)

// Contracts for molecular-profile, gene, gene-panel, sample-list and
// gene-set endpoints the frontend calls. Expected values follow the Java
// controllers / mappers applied to the e2e fixture studies.

// ---------------------------------------------------------- sample lists ----

// SampleListMapper: SUMMARY carries the list fields, ID only the id; DETAILED
// adds the sample ids and their count. Unknown ids are ignored. The importer
// names the generated _all list "All cases in study" (AddCaseList); lists
// without a category get "other".
func TestContractSampleListsFetch(t *testing.T) {
	requireAPI(t)
	var lists []map[string]any
	post(t, "/api/sample-lists/fetch", []string{"study_es_0_cnaseq", "study_es_0_all", "no_such_list"}, &lists)
	byID := map[string]map[string]any{}
	for _, l := range lists {
		byID[l["sampleListId"].(string)] = l
	}
	wantEq(t, byID, map[string]map[string]any{
		"study_es_0_all": {"sampleListId": "study_es_0_all", "studyId": "study_es_0", "category": "other",
			"name": "All cases in study", "description": "All cases in study"},
		"study_es_0_cnaseq": {"sampleListId": "study_es_0_cnaseq", "studyId": "study_es_0", "category": "other",
			"name":        "Samples profiled for mutations and CNA",
			"description": "This is this case list that contains all samples that are profiled for mutations and CNA."},
	})

	lists = nil
	post(t, "/api/sample-lists/fetch?projection=ID", []string{"study_es_0_cnaseq"}, &lists)
	wantEq(t, lists, []map[string]any{{"sampleListId": "study_es_0_cnaseq"}})

	var detailed []struct {
		SampleListID string   `json:"sampleListId"`
		SampleCount  int      `json:"sampleCount"`
		SampleIDs    []string `json:"sampleIds"`
	}
	post(t, "/api/sample-lists/fetch?projection=DETAILED", []string{"study_es_0_cnaseq"}, &detailed)
	if len(detailed) != 1 {
		t.Fatalf("got %d lists", len(detailed))
	}
	d := detailed[0]
	wantEq(t, d.SampleCount, 788)
	wantEq(t, len(d.SampleIDs), 788)
	for _, id := range []string{"TCGA-A1-A0SB-01", "TEST-A23C-01", "TEST_SAMPLE_15", "TCGA-GI-A2C8-01"} {
		if !slices.Contains(d.SampleIDs, id) {
			t.Errorf("DETAILED sampleIds lack %s", id)
		}
	}

	// GET .../sample-ids lists the same samples.
	var ids []string
	get(t, "/api/sample-lists/study_es_0_cnaseq/sample-ids", &ids)
	a, b := slices.Clone(ids), slices.Clone(d.SampleIDs)
	sort.Strings(a)
	sort.Strings(b)
	wantEq(t, a, b)
	wantError(t, "GET", "/api/sample-lists/no_such_list/sample-ids", "", http.StatusNotFound, "Sample list not found: no_such_list")
}

// ----------------------------------------------------------- gene panels ----

// GenePanelServiceImpl.getGenePanel: the panel with its genes ordered by Hugo
// symbol (GenePanelMapper.getGenesOfPanels); data_gene_panel_testpanel1.txt.
func TestContractGenePanelDetail(t *testing.T) {
	requireAPI(t)
	var p map[string]any
	get(t, "/api/gene-panels/TESTPANEL1", &p)
	genes := []any{}
	for _, g := range []struct {
		id  float64
		sym string
	}{
		{3983, "ABLIM1"}, {55, "ACP3"}, {80070, "ADAMTS20"}, {472, "ATM"}, {672, "BRCA1"}, {675, "BRCA2"},
		{253559, "CADM2"}, {1838, "DTNB"}, {2648, "KAT2A"}, {4437, "MSH3"}, {4602, "MYB"}, {440348, "NPIPB15"},
		{81061, "OR11H1"}, {56914, "OTOR"}, {27334, "P2RY10"}, {388946, "TMEM247"}, {7157, "TP53"},
	} {
		genes = append(genes, map[string]any{"entrezGeneId": g.id, "hugoGeneSymbol": g.sym})
	}
	wantEq(t, p, map[string]any{
		"genePanelId": "TESTPANEL1", "description": "Example gene panel meta file for testing purposes.", "genes": genes,
	})
}

// ---------------------------------------------------- molecular profiles ----

// MolecularProfileMapper: by study ids the profiles come ordered by stable
// id; patientLevel defaults to false, absent pivotThreshold / sortOrder /
// genericAssayType are left out.
func TestContractMolecularProfilesFetch(t *testing.T) {
	requireAPI(t)
	var profiles []map[string]any
	post(t, "/api/molecular-profiles/fetch", map[string]any{"studyIds": []string{"study_hg38", "ascn_test_study"}}, &profiles)
	wantEq(t, profiles, []map[string]any{
		{"molecularProfileId": "ascn_test_study_cna", "studyId": "ascn_test_study", "molecularAlterationType": "COPY_NUMBER_ALTERATION",
			"datatype": "DISCRETE", "name": "Discrete Copy Number Data", "description": "Discrete Copy Number Data",
			"showProfileInAnalysisTab": true, "patientLevel": false},
		{"molecularProfileId": "ascn_test_study_mutations", "studyId": "ascn_test_study", "molecularAlterationType": "MUTATION_EXTENDED",
			"datatype": "MAF", "name": "Mutations", "description": "Mutation data",
			"showProfileInAnalysisTab": true, "patientLevel": false},
		{"molecularProfileId": "study_hg38_mutations", "studyId": "study_hg38", "molecularAlterationType": "MUTATION_EXTENDED",
			"datatype": "MAF", "name": "Mutations", "description": "Mutation data from whole exome sequencing.",
			"showProfileInAnalysisTab": true, "patientLevel": false},
	})

	profiles = nil
	post(t, "/api/molecular-profiles/fetch?projection=ID", map[string]any{
		"molecularProfileIds": []string{"study_es_0_gistic", "ascn_test_study_cna", "no_such_profile"}}, &profiles)
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i]["molecularProfileId"].(string) < profiles[j]["molecularProfileId"].(string)
	})
	wantEq(t, profiles, []map[string]any{
		{"molecularProfileId": "ascn_test_study_cna", "studyId": "ascn_test_study"},
		{"molecularProfileId": "study_es_0_gistic", "studyId": "study_es_0"},
	})

	// study_es_0 has 13 profile meta files.
	resp, _ := rawDo(t, "POST", "/api/molecular-profiles/fetch?projection=META", `{"studyIds":["study_es_0"]}`)
	wantEq(t, resp.Header.Get("total-count"), "13")
}

// ----------------------------------------------------------------- genes ----

// GeneMapper.getGenes: each keyword term is a case-insensitive Hugo prefix,
// ordered by symbol; an alias matches exactly; without a keyword the ID
// projection orders by Entrez id.
func TestContractGenesSearch(t *testing.T) {
	requireAPI(t)
	var genes []map[string]any
	genes = nil
	get(t, "/api/genes?keyword=BRCA&projection=ID", &genes)
	wantEq(t, genes, []map[string]any{
		{"entrezGeneId": 672.0, "hugoGeneSymbol": "BRCA1"},
		{"entrezGeneId": 394269.0, "hugoGeneSymbol": "BRCA1P1"},
		{"entrezGeneId": 675.0, "hugoGeneSymbol": "BRCA2"},
	})
	genes = nil
	get(t, "/api/genes?keyword=brca", &genes)
	wantEq(t, ids(genes, "hugoGeneSymbol"), []string{"BRCA1", "BRCA1P1", "BRCA2"})
	wantEq(t, genes[1]["type"], "pseudogene")

	// A prefix, not a substring: ATP5... does not match TP5.
	resp, _ := rawDo(t, "GET", "/api/genes?keyword=TP5&projection=META", "")
	wantEq(t, resp.Header.Get("total-count"), "23")
	genes = nil
	get(t, "/api/genes?keyword=TP53&pageSize=3&pageNumber=1", &genes)
	wantEq(t, ids(genes, "hugoGeneSymbol"), []string{"TP53BP2", "TP53BP2P1", "TP53COR1"})
	// Terms are ANDed: no symbol starts with both.
	genes = nil
	get(t, "/api/genes?keyword=TP53%20BRCA", &genes)
	wantEq(t, len(genes), 0)

	genes = nil
	get(t, "/api/genes?alias=LFS1", &genes)
	wantEq(t, ids(genes, "hugoGeneSymbol"), []string{"TP53"})
	genes = nil
	get(t, "/api/genes?alias=p53", &genes)
	wantEq(t, ids(genes, "hugoGeneSymbol"), []string{"TP53"})
	genes = nil
	get(t, "/api/genes?alias=P53", &genes)
	wantEq(t, len(genes), 0)
}

// ReferenceGenomeGeneController.fetchReferenceGenomeGenes: Entrez ids when the
// first id is an integer, else Hugo symbols with their exact case; the body
// holds 1 to 100000 ids.
func TestContractReferenceGenomeGenesFetch(t *testing.T) {
	requireAPI(t)
	tp53 := func(genome float64) map[string]any {
		return map[string]any{"entrezGeneId": 7157.0, "referenceGenomeId": genome, "hugoGeneSymbol": "TP53",
			"chromosome": "17", "cytoband": "17p13.1", "start": 0.0, "end": 0.0}
	}
	brca1 := func(genome float64) map[string]any {
		return map[string]any{"entrezGeneId": 672.0, "referenceGenomeId": genome, "hugoGeneSymbol": "BRCA1",
			"chromosome": "17", "cytoband": "17q21.31", "start": 0.0, "end": 0.0}
	}
	var genes []map[string]any
	post(t, "/api/reference-genome-genes/hg19/fetch", []string{"7157", "672"}, &genes)
	wantEq(t, genes, []map[string]any{brca1(1), tp53(1)})
	genes = nil
	post(t, "/api/reference-genome-genes/hg38/fetch", []string{"TP53", "brca1"}, &genes)
	wantEq(t, genes, []map[string]any{tp53(2)})
	wantError(t, "POST", "/api/reference-genome-genes/hg19/fetch", "[]", http.StatusBadRequest,
		"geneIds size must be between 1 and 100000")
}

// ------------------------------------------------------ profile data ----

// MutationSpectrumServiceImpl: per sample, the profile's single-base
// substitutions folded onto the C/T strand. Samples come in the order of the
// HashMap they are grouped into; samples without SNPs (TCGA-A2-A0CM-01 only
// has a deletion) or without mutations are absent.
func TestContractMutationSpectrums(t *testing.T) {
	requireAPI(t)
	var rows []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_mutations/mutation-spectrums/fetch", map[string]any{
		"sampleIds": []string{"TCGA-A1-A0SB-01", "TCGA-A2-A04P-01", "TCGA-AR-A1AR-01", "TCGA-B6-A0I6-01",
			"TCGA-BH-A0E0-01", "TCGA-A2-A0CM-01", "TCGA-A1-A0SD-01"},
	}, &rows)
	spectrum := func(sample, patient string, ca, cg, ct, ta, tc, tg float64) map[string]any {
		return map[string]any{
			"uniqueSampleKey": javaKey(sample, "study_es_0"), "uniquePatientKey": javaKey(patient, "study_es_0"),
			"molecularProfileId": "study_es_0_mutations", "sampleId": sample, "patientId": patient, "studyId": "study_es_0",
			"CtoA": ca, "CtoG": cg, "CtoT": ct, "TtoA": ta, "TtoC": tc, "TtoG": tg,
		}
	}
	wantEq(t, rows, []map[string]any{
		spectrum("TCGA-BH-A0E0-01", "TCGA-BH-A0E0", 0, 0, 0, 0, 1, 0),
		spectrum("TCGA-B6-A0I6-01", "TCGA-B6-A0I6", 0, 0, 0, 0, 1, 0),
		spectrum("TCGA-A1-A0SB-01", "TCGA-A1-A0SB", 2, 0, 2, 0, 4, 1),
		spectrum("TCGA-AR-A1AR-01", "TCGA-AR-A1AR", 0, 0, 0, 0, 1, 0),
		spectrum("TCGA-A2-A04P-01", "TCGA-A2-A04P", 0, 0, 0, 0, 1, 0),
	})
}

// DiscreteCopyNumberServiceImpl.fetchCopyNumberCounts: one count per
// requested (gene, alteration) in request order, counting the samples with
// that event (sample_cna_event holds only -2 and 2); the altered count is
// null for an event no sample has. numberOfSamples is the profile's 788
// samples. Only a discrete CNA profile is found.
func TestContractDiscreteCopyNumberCounts(t *testing.T) {
	requireAPI(t)
	var counts []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number-counts/fetch", []map[string]int{
		{"entrezGeneId": 116983, "alteration": 2}, {"entrezGeneId": 116983, "alteration": -1},
		{"entrezGeneId": 83858, "alteration": -2}, {"entrezGeneId": 7157, "alteration": 2},
	}, &counts)
	count := func(gene, alt float64, n *float64) map[string]any {
		m := map[string]any{"molecularProfileId": "study_es_0_gistic", "entrezGeneId": gene, "alteration": alt, "numberOfSamples": 788.0}
		if n != nil {
			m["numberOfSamplesWithAlterationInGene"] = *n
		}
		return m
	}
	seven, two := 7.0, 2.0
	wantEq(t, counts, []map[string]any{
		count(116983, 2, &seven), count(116983, -1, nil), count(83858, -2, &two), count(7157, 2, nil),
	})
	wantError(t, "POST", "/api/molecular-profiles/study_es_0_mrna/discrete-copy-number-counts/fetch",
		`[{"entrezGeneId":7157,"alteration":2}]`, http.StatusNotFound, "Molecular profile not found: study_es_0_mrna")
	wantError(t, "POST", "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number-counts/fetch",
		`[]`, http.StatusBadRequest, "copyNumberCountIdentifiers size must be between 1 and 50000")
}

// GenePanelServiceImpl.fetchGenePanelData: per requested sample of the study,
// its panel in the profile (none for whole-exome) and whether it is profiled.
// A mutation sample outside the MAF counts as profiled only when it is in the
// _sequenced list; TCGA-A1-A0SD-01 is in neither, but is in the CNA data.
func TestContractGenePanelDataInProfile(t *testing.T) {
	requireAPI(t)
	samples := []string{"TCGA-A1-A0SB-01", "TEST_SAMPLE_1", "TEST_SAMPLE_2", "TCGA-A1-A0SD-01", "NO_SUCH_SAMPLE"}
	type key struct{ sample, panel string }
	fetch := func(profile string) map[key]bool {
		var rows []map[string]any
		post(t, "/api/molecular-profiles/"+profile+"/gene-panel-data/fetch", map[string]any{"sampleIds": samples}, &rows)
		out := map[key]bool{}
		for _, r := range rows {
			panel, _ := r["genePanelId"].(string)
			out[key{r["sampleId"].(string), panel}] = r["profiled"].(bool)
			if r["molecularProfileId"] != profile || r["studyId"] != "study_es_0" ||
				r["uniqueSampleKey"] != javaKey(r["sampleId"].(string), "study_es_0") {
				t.Errorf("%s: row %v", profile, r)
			}
		}
		return out
	}
	wantEq(t, fetch("study_es_0_mutations"), map[key]bool{
		{"TCGA-A1-A0SB-01", "TESTPANEL1"}: true, {"TEST_SAMPLE_1", ""}: true,
		{"TEST_SAMPLE_2", "TESTPANEL1"}: true, {"TCGA-A1-A0SD-01", ""}: false,
	})
	wantEq(t, fetch("study_es_0_gistic"), map[key]bool{
		{"TCGA-A1-A0SB-01", "TESTPANEL1"}: true, {"TEST_SAMPLE_1", ""}: true,
		{"TEST_SAMPLE_2", ""}: true, {"TCGA-A1-A0SD-01", ""}: true,
	})

	var rows []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_mutations/gene-panel-data/fetch", map[string]any{"sampleListId": "study_es_0_sequenced"}, &rows)
	wantEq(t, len(rows), 31)
	for _, r := range rows {
		if r["profiled"] != true {
			t.Errorf("sequenced sample not profiled: %v", r)
		}
	}
}

// AlterationDriverAnnotationServiceImpl: binary annotations present and the
// sorted distinct tiers (NA / empty excluded) of the profiles' mutation, CNA
// event and SV annotations. An empty list reports none.
func TestContractCustomDriverAnnotationReport(t *testing.T) {
	requireAPI(t)
	report := func(ids []string) map[string]any {
		var r map[string]any
		post(t, "/api/custom-driver-annotation-report/fetch", ids, &r)
		return r
	}
	wantEq(t, report([]string{"study_es_0_mutations"}), map[string]any{"hasBinary": true, "tiers": []any{"Class 1", "Class 2", "Class 3", "Class 4"}})
	wantEq(t, report([]string{"study_es_0_gistic"}), map[string]any{"hasBinary": true, "tiers": []any{"Class 1", "Class 2"}})
	wantEq(t, report([]string{"study_es_0_structural_variants"}), map[string]any{"hasBinary": true, "tiers": []any{"Class 1", "Class 2", "Class 3", "Class 4", "Unknown"}})
	wantEq(t, report([]string{"study_hg38_mutations"}), map[string]any{"hasBinary": false, "tiers": []any{}})
	wantEq(t, report([]string{}), map[string]any{"hasBinary": false, "tiers": []any{}})
}

// ------------------------------------------------------------- gene sets ----

// GenesetDataServiceImpl.fetchGenesetData: the GSVA score of every requested
// gene set for every requested sample of the profile (data_gsva_scores.txt);
// TCGA-A2-A04P-01 has no scores. Values are truncated to 4 decimals.
func TestContractGenesetGeneticData(t *testing.T) {
	requireAPI(t)
	var rows []map[string]any
	post(t, "/api/genetic-profiles/study_es_0_gsva_scores/geneset-genetic-data/fetch", map[string]any{
		"genesetIds": []string{"GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS", "GO_ACYLGLYCEROL_HOMEOSTASIS"},
		"sampleIds":  []string{"TCGA-A2-A04U-01", "TCGA-A1-A0SB-01", "TCGA-A2-A04P-01"},
	}, &rows)
	got := map[[2]string]string{}
	for _, r := range rows {
		got[[2]string{r["sampleId"].(string), r["genesetId"].(string)}] = r["value"].(string)
		if r["geneticProfileId"] != "study_es_0_gsva_scores" || r["studyId"] != "study_es_0" ||
			r["patientId"] != r["sampleId"].(string)[:12] {
			t.Errorf("row %v", r)
		}
	}
	wantEq(t, got, map[[2]string]string{
		{"TCGA-A1-A0SB-01", "GO_ACYLGLYCEROL_HOMEOSTASIS"}:                                 "-0.1385",
		{"TCGA-A2-A04U-01", "GO_ACYLGLYCEROL_HOMEOSTASIS"}:                                 "-0.5034",
		{"TCGA-A1-A0SB-01", "GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS"}: "0.0644",
		{"TCGA-A2-A04U-01", "GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS"}: "-0.0625",
	})
}

// GenesetCorrelationServiceImpl: Spearman correlation of each member gene's
// expression (study_es_0_mrna, the profile the GSVA scores derive from) with
// the gene-set scores over the study's samples, kept at or above the
// threshold and sorted descending. The scores cover 4 samples with
// expression, so the correlations are 0.8 / 0.4.
func TestContractGenesetExpressionCorrelation(t *testing.T) {
	requireAPI(t)
	var rows []map[string]any
	var version string
	get(t, "/api/genesets/version", &version)
	if version == "msigdb_7.5.1" {
		// With the gene set reference only member genes are correlated:
		// GO_ACYLGLYCEROL_HOMEOSTASIS has AKT1 and AKT2, which
		// data_expression_median.txt lacks, so their correlation is 0 — kept
		// at threshold 0, dropped at 0.3.
		post(t, "/api/genesets/GO_ACYLGLYCEROL_HOMEOSTASIS/expression-correlation/fetch?geneticProfileId=study_es_0_gsva_scores&correlationThreshold=0.3", nil, &rows)
		wantEq(t, len(rows), 0)
		post(t, "/api/genesets/GO_ACYLGLYCEROL_HOMEOSTASIS/expression-correlation/fetch?geneticProfileId=study_es_0_gsva_scores&correlationThreshold=0", nil, &rows)
		wantEq(t, rows, []map[string]any{
			{"correlationValue": 0.0, "entrezGeneId": 207.0, "expressionGeneticProfileId": "study_es_0_mrna",
				"hugoGeneSymbol": "AKT1", "zScoreGeneticProfileId": "study_es_0_mrna_median_Zscores"},
			{"correlationValue": 0.0, "entrezGeneId": 208.0, "expressionGeneticProfileId": "study_es_0_mrna",
				"hugoGeneSymbol": "AKT2", "zScoreGeneticProfileId": "study_es_0_mrna_median_Zscores"},
		})
		resp, body := rawDo(t, http.MethodPost, "/api/genesets/NOPE/expression-correlation/fetch?geneticProfileId=study_es_0_gsva_scores", "")
		wantEq(t, []any{resp.StatusCode, string(body)}, []any{404, `{"message":"Gene set not found: NOPE"}`})
		return
	}
	post(t, "/api/genesets/GO_ACYLGLYCEROL_HOMEOSTASIS/expression-correlation/fetch?geneticProfileId=study_es_0_gsva_scores&correlationThreshold=0.3", nil, &rows)
	want := map[string]float64{"ERCC5": 0.8, "CREB3L1": 0.8, "MMP2": 0.8, "ZHX3": 0.8, "PNMA1": 0.4}
	if len(rows) != len(want) {
		t.Fatalf("got %v", rows)
	}
	prev := 1.0
	for _, r := range rows {
		sym := r["hugoGeneSymbol"].(string)
		c := r["correlationValue"].(float64)
		wantNear(t, sym, c, want[sym], 1e-9)
		if c > prev+1e-12 {
			t.Errorf("not sorted descending at %s", sym)
		}
		prev = c
		if r["expressionGeneticProfileId"] != "study_es_0_mrna" || r["zScoreGeneticProfileId"] != "study_es_0_mrna_median_Zscores" {
			t.Errorf("profiles %v", r)
		}
	}
}

// ColumnStoreCoExpressionController / ClickhouseCoExpressionMapper: the
// Spearman correlation of every other gene with the query gene over the
// requested samples, with FetchCoExpressionsUseCase's t-distribution p-value.
// Genes with fewer than 3 samples (or constant / low-spread values) are
// listed without a correlation; no query gene or no samples gives [].
func TestContractCoExpressions(t *testing.T) {
	requireAPI(t)
	path := "/api/molecular-profiles/co-expressions/fetch?molecularProfileIdA=study_es_0_mrna&molecularProfileIdB=study_es_0_mrna"
	var rows []map[string]any
	post(t, path+"&threshold=0", map[string]any{"entrezGeneId": 90993, "sampleListId": "study_es_0_all"}, &rows)
	// CREB3L1 against the other genes of data_expression_median.txt, 526 shared
	// samples; rankCorr is 1 - 6*sum(d^2)/(n(n^2-1)) over average ranks.
	want := map[string][2]float64{
		"4313": {0.41031695298013726, 8.872114220282075e-23},
		"2073": {0.12621015927528867, 0.003739893902921717},
		"9240": {0.01429144502152635, 0.7436626957651904},
	}
	seen := 0
	for _, r := range rows {
		if r["geneticEntityType"] != "GENE" {
			t.Errorf("type %v", r)
		}
		w, ok := want[r["geneticEntityId"].(string)]
		if !ok {
			continue
		}
		seen++
		wantNear(t, "r "+r["geneticEntityId"].(string), r["spearmansCorrelation"].(float64), w[0], 1e-12)
		wantNear(t, "p "+r["geneticEntityId"].(string), r["pValue"].(float64), w[1], 1e-6)
	}
	wantEq(t, seen, 3)
	wantEq(t, len(rows), 5)

	rows = nil
	post(t, path, map[string]any{"entrezGeneId": 90993, "sampleListId": "study_es_0_all"}, &rows)
	wantEq(t, ids(rows, "geneticEntityId"), []string{"4313"})

	rows = nil
	post(t, path, map[string]any{"entrezGeneId": 90993, "sampleIds": []string{"TCGA-A1-A0SD-01", "TCGA-A1-A0SE-01"}}, &rows)
	wantEq(t, len(rows), 5)
	for _, r := range rows {
		if _, ok := r["spearmansCorrelation"]; ok {
			t.Errorf("two samples cannot be correlated: %v", r)
		}
	}
	rows = nil
	post(t, path, map[string]any{"sampleIds": []string{"TCGA-A1-A0SD-01"}}, &rows)
	wantEq(t, len(rows), 0)
	rows = nil
	post(t, path, map[string]any{"entrezGeneId": 90993, "sampleIds": []string{}}, &rows)
	wantEq(t, len(rows), 0)
	wantError(t, "POST", path, `{"entrezGeneId":999999999,"sampleIds":["TCGA-A1-A0SD-01"]}`, http.StatusNotFound,
		"Gene not found: 999999999")
}
