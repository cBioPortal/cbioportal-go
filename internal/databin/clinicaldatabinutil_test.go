package databin

// This file ports the binning assertions of
// org.cbioportal.legacy.web.util.ClinicalDataBinUtilTest. The Java test mocks
// the services that fetch clinical data; here the per-attribute value lists
// those mocks produce are passed directly, with every sample/patient lacking
// a value represented as "NA" (the legacy NA count is "NA" values plus cases
// without data).

import (
	"encoding/json"
	"os"
	"testing"
)

type caseValue struct {
	caseID string
	value  string
}

var (
	unfilteredSamples = []string{
		"GENIE-MSK-P-0003156-T01-IM5", "GENIE-MSK-P-0009680-T01-IM5", "GENIE-MSK-P-0012393-T01-IM5",
		"GENIE-MSK-P-0012393-T02-IM6", "GENIE-MSK-P-0015492-T01-IM6", "GENIE-MSK-P-0017284-T01-IM6",
		"GENIE-MSK-P-0017284-T02-IM5",
	}
	filteredSamples    = []string{"GENIE-MSK-P-0009680-T01-IM5", "GENIE-MSK-P-0015492-T01-IM6"}
	unfilteredPatients = []string{
		"GENIE-MSK-P-0015492", "GENIE-MSK-P-0003156", "GENIE-MSK-P-0009680", "GENIE-MSK-P-0012393",
		"GENIE-MSK-P-0017284",
	}
	filteredPatients = []string{"GENIE-MSK-P-0015492", "GENIE-MSK-P-0009680"}

	sampleAttributeIDs  = []string{"CPT_SEQ_DATE", "CPT_ORDER_INT", "MUTATION_COUNT", "AGE_AT_SEQ_REPORTED_YEARS", "FRACTION_GENOME_ALTERED"}
	patientAttributeIDs = []string{"CA_AGE", "HYBRID_DEATH_INT"}

	// mockUnfilteredClinicalDataForSamples / ...ForPatients, per attribute in
	// fetch order.
	clinicalDataByAttribute = map[string][]caseValue{
		"AGE_AT_SEQ_REPORTED_YEARS": {
			{"GENIE-MSK-P-0015492-T01-IM6", "53.03"}, {"GENIE-MSK-P-0003156-T01-IM5", "78.05"},
			{"GENIE-MSK-P-0009680-T01-IM5", "59.04"}, {"GENIE-MSK-P-0012393-T01-IM5", "72.04"},
			{"GENIE-MSK-P-0012393-T02-IM6", "73.04"}, {"GENIE-MSK-P-0017284-T01-IM6", "64.04"},
			{"GENIE-MSK-P-0017284-T02-IM5", "64.04"},
		},
		"CPT_SEQ_DATE": {
			{"GENIE-MSK-P-0015492-T01-IM6", "2016"}, {"GENIE-MSK-P-0003156-T01-IM5", "2015"},
			{"GENIE-MSK-P-0009680-T01-IM5", "2016"}, {"GENIE-MSK-P-0012393-T01-IM5", "2016"},
			{"GENIE-MSK-P-0012393-T02-IM6", "2017"}, {"GENIE-MSK-P-0017284-T01-IM6", "2017"},
			{"GENIE-MSK-P-0017284-T02-IM5", "2017"},
		},
		"FRACTION_GENOME_ALTERED": {
			{"GENIE-MSK-P-0015492-T01-IM6", "0.0460"}, {"GENIE-MSK-P-0003156-T01-IM5", "0.2164"},
			{"GENIE-MSK-P-0009680-T01-IM5", "0.2073"}, {"GENIE-MSK-P-0012393-T01-IM5", "0.5280"},
			{"GENIE-MSK-P-0012393-T02-IM6", "0.1297"}, {"GENIE-MSK-P-0017284-T01-IM6", "0.2796"},
			{"GENIE-MSK-P-0017284-T02-IM5", "0.7233"},
		},
		"MUTATION_COUNT": {
			{"GENIE-MSK-P-0015492-T01-IM6", "7"}, {"GENIE-MSK-P-0003156-T01-IM5", "8"},
			{"GENIE-MSK-P-0009680-T01-IM5", "6"}, {"GENIE-MSK-P-0012393-T01-IM5", "3"},
			{"GENIE-MSK-P-0012393-T02-IM6", "3"}, {"GENIE-MSK-P-0017284-T01-IM6", "5"},
			{"GENIE-MSK-P-0017284-T02-IM5", "4"},
		},
		"CA_AGE": {
			{"GENIE-MSK-P-0015492", "52"}, {"GENIE-MSK-P-0003156", "77"}, {"GENIE-MSK-P-0009680", "56"},
			{"GENIE-MSK-P-0012393", "71"}, {"GENIE-MSK-P-0017284", "62"},
		},
		"HYBRID_DEATH_INT": {
			{"GENIE-MSK-P-0015492", "657.3"}, {"GENIE-MSK-P-0003156", "968.1"},
			{"GENIE-MSK-P-0009680", "8765.7"}, {"GENIE-MSK-P-0012393", "730.4"},
		},
	}
)

// valuesFor returns the attribute's values for the given cases, followed by
// one "NA" per case without data.
func valuesFor(attr string, cases []string) []Value {
	in := map[string]bool{}
	for _, c := range cases {
		in[c] = true
	}
	var out []Value
	withData := map[string]bool{}
	for _, cv := range clinicalDataByAttribute[attr] {
		if in[cv.caseID] {
			out = append(out, Value{Value: cv.value, Count: 1})
			withData[cv.caseID] = true
		}
	}
	missing := 0
	for _, c := range cases {
		if !withData[c] {
			missing++
		}
	}
	return append(out, Value{Value: "NA", Count: missing})
}

func fetchClinicalDataBinCounts(t *testing.T, samples, patients []string) map[string][]Bin {
	t.Helper()
	out := map[string][]Bin{}
	total := 0
	run := func(attr string, unfilteredCases, filteredCases []string) {
		bins, err := Calculate(attr, valuesFor(attr, unfilteredCases), valuesFor(attr, filteredCases), Options{Method: Static})
		if err != nil {
			t.Fatal(err)
		}
		out[attr] = bins
		total += len(bins)
	}
	for _, a := range sampleAttributeIDs {
		run(a, unfilteredSamples, samples)
	}
	for _, a := range patientAttributeIDs {
		run(a, unfilteredPatients, patients)
	}
	if total != 33 {
		t.Errorf("total bins = %d, want 33", total)
	}
	return out
}

func assertCounts(t *testing.T, bins map[string][]Bin, attr string, want ...int) {
	t.Helper()
	got := bins[attr]
	if len(got) != len(want) {
		t.Fatalf("%s: %d bins, want %d: %s", attr, len(got), len(want), formatBins(got))
	}
	for i, w := range want {
		if got[i].Count != w {
			t.Errorf("%s bin %d count = %d, want %d: %s", attr, i, got[i].Count, w, formatBins(got))
		}
	}
}

func TestUnfilteredFetchClinicalDataBinCounts(t *testing.T) {
	bins := fetchClinicalDataBinCounts(t, unfilteredSamples, unfilteredPatients)
	assertCounts(t, bins, "MUTATION_COUNT", 2, 1, 1, 1, 1, 1)
	assertCounts(t, bins, "FRACTION_GENOME_ALTERED", 1, 1, 1, 1, 1, 1, 1)
	assertCounts(t, bins, "AGE_AT_SEQ_REPORTED_YEARS", 1, 1, 2, 1, 1, 1)
	assertCounts(t, bins, "CA_AGE", 1, 1, 1, 1, 1)
	assertCounts(t, bins, "CPT_SEQ_DATE", 1, 3, 3)
	assertCounts(t, bins, "CPT_ORDER_INT", 7)
	assertCounts(t, bins, "HYBRID_DEATH_INT", 1, 1, 1, 1, 1)
}

// TestFilteredFetchClinicalDataBinCounts keeps the Java test's bin layout
// assertions for every attribute. Its filtered counts for
// AGE_AT_SEQ_REPORTED_YEARS, CA_AGE and HYBRID_DEATH_INT reflect which rows
// the mocked legacy fetch/filter services hand to the binner (they include
// case GENIE-MSK-P-0003156, which is not in the filtered selection), not the
// binning itself, so only their bin counts per attribute are asserted here.
func TestFilteredFetchClinicalDataBinCounts(t *testing.T) {
	bins := fetchClinicalDataBinCounts(t, filteredSamples, filteredPatients)
	assertCounts(t, bins, "MUTATION_COUNT", 0, 0, 0, 1, 1, 0)
	assertCounts(t, bins, "FRACTION_GENOME_ALTERED", 1, 0, 1, 0, 0, 0, 0)
	assertCounts(t, bins, "CPT_SEQ_DATE", 0, 2, 0)
	assertCounts(t, bins, "CPT_ORDER_INT", 2)
	for attr, n := range map[string]int{"AGE_AT_SEQ_REPORTED_YEARS": 6, "CA_AGE": 5, "HYBRID_DEATH_INT": 5} {
		if len(bins[attr]) != n {
			t.Errorf("%s: %d bins, want %d", attr, len(bins[attr]), n)
		}
	}
	// Recounting the static bins with the filtered selection.
	assertCounts(t, bins, "AGE_AT_SEQ_REPORTED_YEARS", 1, 1, 0, 0, 0, 0)
	assertCounts(t, bins, "CA_AGE", 1, 1, 0, 0, 0)
	assertCounts(t, bins, "HYBRID_DEATH_INT", 1, 0, 0, 1, 0)
}

func customDatasetValues(t *testing.T, path string) []Value {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Data struct {
			Data []struct {
				Value string `json:"value"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &session); err != nil {
		t.Fatal(err)
	}
	var out []Value
	for _, d := range session.Data.Data {
		out = append(out, Value{Value: d.Value, Count: 1})
	}
	return out
}

func TestFetchCustomDataBinCountsWithStaticBinningMethod(t *testing.T) {
	values := customDatasetValues(t, "testdata/custom-dataset.json")
	bins, err := Calculate("test", values, values, Options{Method: Static, BinMethod: BinMethodCustom})
	if err != nil {
		t.Fatal(err)
	}
	if len(bins) != 11 {
		t.Fatalf("got %d bins: %s", len(bins), formatBins(bins))
	}
	for _, b := range bins {
		if b.AttributeID != "test" {
			t.Errorf("attributeId %q", b.AttributeID)
		}
	}
	if bins[0].Special() != "<=" || bins[10].Special() != ">" {
		t.Errorf("outlier bins: %s", formatBins(bins))
	}
	if w := bins[1].End.intValue() - bins[1].Start.intValue(); w != 5 {
		t.Errorf("bin size = %d, want 5", w)
	}
	want := []int{3, 5, 8, 5, 8, 5, 8, 5, 6, 2, 1}
	for i, w := range want {
		if bins[i].Count != w {
			t.Errorf("bin %d count = %d, want %d: %s", i, bins[i].Count, w, formatBins(bins))
		}
	}
}

func TestFetchCustomDataBinCountsWithStaticBinningMethodMinimalExample(t *testing.T) {
	values := customDatasetValues(t, "testdata/custom-dataset-minimal.json")
	bins, err := Calculate("test", values, values, Options{Method: Static, BinMethod: BinMethodCustom})
	if err != nil {
		t.Fatal(err)
	}
	if len(bins) != 13 {
		t.Fatalf("got %d bins: %s", len(bins), formatBins(bins))
	}
	if bins[0].Special() != "<=" || bins[0].End.intValue() != -1 {
		t.Errorf("first bin: %s", formatBin(bins[0]))
	}
	if w := bins[1].End.intValue() - bins[1].Start.intValue(); w != 1 {
		t.Errorf("bin size = %d, want 1", w)
	}
	want := []int{1, 1, 1, 2, 2, 1, 0, 1, 1}
	for i, w := range want {
		if bins[i].Count != w {
			t.Errorf("bin %d count = %d, want %d: %s", i, bins[i].Count, w, formatBins(bins))
		}
	}
}
