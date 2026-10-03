package contract

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"
)

// Contracts found by replaying requests against the Java backend on the same
// fixture studies (test/parity, docs/testing.md "Java oracle"). Each case
// also passes against that backend: API=http://localhost:18095 go test ./test/ -run Oracle

// ascn_test_study's segment meta file is named
// ascn_test_study_meta_cna_hg19_seg.txt: the importer's meta file pattern
// matches "_meta_" anywhere in the name, so its segments, the derived
// FRACTION_GENOME_ALTERED and copyNumberSegmentPresent are imported.
func TestOracleMetaFileNamePattern(t *testing.T) {
	requireAPI(t)
	var segs []map[string]any
	get(t, "/api/studies/ascn_test_study/samples/FAKE_P001_S2/copy-number-segments", &segs)
	wantEq(t, len(segs), 44)
	var sample map[string]any
	get(t, "/api/studies/ascn_test_study/samples/FAKE_P001_S2", &sample)
	wantEq(t, sample["copyNumberSegmentPresent"], true)
	var d []map[string]any
	post(t, "/api/clinical-data/fetch?clinicalDataType=SAMPLE", map[string]any{
		"attributeIds": []string{"FRACTION_GENOME_ALTERED"},
		"identifiers":  []map[string]string{{"studyId": "ascn_test_study", "entityId": "FAKE_P001_S2"}},
	}, &d)
	if len(d) != 1 {
		t.Fatalf("FRACTION_GENOME_ALTERED: %v", d)
	}
	wantEq(t, d[0]["value"], "0.8079")
}

// MUTATION_COUNT is computed at import over the importer's sample_profile:
// a sample profiled for mutations only through the *_sequenced case list
// (gene panel matrix NA) gets none.
func TestOracleMutationCountSkipsCaseListOnlySamples(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/clinical-data/fetch?clinicalDataType=SAMPLE", map[string]any{
		"attributeIds": []string{"MUTATION_COUNT"},
		"identifiers": []map[string]string{
			{"studyId": "study_es_0", "entityId": "TEST_SAMPLE_10"},
			{"studyId": "study_es_0", "entityId": "TEST_SAMPLE_1"},
		},
	}, &d)
	if len(d) != 1 || d[0]["sampleId"] != "TEST_SAMPLE_1" || d[0]["value"] != "1" {
		t.Errorf("MUTATION_COUNT rows: %v", d)
	}
}

// A patient_level generic assay profile's values are stored for every sample
// of each patient.
func TestOraclePatientLevelGenericAssayData(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	get(t, "/api/generic-assay-data/study_es_0_generic_assay_patient_test/generic-assay/test_patient_generic_assay_1", &d)
	var got []string
	for _, x := range d {
		if x["patientLevel"] != true {
			t.Errorf("patientLevel not set: %v", x)
		}
		got = append(got, x["patientId"].(string)+"/"+x["sampleId"].(string)+"="+x["value"].(string))
	}
	sort.Strings(got)
	wantEq(t, got, []string{"TCGA-A1-A0SB/TCGA-A1-A0SB-01=0.3703", "TCGA-A1-A0SB/TCGA-A1-A0SB-02=0.3703",
		"TCGA-A1-A0SD/TCGA-A1-A0SD-01=0.0104"})

	d = nil
	post(t, "/api/generic_assay_data/lgg_ucsf_2014_test_generic_assay_hla/fetch",
		map[string]any{"sampleListId": "lgg_ucsf_2014_test_generic_assay_all", "genericAssayStableIds": []string{"HLA-A"}}, &d)
	wantEq(t, len(d), 61)
}

// Generic assay entity properties are the meta file's
// generic_entity_meta_properties, keyed as listed, and those columns are
// not read as samples.
func TestOracleGenericAssayMetaProperties(t *testing.T) {
	requireAPI(t)
	cases := map[string]map[string]string{
		"confidence_1": {"name": "confidence_1", "description": "confidence_1", "confidenceStatement": "NA"},
		"HLA-A":        {"NAME": "HLA-A", "DESCRIPTION": "Polysolver HLA-A"},
		"1p_status":    {"NAME": "1p", "DESCRIPTION": "Copy number status of 1p"},
	}
	for id, want := range cases {
		var d []struct {
			GenericEntityMetaProperties map[string]string `json:"genericEntityMetaProperties"`
		}
		get(t, "/api/generic-assay-meta/generic-assay/"+id, &d)
		if len(d) != 1 {
			t.Fatalf("%s: %v", id, d)
		}
		wantEq(t, d[0].GenericEntityMetaProperties, want)
	}
	var data []map[string]any
	get(t, "/api/generic-assay-data/study_es_0_mutational_signature/generic-assay/mean_1", &data)
	for _, x := range data {
		if x["sampleId"] == "confidenceStatement" {
			t.Errorf("property column read as a sample: %v", x)
		}
	}
	wantEq(t, len(data), 8)
}

func halves(t *testing.T, study, profile string) []map[string]any {
	t.Helper()
	var samples []map[string]any
	get(t, "/api/studies/"+study+"/samples", &samples)
	ids := make([]string, len(samples))
	for i, s := range samples {
		ids[i] = s["sampleId"].(string)
	}
	sort.Strings(ids)
	group := func(name string, ids []string) map[string]any {
		var cs []map[string]string
		for _, id := range ids {
			cs = append(cs, map[string]string{"caseId": id, "molecularProfileId": profile})
		}
		return map[string]any{"name": name, "molecularProfileCaseIdentifiers": cs}
	}
	return []map[string]any{group("a", ids[:len(ids)/2]), group("b", ids[len(ids)/2:])}
}

// Expression enrichments come in the order Java iterates the profile (genes
// by genetic entity id), unsorted and without qValue or cytoband; a profile
// of another alteration type is not found.
func TestOracleExpressionEnrichmentsShape(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/expression-enrichments/fetch?enrichmentType=SAMPLE", halves(t, "study_es_0", "study_es_0_methylation_hm27"), &d)
	var genes []string
	for _, e := range d {
		genes = append(genes, e["hugoGeneSymbol"].(string))
		for _, k := range []string{"qValue", "cytoband"} {
			if _, ok := e[k]; ok {
				t.Errorf("enrichment carries %s: %v", k, e)
			}
		}
	}
	wantEq(t, genes, []string{"HOXD3", "IMPA2", "PANX1", "ATP2A1", "RHOC", "SLMAP", "CCDC88B", "TAF15"})
	body, _ := json.Marshal(halves(t, "study_es_0", "study_es_0_log2CNA"))
	wantError(t, "POST", "/api/expression-enrichments/fetch", string(body), http.StatusNotFound, "Molecular profile not found: study_es_0_log2CNA")
}

// Generic assay (numeric) enrichments: entities in file order (here those
// with two values in both groups), no qValue or name.
func TestOracleGenericAssayEnrichmentsShape(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/generic-assay-enrichments/fetch?enrichmentType=SAMPLE", halves(t, "study_es_0", "study_es_0_treatment_ec50"), &d)
	var ids []string
	for _, e := range d {
		ids = append(ids, e["stableId"].(string))
		for _, k := range []string{"qValue", "name"} {
			if _, ok := e[k]; ok {
				t.Errorf("enrichment carries %s: %v", k, e)
			}
		}
	}
	wantEq(t, ids, []string{"17-AAG", "Irinotecan", "Lapatinib"})
}

// Spring Boot's default error body: epoch-millisecond timestamp.
func TestOracleSpringErrorTimestamp(t *testing.T) {
	requireAPI(t)
	resp, b := rawDo(t, "POST", "/api/no-such-endpoint/fetch", "{}")
	var e map[string]any
	json.Unmarshal(b, &e)
	if _, ok := e["timestamp"].(float64); resp.StatusCode != 404 || !ok {
		t.Errorf("404 body: %d %s", resp.StatusCode, b)
	}
}

// The study view's filtered samples carry the ID fields only.
func TestOracleFilteredSamplesIDFields(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/filtered-samples/fetch", map[string]any{"studyIds": []string{"ascn_test_study"}}, &d)
	if len(d) == 0 {
		t.Fatal("no samples")
	}
	wantEq(t, keys(d[0]), []string{"patientId", "sampleId", "studyId", "uniquePatientKey", "uniqueSampleKey"})
}

// ImportClinicalData prefixes old-format survival statuses of patients.
func TestOracleSurvivalStatusPrefix(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	get(t, "/api/studies/study_hg38/patients/TCGA-A2-A04P/clinical-data", &d)
	got := map[string]any{}
	for _, x := range d {
		got[x["clinicalAttributeId"].(string)] = x["value"]
	}
	wantEq(t, got["DFS_STATUS"], "1:Recurred/Progressed")
}

// Generic assay profiles carry the meta file's pivot threshold, sort order
// and patient level.
func TestOracleMolecularProfileGenericAssayFields(t *testing.T) {
	requireAPI(t)
	var ec50, patient map[string]any
	get(t, "/api/molecular-profiles/study_es_0_treatment_ec50", &ec50)
	wantEq(t, []any{ec50["pivotThreshold"], ec50["sortOrder"], ec50["patientLevel"]}, []any{0.1, "ASC", false})
	get(t, "/api/molecular-profiles/study_es_0_generic_assay_patient_test", &patient)
	wantEq(t, patient["patientLevel"], true)
	var all []map[string]any
	get(t, "/api/studies/lgg_ucsf_2014_test_generic_assay/molecular-profiles", &all)
	for _, p := range all {
		if p["molecularProfileId"] == "lgg_ucsf_2014_test_generic_assay_hla" && p["patientLevel"] != true {
			t.Errorf("hla profile: %v", p)
		}
	}
}

// FRACTION_GENOME_ALTERED reads as Java's Double.toString writes it.
func TestOracleFractionGenomeAlteredFormat(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/clinical-data/fetch?clinicalDataType=SAMPLE", map[string]any{
		"attributeIds": []string{"FRACTION_GENOME_ALTERED"},
		"identifiers": []map[string]string{{"studyId": "teststudy_genepanels", "entityId": "sample1"},
			{"studyId": "teststudy_genepanels", "entityId": "sample2"}},
	}, &d)
	var got []string
	for _, x := range d {
		got = append(got, x["sampleId"].(string)+"="+x["value"].(string))
	}
	sort.Strings(got)
	wantEq(t, got, []string{"sample1=0.0", "sample2=1.0"})
}

// The density plot leaves the correlations unset when no sample has both
// values, and writes an undefined one (constant values) as "NaN"; an empty
// violin plot's axis runs from "Infinity" to "-Infinity" (Jackson's
// non-finite doubles).
func TestOracleNonFiniteDoubles(t *testing.T) {
	requireAPI(t)
	path := "/api/clinical-data-density-plot/fetch?xAxisAttributeId=MUTATION_COUNT&yAxisAttributeId=FRACTION_GENOME_ALTERED&xAxisBinCount=10&yAxisBinCount=10"
	var none map[string]any
	post(t, path, map[string]any{"studyIds": []string{"study_hg38"}}, &none)
	if _, ok := none["pearsonCorr"]; ok {
		t.Errorf("correlation without data: %v", none)
	}
	var constant map[string]any
	post(t, path, map[string]any{"studyIds": []string{"ascn_test_study"}, "clinicalDataFilters": []map[string]any{{
		"attributeId": "MUTATION_COUNT", "values": []map[string]any{{"start": 6, "end": 6}}}}}, &constant)
	wantEq(t, []any{constant["pearsonCorr"], constant["spearmanCorr"]}, []any{"NaN", "NaN"})
	var violin map[string]any
	post(t, "/api/clinical-data-violin-plots/fetch?categoricalAttributeId=CANCER_TYPE_DETAILED&numericalAttributeId=MUTATION_COUNT",
		map[string]any{"studyIds": []string{"ascn_test_study"}}, &violin)
	wantEq(t, []any{violin["axisStart"], violin["axisEnd"]}, []any{"Infinity", "-Infinity"})
}

// Bin counts of a patient-level generic assay profile count patients.
func TestOraclePatientLevelGenericAssayBinsCountPatients(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/generic-assay-data-bin-counts/fetch?dataBinMethod=STATIC", map[string]any{
		"studyViewFilter":            map[string]any{"studyIds": []string{"study_es_0"}},
		"genericAssayDataBinFilters": []map[string]any{{"stableId": "test_patient_generic_assay_1", "profileType": "generic_assay_patient_test"}},
	}, &d)
	total := 0
	var na float64
	for _, b := range d {
		total += int(b["count"].(float64))
		if b["specialValue"] == "NA" {
			na = b["count"].(float64)
		}
	}
	var patients []map[string]any
	get(t, "/api/studies/study_es_0/patients", &patients)
	wantEq(t, []any{total, na}, []any{len(patients), float64(len(patients) - 2)})
}

// The importer normalizes MAF chromosomes (DaoGeneOptimized.normalizeChr):
// X is stored as 23.
func TestOracleMutationChromosomeNormalized(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/mutations/fetch?projection=DETAILED", map[string]any{
		"sampleMolecularIdentifiers": []map[string]string{{"molecularProfileId": "lgg_ucsf_2014_test_generic_assay_mutations", "sampleId": "P01_Pri"}},
		"entrezGeneIds":              []int{546},
	}, &d)
	if len(d) == 0 {
		t.Fatal("no ATRX mutation")
	}
	for _, m := range d {
		wantEq(t, m["chr"], "23")
	}
}

// Discrete CNA driver columns read as CnaUtil reads them: empty is "NA".
func TestOracleCnaDriverColumnsNA(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch?discreteCopyNumberEventType=HOMDEL_AND_AMP&projection=DETAILED",
		map[string]any{"sampleIds": []string{"TCGA-A7-A26F-01"}, "entrezGeneIds": []int{2073, 55210}}, &d)
	got := map[float64][]any{}
	for _, x := range d {
		got[x["entrezGeneId"].(float64)] = []any{x["driverFilter"], x["driverFilterAnnotation"], x["driverTiersFilter"]}
	}
	wantEq(t, got, map[float64][]any{2073: {"Putative_Driver", "NA", "NA"}, 55210: {"NA", "NA", "Class 2"}})
}

// Variant counts come from mutation_count_by_keyword: a gene and keyword
// without mutations has no row.
func TestOracleVariantCountsOnlyExistingKeywords(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/molecular-profiles/study_hg38_mutations/variant-counts/fetch",
		[]map[string]any{{"entrezGeneId": 7157, "keyword": "TP53 truncating"}}, &d)
	wantEq(t, len(d), 0)
}

// Mutation counts by protein position: the importer's positions (parsed
// from the protein change when the MAF has no Protein_position), in request
// order.
func TestOracleMutationCountsByPosition(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/mutation-counts-by-position/fetch", []map[string]int{
		{"entrezGeneId": 7157, "proteinPosStart": 273, "proteinPosEnd": 273},
		{"entrezGeneId": 672, "proteinPosStart": 1800, "proteinPosEnd": 1805},
	}, &d)
	var got [][2]float64
	for _, x := range d {
		got = append(got, [2]float64{x["entrezGeneId"].(float64), x["count"].(float64)})
	}
	wantEq(t, got, [][2]float64{{7157, 9}, {672, 1}})
}

// The violin plot shows the numerical attribute's whole distribution (its
// own filter removed); each row counts the samples of the full filter.
func TestOracleViolinIgnoresOwnNumericalFilter(t *testing.T) {
	requireAPI(t)
	path := "/api/clinical-data-violin-plots/fetch?categoricalAttributeId=CANCER_TYPE_DETAILED&numericalAttributeId=MUTATION_COUNT"
	var all, filtered struct {
		Rows []struct {
			Category   string `json:"category"`
			NumSamples int    `json:"numSamples"`
		} `json:"rows"`
	}
	post(t, path, map[string]any{"studyIds": []string{lggGA}}, &all)
	post(t, path, map[string]any{"studyIds": []string{lggGA}, "clinicalDataFilters": []map[string]any{{
		"attributeId": "MUTATION_COUNT", "values": []map[string]any{{"start": 0, "end": 30}}}}}, &filtered)
	if len(all.Rows) == 0 || len(filtered.Rows) != len(all.Rows) {
		t.Fatalf("rows: all %v, filtered %v", all.Rows, filtered.Rows)
	}
	totalAll, totalFiltered := 0, 0
	for i := range all.Rows {
		totalAll += all.Rows[i].NumSamples
		totalFiltered += filtered.Rows[i].NumSamples
	}
	if totalFiltered == 0 || totalFiltered >= totalAll {
		t.Errorf("numSamples: all %d, filtered %d", totalAll, totalFiltered)
	}
}
