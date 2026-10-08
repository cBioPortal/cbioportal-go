package contract

import "testing"

// Contracts from replaying production's logged requests (rewrite/java-v7-to-go/loggedgen).

// Discrete copy-number counts report the samples of the profile's matrix
// (genetic_profile_samples): the data file's samples, for a wide matrix and
// for a discrete-long file alike.
func TestContractDiscreteCopyNumberCountSamples(t *testing.T) {
	requireAPI(t)
	for profile, want := range map[string]float64{
		"ascn_test_study_cna": 6,   // wide matrix: its header's samples
		"study_es_0_gistic":   788, // discrete-long: the file's samples
	} {
		var d []map[string]any
		post(t, "/api/molecular-profiles/"+profile+"/discrete-copy-number-counts/fetch",
			[]map[string]any{{"entrezGeneId": 7157, "alteration": -2}}, &d)
		if len(d) != 1 {
			t.Fatalf("%s: %v", profile, d)
		}
		wantEq(t, d[0]["numberOfSamples"], want)
	}
}

// Variant counts: numberOfSamples is the size of the study's sequenced list;
// an identifier without a keyword has no row (production's
// mutation_count_by_keyword stores a missing keyword as the text "null",
// which `keyword IS NULL` does not match), a repeated identifier one row,
// and a profile that is not a mutation profile is not found.
func TestContractVariantCountsRows(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_mutations/variant-counts/fetch", []map[string]any{
		{"entrezGeneId": 7157, "keyword": "TP53 truncating"},
		{"entrezGeneId": 7157, "keyword": "TP53 truncating"},
		{"entrezGeneId": 7157},
	}, &d)
	if len(d) != 1 {
		t.Fatalf("variant counts: %v", d)
	}
	wantEq(t, d[0]["keyword"], "TP53 truncating")
	wantEq(t, d[0]["numberOfSamples"], float64(31))

	resp, body := rawDo(t, "POST", "/api/molecular-profiles/study_es_0_gistic/variant-counts/fetch",
		`[{"entrezGeneId":7157,"keyword":"TP53 truncating"}]`)
	if resp.StatusCode != 404 {
		t.Errorf("CNA profile: %d %s", resp.StatusCode, body)
	}
}

// With sample ids, mutation-data-counts answers one item per gene and
// profile type as the mapper's result map groups its rows: the mutation
// types under "mutations", then "Not Mutated" and "Not Profiled" items of
// their own, present (with count and uniqueCount 0) even when empty.
func TestContractMutationDataCountsWithSampleIDs(t *testing.T) {
	requireAPI(t)
	type count struct {
		Value       string   `json:"value"`
		Count       int      `json:"count"`
		UniqueCount *int     `json:"uniqueCount"`
		SampleIDs   []string `json:"sampleIds"`
	}
	var d []struct {
		HugoGeneSymbol string  `json:"hugoGeneSymbol"`
		ProfileType    string  `json:"profileType"`
		Counts         []count `json:"counts"`
	}
	post(t, "/api/mutation-data-counts/fetch?projection=DETAILED&includeSampleIds=true", map[string]any{
		"genomicDataFilters": []map[string]string{{"hugoGeneSymbol": "TP53", "profileType": "mutations"}},
		"studyViewFilter":    map[string]any{"studyIds": []string{"study_es_0"}},
	}, &d)
	types := map[string][]count{}
	for _, it := range d {
		wantEq(t, it.HugoGeneSymbol, "TP53")
		types[it.ProfileType] = it.Counts
	}
	if len(d) != 3 || len(types["mutations"]) == 0 || len(types["Not Mutated"]) != 1 || len(types["Not Profiled"]) != 1 {
		t.Fatalf("items: %+v", d)
	}
	wantEq(t, types["Not Mutated"][0].Value, "NOT_MUTATED")
	np := types["Not Profiled"][0]
	if np.Value != "NOT_PROFILED" || np.UniqueCount == nil || *np.UniqueCount != np.Count || len(np.SampleIDs) != np.Count {
		t.Errorf("not profiled: %+v", np)
	}

	// A study whose samples are all profiled: the not-profiled item counts 0.
	d = nil
	post(t, "/api/mutation-data-counts/fetch?projection=DETAILED&includeSampleIds=true", map[string]any{
		"genomicDataFilters": []map[string]string{{"hugoGeneSymbol": "TP53", "profileType": "mutations"}},
		"studyViewFilter":    map[string]any{"studyIds": []string{"study_hg38"}},
	}, &d)
	for _, it := range d {
		if it.ProfileType == "Not Profiled" {
			c := it.Counts[0]
			if c.Count != 0 || c.UniqueCount == nil || *c.UniqueCount != 0 || c.SampleIDs != nil {
				t.Errorf("empty not profiled: %+v", c)
			}
			return
		}
	}
	t.Errorf("no Not Profiled item: %+v", d)
}
