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
