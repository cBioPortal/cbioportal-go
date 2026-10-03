package contract

import (
	"testing"
)

// Bulk list and keyword-search contracts. Expected rows come from the fixture
// studies' clinical files; orderings from the Java mappers.

// ClickhouseSampleMapper: every space-separated keyword term must occur in the
// sample id (ILIKE); ids starting with the whole keyword come first, then by id.
func TestContractSamplesByKeyword(t *testing.T) {
	requireAPI(t)
	var rows []map[string]any
	get(t, "/api/samples?keyword=sample&projection=ID", &rows)
	want := []string{"sample1", "sample2", "sample3", "sample4", "sample5",
		"TEST_SAMPLE_1", "TEST_SAMPLE_10", "TEST_SAMPLE_11", "TEST_SAMPLE_12", "TEST_SAMPLE_13", "TEST_SAMPLE_14",
		"TEST_SAMPLE_15", "TEST_SAMPLE_2", "TEST_SAMPLE_3", "TEST_SAMPLE_4", "TEST_SAMPLE_5", "TEST_SAMPLE_6",
		"TEST_SAMPLE_7", "TEST_SAMPLE_8", "TEST_SAMPLE_9",
		"TEST_SAMPLE_SOMATIC_HETEROZYGOUS", "TEST_SAMPLE_SOMATIC_HOMOZYGOUS", "TEST_SAMPLE_SOMATIC_UNDEFINED"}
	wantEq(t, ids(rows, "sampleId"), want)

	// Terms are ANDed; patient ids do not match.
	get(t, "/api/samples?keyword=a0sb%2002", &rows)
	wantEq(t, ids(rows, "sampleId"), []string{"TCGA-A1-A0SB-02"})
	get(t, "/api/samples?keyword=patientA", &rows)
	wantEq(t, len(rows), 0)
}

// PatientMapper joins patients to their samples for a keyword search, so a
// patient is listed once per sample that matches (here both samples of
// TCGA-A1-A0SB match through the patient id); a term may also match a sample id.
func TestContractPatientsByKeyword(t *testing.T) {
	requireAPI(t)
	var rows []map[string]any
	get(t, "/api/patients?keyword=A0SB", &rows)
	wantEq(t, ids(rows, "patientId"), []string{"TCGA-A1-A0SB", "TCGA-A1-A0SB"})
	get(t, "/api/patients?keyword=A0SB-02", &rows)
	wantEq(t, ids(rows, "patientId"), []string{"TCGA-A1-A0SB"})
}

// ClinicalAttributeMapper.getClinicalAttributes orders by attribute id, then
// study, for the all-studies listing and the multi-study fetch alike.
func TestContractClinicalAttributesOrder(t *testing.T) {
	requireAPI(t)
	check := func(name string, rows []map[string]any) {
		t.Helper()
		if len(rows) == 0 {
			t.Fatalf("%s: no attributes", name)
		}
		for i := 1; i < len(rows); i++ {
			a, b := rows[i-1]["clinicalAttributeId"].(string), rows[i]["clinicalAttributeId"].(string)
			if a > b {
				t.Fatalf("%s: %s listed before %s", name, a, b)
			}
		}
	}
	var all []map[string]any
	get(t, "/api/clinical-attributes", &all)
	check("GET /api/clinical-attributes", all)
	var fetched []map[string]any
	post(t, "/api/clinical-attributes/fetch", []string{"study_es_0", "lgg_ucsf_2014_test_generic_assay"}, &fetched)
	check("POST /api/clinical-attributes/fetch", fetched)
}

// ClinicalAttributeMapper.getClinicalAttributeCountsBySampleIds counts rows:
// a patient attribute counts once per selected sample of the patient. Of
// TCGA-A1-A0SB's patient attributes (OS_STATUS, OS_MONTHS, DFS_STATUS,
// DFS_MONTHS) each counts 2 with both its samples selected; its SUBTYPE is NA,
// which the importer does not store.
func TestContractClinicalAttributeCountsPerSample(t *testing.T) {
	requireAPI(t)
	count := func(samples ...string) map[string]float64 {
		ids := make([]map[string]string, len(samples))
		for i, s := range samples {
			ids[i] = map[string]string{"studyId": "study_es_0", "sampleId": s}
		}
		var rows []map[string]any
		post(t, "/api/clinical-attributes/counts/fetch", map[string]any{"sampleIdentifiers": ids}, &rows)
		out := map[string]float64{}
		for _, r := range rows {
			out[r["clinicalAttributeId"].(string)] = r["count"].(float64)
		}
		return out
	}
	both := count("TCGA-A1-A0SB-01", "TCGA-A1-A0SB-02")
	one := count("TCGA-A1-A0SB-01")
	for _, a := range []string{"OS_STATUS", "OS_MONTHS", "DFS_STATUS", "DFS_MONTHS"} {
		wantEq(t, []float64{both[a], one[a]}, []float64{2, 1})
	}
	if _, ok := both["SUBTYPE"]; ok {
		t.Errorf("SUBTYPE (NA for these samples) counted: %v", both)
	}
}
