package contract

import "testing"

// A patient-level generic assay profile (lgg's HLA, patient_level: true) is
// counted per patient (GenericAssayMapper: count(distinct patient_unique_id))
// over the samples of the filtered patients. Expected counts are the HLA-A
// row of data_HLA.txt: all 23 patients typed, so the NA count is 0.
func TestContractGenericAssayCountsPatientLevel(t *testing.T) {
	requireAPI(t)
	var items []struct {
		StableID string `json:"stableId"`
		Counts   []struct {
			Value string `json:"value"`
			Count int    `json:"count"`
		} `json:"counts"`
	}
	post(t, "/api/generic-assay-data-counts/fetch", map[string]any{
		"genericAssayDataFilters": []map[string]string{{"stableId": "HLA-A", "profileType": "hla"}},
		"studyViewFilter":         map[string]any{"studyIds": []string{"lgg_ucsf_2014_test_generic_assay"}},
	}, &items)
	if len(items) != 1 || items[0].StableID != "HLA-A" {
		t.Fatalf("items %+v", items)
	}
	got := map[string]int{}
	for _, c := range items[0].Counts {
		got[c.Value] = c.Count
	}
	wantEq(t, got, map[string]int{
		"A0101/A0101": 1, "A0101/A0201": 2, "A0101/A0301": 1, "A0101/A2402": 1, "A0101/A2902": 1,
		"A0201/A0201": 1, "A0201/A0301": 1, "A0201/A2301": 1, "A0201/A2402": 1, "A0201/A2601": 2,
		"A0201/A3101": 1, "A0206/A6802": 1, "A0301/A1101": 2, "A0301/A2402": 1, "A0301/A3201": 1,
		"A1101/A2402": 1, "A2402/A2402": 1, "A2402/A2601": 2, "A2902/A3201": 1, "NA": 0,
	})
}
