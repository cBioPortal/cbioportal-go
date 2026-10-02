package contract

import "maps"

import "testing"

// Study-view filter contracts: every StudyViewFilter sub-filter narrows the
// sample set, and a chart's own filter is ignored when computing its counts.

func filteredCount(t *testing.T, filter map[string]any) int {
	t.Helper()
	var d []map[string]any
	post(t, "/api/filtered-samples/fetch", filter, &d)
	return len(d)
}

func es0(extra map[string]any) map[string]any {
	f := map[string]any{"studyIds": []string{"study_es_0"}}
	maps.Copy(f, extra)
	return f
}

func valueCounts(items []map[string]any, attr string) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		if it["attributeId"] != attr {
			continue
		}
		for _, c := range it["counts"].([]any) {
			m := c.(map[string]any)
			out[m["value"].(string)] = int(m["count"].(float64))
		}
	}
	return out
}

var livingFilter = []map[string]any{{"attributeId": "OS_STATUS", "values": []map[string]any{{"value": "0:LIVING"}}}}

func TestContractFilterClinicalCategorical(t *testing.T) {
	requireAPI(t)
	wantEq(t, filteredCount(t, es0(map[string]any{"clinicalDataFilters": livingFilter})), 723)
}

func TestContractFilterClinicalNumericRangesMerged(t *testing.T) {
	requireAPI(t)
	f := es0(map[string]any{"clinicalDataFilters": []map[string]any{{
		"attributeId": "OS_MONTHS",
		"values":      []map[string]any{{"start": 0, "end": 6}, {"start": 6, "end": 12}},
	}}})
	wantEq(t, filteredCount(t, f), 286)
}

func TestContractFilterGeneMutation(t *testing.T) {
	requireAPI(t)
	f := es0(map[string]any{"geneFilters": []map[string]any{{
		"molecularProfileIds": []string{"study_es_0_mutations"},
		"geneQueries":         [][]map[string]any{{{"hugoGeneSymbol": "BRCA1"}}},
	}}})
	wantEq(t, filteredCount(t, f), 4)
}

func TestContractFilterGeneCnaAmp(t *testing.T) {
	requireAPI(t)
	f := es0(map[string]any{"geneFilters": []map[string]any{{
		"molecularProfileIds": []string{"study_es_0_gistic"},
		"geneQueries":         [][]map[string]any{{{"hugoGeneSymbol": "ERCC5", "alterations": []string{"AMP"}}}},
	}}})
	wantEq(t, filteredCount(t, f), 7)
}

func TestContractFilterCaseList(t *testing.T) {
	requireAPI(t)
	wantEq(t, filteredCount(t, es0(map[string]any{"caseLists": [][]string{{"cnaseq"}}})), 788)
}

func TestContractFilterGenomicDataCna(t *testing.T) {
	requireAPI(t)
	f := es0(map[string]any{"genomicDataFilters": []map[string]any{{
		"hugoGeneSymbol": "ERCC5", "profileType": "gistic", "values": []map[string]any{{"value": "2"}},
	}}})
	wantEq(t, filteredCount(t, f), 7)
}

func TestContractClinicalCountsIgnoreOwnFilter(t *testing.T) {
	requireAPI(t)
	var own, other []map[string]any
	post(t, "/api/clinical-data-counts/fetch", map[string]any{
		"attributes":      []map[string]any{{"attributeId": "OS_STATUS"}},
		"studyViewFilter": es0(map[string]any{"clinicalDataFilters": livingFilter}),
	}, &own)
	wantEq(t, valueCounts(own, "OS_STATUS")["1:DECEASED"], 87)
	post(t, "/api/clinical-data-counts/fetch", map[string]any{
		"attributes":      []map[string]any{{"attributeId": "SUBTYPE"}},
		"studyViewFilter": es0(map[string]any{"clinicalDataFilters": livingFilter}),
	}, &other)
	wantEq(t, valueCounts(other, "SUBTYPE")["Luminal A"], 210)
}

func TestContractMutationDataCountsSummary(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/mutation-data-counts/fetch?projection=SUMMARY", map[string]any{
		"genomicDataFilters": []map[string]any{{"hugoGeneSymbol": "BRCA1", "profileType": "mutations"}},
		"studyViewFilter":    es0(nil),
	}, &d)
	if len(d) != 1 {
		t.Fatalf("want 1 item, got %v", d)
	}
	c := d[0]["counts"].([]any)[0].(map[string]any)
	wantEq(t, []any{c["value"], c["count"]}, []any{"MUTATED", 4.0})
}

func TestContractGenomicDataCountsCna(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/genomic-data-counts/fetch", map[string]any{
		"genomicDataFilters": []map[string]any{{"hugoGeneSymbol": "ERCC5", "profileType": "gistic"}},
		"studyViewFilter":    es0(nil),
	}, &d)
	got := map[string]float64{}
	for _, it := range d {
		for _, c := range it["counts"].([]any) {
			m := c.(map[string]any)
			got[m["label"].(string)] = m["count"].(float64)
		}
	}
	wantEq(t, []float64{got["Amplified"], got["Homozygously deleted"]}, []float64{7, 2})
}

func TestContractTreatmentPatientCounts(t *testing.T) {
	requireAPI(t)
	var d struct {
		TotalPatients     int `json:"totalPatients"`
		PatientTreatments []struct {
			Treatment string `json:"treatment"`
			Count     int    `json:"count"`
		} `json:"patientTreatments"`
	}
	post(t, "/api/treatments/patient-counts/fetch", map[string]any{"studyIds": []string{"lgg_ucsf_2014_test_generic_assay"}}, &d)
	counts := map[string]int{}
	for _, p := range d.PatientTreatments {
		counts[p.Treatment] = p.Count
	}
	wantEq(t, []int{d.TotalPatients, counts["TMZ"]}, []int{10, 10})
}

func TestContractClinicalEventTypeCounts(t *testing.T) {
	requireAPI(t)
	var d []struct {
		EventType string `json:"eventType"`
		Count     int    `json:"count"`
	}
	post(t, "/api/clinical-event-type-counts/fetch", map[string]any{"studyIds": []string{"lgg_ucsf_2014_test_generic_assay"}}, &d)
	counts := map[string]int{}
	for _, e := range d {
		counts[e.EventType] = e.Count
	}
	wantEq(t, counts["TREATMENT"], 12)
}

func TestContractCustomDataCountsAndFilter(t *testing.T) {
	requireAPI(t)
	var created struct {
		ID string `json:"id"`
	}
	post(t, "/api/session/custom_data", map[string]any{
		"displayName": "contract custom", "datatype": "STRING", "patientAttribute": false,
		"origin": []string{"study_es_0"},
		"data": []map[string]any{
			{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SB-01", "patientId": "TCGA-A1-A0SB", "value": "red"},
			{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SB-02", "patientId": "TCGA-A1-A0SB", "value": "red"},
			{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SD-01", "patientId": "TCGA-A1-A0SD", "value": "blue"},
		},
	}, &created)
	var counts []map[string]any
	post(t, "/api/custom-data-counts/fetch", map[string]any{
		"attributes":      []map[string]any{{"attributeId": created.ID}},
		"studyViewFilter": es0(nil),
	}, &counts)
	wantEq(t, valueCounts(counts, created.ID), map[string]int{"red": 2, "blue": 1, "NA": 842})
	f := es0(map[string]any{"customDataFilters": []map[string]any{{
		"attributeId": created.ID, "values": []map[string]any{{"value": "red"}},
	}}})
	wantEq(t, filteredCount(t, f), 2)
}

func TestContractClinicalBinsQuartile(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/clinical-data-bin-counts/fetch?dataBinMethod=STATIC", map[string]any{
		"attributes":      []map[string]any{{"attributeId": "OS_MONTHS", "binMethod": "QUARTILE"}},
		"studyViewFilter": es0(nil),
	}, &d)
	var counts []float64
	for _, b := range d {
		counts = append(counts, b["count"].(float64))
	}
	wantEq(t, counts, []float64{202, 202, 203, 201, 33})
}

func TestContractComputedClinicalAttributes(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/clinical-data-counts/fetch", map[string]any{
		"attributes":      []map[string]any{{"attributeId": "SAMPLE_COUNT"}, {"attributeId": "MUTATION_COUNT"}},
		"studyViewFilter": es0(nil),
	}, &d)
	wantEq(t, valueCounts(d, "SAMPLE_COUNT"), map[string]int{"1": 838, "2": 2, "3": 1})
	if len(valueCounts(d, "MUTATION_COUNT")) == 0 {
		t.Errorf("MUTATION_COUNT missing: %v", d)
	}
}
