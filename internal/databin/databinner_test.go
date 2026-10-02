package databin

// This file ports org.cbioportal.legacy.web.util.DataBinnerTest: every Java
// test method (and every result list asserted within one) is a table entry
// with the same fixture data, the same filter settings and the same
// assertions. The legacy Java tests pass the case ids of samples/patients
// without data; here those cases are passed as "NA" values (missingCases).

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type check struct {
	bin   int
	field string
	want  string
}

type expectedBin struct {
	special, start, end *string
	count               int
}

func sp(s string) *string { return &s }

func dec(s string) *Decimal { return decPtr(MustParseDecimal(s)) }

func decs(s ...string) []Decimal {
	out := make([]Decimal, len(s))
	for i, v := range s {
		out[i] = MustParseDecimal(v)
	}
	return out
}

func loadMockData(t *testing.T) map[string][]string {
	t.Helper()
	b, err := os.ReadFile("testdata/mockdata.json")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string][]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func perCase(values []string) []Value {
	out := make([]Value, len(values))
	for i, v := range values {
		out[i] = Value{Value: v, Count: 1}
	}
	return out
}

// round mirrors BigDecimal.round(new MathContext(precision, mode)).
func round(d Decimal, precision int, mode roundingMode) Decimal {
	drop := d.precision() - precision
	if drop <= 0 {
		return d
	}
	return d.setScale(d.scale-int32(drop), mode)
}

func TestDataBinnerJavaPort(t *testing.T) {
	mock := loadMockData(t)
	cases := []struct {
		name         string
		mock         string
		attr         string
		missingCases int
		static       bool
		filteredTo   int
		opts         Options
		checks       []check
		expected     []expectedBin
	}{

		{
			name: "LinearDataBinner",
			mock: "blca_tcga_AGE",
			attr: "AGE",
			checks: []check{
				{-1, "size", "11"},
				{0, "special", "<="},
				{0, "end", "40.0"},
				{0, "count", "2"},
				{1, "start", "40.0"},
				{1, "end", "45.0"},
				{1, "count", "6"},
				{2, "start", "45.0"},
				{2, "end", "50.0"},
				{2, "count", "17"},
				{3, "start", "50.0"},
				{3, "end", "55.0"},
				{3, "count", "24"},
				{4, "start", "55.0"},
				{4, "end", "60.0"},
				{4, "count", "59"},
				{5, "start", "60.0"},
				{5, "end", "65.0"},
				{5, "count", "53"},
				{6, "start", "65.0"},
				{6, "end", "70.0"},
				{6, "count", "71"},
				{7, "start", "70.0"},
				{7, "end", "75.0"},
				{7, "count", "67"},
				{8, "start", "75.0"},
				{8, "end", "80.0"},
				{8, "count", "64"},
				{9, "start", "80.0"},
				{9, "end", "85.0"},
				{9, "count", "35"},
				{10, "start", "85.0"},
				{10, "end", "90.0"},
				{10, "count", "13"},
			},
		},
		{
			name: "LinearDataBinnerWithRange",
			mock: "linear_integer_continuous",
			attr: "random",
			opts: Options{Start: dec("39.5"), End: dec("80.5")},
			checks: []check{
				{-1, "size", "8"},
				{0, "special", "<="},
				{0, "end", "45.0"},
				{0, "count", "6"},
				{1, "start", "45.0"},
				{1, "end", "50.0"},
				{1, "count", "5"},
				{2, "start", "50.0"},
				{2, "end", "55.0"},
				{2, "count", "5"},
				{7, "start", "75.0"},
				{7, "end", "80.0"},
				{7, "count", "5"},
			},
		},
		{
			name: "LinearDataBinnerWithRangeOne",
			mock: "linear_integer_continuous",
			attr: "random",
			opts: Options{Start: dec("39.5"), End: dec("80.5")},
			checks: []check{
				{-1, "size", "8"},
				{0, "special", "<="},
				{0, "end", "45.0"},
				{0, "count", "6"},
				{1, "start", "45.0"},
				{1, "end", "50.0"},
				{1, "count", "5"},
				{2, "start", "50.0"},
				{2, "end", "55.0"},
				{2, "count", "5"},
				{7, "start", "75.0"},
				{7, "end", "80.0"},
				{7, "count", "5"},
			},
		},
		{
			name: "LinearDataBinnerWithRangeTwo",
			mock: "linear_integer_continuous",
			attr: "random",
			opts: Options{Start: dec("39.5"), End: dec("81.5")},
			checks: []check{
				{-1, "size", "9"},
				{0, "special", "<="},
				{0, "end", "45.0"},
				{0, "count", "6"},
				{1, "start", "45.0"},
				{1, "end", "50.0"},
				{1, "count", "5"},
				{2, "start", "50.0"},
				{2, "end", "55.0"},
				{2, "count", "5"},
				{7, "start", "75.0"},
				{7, "end", "80.0"},
				{7, "count", "5"},
				{8, "start", "80.0"},
				{8, "special", ">"},
				{8, "count", "1"},
			},
		},
		{
			name: "LinearDataBinnerWithRangeAndCustomBins",
			mock: "linear_integer_continuous",
			attr: "random",
			opts: Options{Start: dec("39.5"), End: dec("81.5"), BinMethod: BinMethodCustom, CustomBins: decs("50.0", "60.0", "70.0")},
			checks: []check{
				{-1, "size", "4"},
				{0, "special", "<="},
				{0, "end", "50.0"},
				{0, "count", "11"},
				{1, "start", "50.0"},
				{1, "end", "60.0"},
				{1, "count", "10"},
				{2, "start", "60.0"},
				{2, "end", "70.0"},
				{2, "count", "10"},
				{3, "start", "70.0"},
				{3, "special", ">"},
				{3, "count", "11"},
			},
		},
		{
			name: "StaticDataBinnerFilterUnfiltered",
			mock: "blca_tcga_AGE",
			attr: "AGE",
			checks: []check{
				{-1, "size", "11"},
				{0, "special", "<="},
				{0, "end", "40.0"},
				{1, "start", "40.0"},
				{1, "end", "45.0"},
				{2, "start", "45.0"},
				{2, "end", "50.0"},
				{3, "start", "50.0"},
				{3, "end", "55.0"},
				{4, "start", "55.0"},
				{4, "end", "60.0"},
				{5, "start", "60.0"},
				{5, "end", "65.0"},
				{10, "start", "85.0"},
				{10, "end", "90.0"},
				{0, "count", "2"},
				{1, "count", "6"},
				{2, "count", "17"},
				{3, "count", "24"},
				{4, "count", "59"},
				{5, "count", "53"},
				{6, "count", "71"},
				{10, "count", "13"},
			},
		},
		{
			name:       "StaticDataBinnerFilterFiltered",
			mock:       "blca_tcga_AGE",
			attr:       "AGE",
			static:     true,
			filteredTo: 108,
			checks: []check{
				{-1, "size", "11"},
				{0, "special", "<="},
				{0, "end", "40.0"},
				{1, "start", "40.0"},
				{1, "end", "45.0"},
				{2, "start", "45.0"},
				{2, "end", "50.0"},
				{3, "start", "50.0"},
				{3, "end", "55.0"},
				{4, "start", "55.0"},
				{4, "end", "60.0"},
				{5, "start", "60.0"},
				{5, "end", "65.0"},
				{10, "start", "85.0"},
				{10, "end", "90.0"},
				{0, "count", "2"},
				{1, "count", "6"},
				{2, "count", "17"},
				{3, "count", "24"},
				{4, "count", "59"},
				{5, "count", "0"},
				{6, "count", "0"},
				{10, "count", "0"},
			},
		},
		{
			name: "LinearDataBinnerWithNumberOfPetOrPetCtScans",
			mock: "genie_N_SCANS_PET_CT_PT",
			attr: "N_SCANS_PET_CT_PT",
			checks: []check{
				{-1, "size", "4"},
				{0, "special", "<="},
				{0, "endInt", "1"},
				{1, "startInt", "2"},
				{1, "endInt", "2"},
				{2, "startInt", "3"},
				{2, "endInt", "3"},
				{3, "special", ">"},
				{3, "startInt", "3"},
			},
		},
		{
			name: "LinearDataBinnerWithNumberOfBoneScansDefaultThreshold",
			mock: "genie_N_SCANS_BONE_PT",
			attr: "N_SCANS_BONE_PT",
			checks: []check{
				{-1, "size", "7"},
				{0, "startInt", "0"},
				{0, "endInt", "0"},
				{6, "startInt", "12"},
				{6, "endInt", "12"},
			},
		},
		{
			name: "LinearDataBinnerWithNumberOfBoneScansThreshold5",
			mock: "genie_N_SCANS_BONE_PT",
			attr: "N_SCANS_BONE_PT",
			opts: Options{DistinctValueThreshold: 5},
			checks: []check{
				{-1, "size", "2"},
				{0, "startInt", "0"},
				{0, "endInt", "0"},
				{1, "special", ">"},
				{1, "startInt", "0"},
				{1, "endNil", ""},
			},
		},
		{
			name: "LinearDataBinnerWithAgeAtWhichSequencingWasReported1",
			mock: "genie_public_AGE_AT_WHICH_SEQUENCING_WAS_REPORTED",
			attr: "AGE_AT_WHICH_SEQUENCING_WAS_REPORTED",
			opts: Options{BinMethod: BinMethodCustom, CustomBins: decs("45.0", "60.0", "70.0")},
			checks: []check{
				{-1, "size", "5"},
				{0, "special", "<="},
				{0, "end", "45.0"},
				{0, "count", "337"},
				{1, "start", "45.0"},
				{1, "end", "60.0"},
				{1, "count", "118"},
				{2, "start", "60.0"},
				{2, "end", "70.0"},
				{2, "count", "64"},
				{3, "special", ">"},
				{3, "start", "70.0"},
				{3, "count", "223"},
				{4, "special", "Unknown"},
				{4, "count", "10"},
			},
		},
		{
			name: "LinearDataBinnerWithAgeAtWhichSequencingWasReported2",
			mock: "genie_public_AGE_AT_WHICH_SEQUENCING_WAS_REPORTED",
			attr: "AGE_AT_WHICH_SEQUENCING_WAS_REPORTED",
			opts: Options{BinMethod: BinMethodCustom, CustomBins: decs("40.0", "55.0")},
			checks: []check{
				{-1, "size", "4"},
				{0, "special", "<="},
				{0, "end", "40.0"},
				{0, "count", "305"},
				{1, "start", "40.0"},
				{1, "end", "55.0"},
				{1, "count", "112"},
				{2, "special", ">"},
				{2, "start", "55.0"},
				{2, "count", "325"},
				{3, "special", "Unknown"},
				{3, "count", "10"},
			},
		},
		{
			name: "LinearDataBinnerWithAgeAtWhichSequencingWasReported3",
			mock: "genie_public_AGE_AT_WHICH_SEQUENCING_WAS_REPORTED",
			attr: "AGE_AT_WHICH_SEQUENCING_WAS_REPORTED",
			opts: Options{BinMethod: BinMethodCustom, CustomBins: decs("5.0", "10.0", "90.0", "100.0")},
			checks: []check{
				{-1, "size", "4"},
				{0, "special", "<="},
				{0, "end", "18"},
				{0, "count", "156"},
				{1, "start", "18"},
				{1, "end", "89"},
				{1, "count", "569"},
				{2, "special", ">"},
				{2, "start", "89"},
				{2, "count", "17"},
				{3, "special", "Unknown"},
				{3, "count", "10"},
			},
		},
		{
			name:         "LinearDataBinnerWithPediatricAge",
			mock:         "skcm_broad_AGE_AT_PROCUREMENT",
			attr:         "AGE_AT_PROCUREMENT",
			missingCases: 4,
			opts:         Options{BinMethod: BinMethodCustom, CustomBins: decs("18.0", "20.0", "30.0", "40.0", "50.0", "60.0", "70.0", "80.0", "90.0")},
			checks: []check{
				{-1, "size", "10"},
				{0, "special", "<="},
				{0, "end", "18.0"},
				{0, "count", "1"},
				{1, "start", "18.0"},
				{1, "end", "20.0"},
				{1, "count", "0"},
				{2, "start", "20.0"},
				{2, "end", "30.0"},
				{2, "count", "9"},
				{3, "start", "30.0"},
				{3, "end", "40.0"},
				{3, "count", "16"},
				{4, "start", "40.0"},
				{4, "end", "50.0"},
				{4, "count", "31"},
				{5, "start", "50.0"},
				{5, "end", "60.0"},
				{5, "count", "25"},
				{6, "start", "60.0"},
				{6, "end", "70.0"},
				{6, "count", "24"},
				{7, "start", "70.0"},
				{7, "end", "80.0"},
				{7, "count", "11"},
				{8, "start", "80.0"},
				{8, "end", "90.0"},
				{8, "count", "2"},
				{9, "special", "NA"},
				{9, "count", "4"},
			},
		},
		{
			name:         "LinearDataBinnerWithPediatricAgeCustomBinsTest1",
			mock:         "skcm_broad_AGE_AT_PROCUREMENT",
			attr:         "AGE_AT_PROCUREMENT",
			missingCases: 4,
			opts:         Options{BinMethod: BinMethodCustom, CustomBins: decs("18.0", "20.0", "30.0", "40.0", "50.0", "60.0", "70.0", "80.0", "90.0")},
			expected: []expectedBin{
				{sp("<="), nil, sp("18.0"), 1},
				{nil, sp("18.0"), sp("20.0"), 0},
				{nil, sp("20.0"), sp("30.0"), 9},
				{nil, sp("30.0"), sp("40.0"), 16},
				{nil, sp("40.0"), sp("50.0"), 31},
				{nil, sp("50.0"), sp("60.0"), 25},
				{nil, sp("60.0"), sp("70.0"), 24},
				{nil, sp("70.0"), sp("80.0"), 11},
				{nil, sp("80.0"), sp("90.0"), 2},
				{sp("NA"), nil, nil, 4},
			},
		},
		{
			name:         "LinearDataBinnerWithPediatricAgeCustomBinsTest2",
			mock:         "skcm_broad_AGE_AT_PROCUREMENT",
			attr:         "AGE_AT_PROCUREMENT",
			missingCases: 4,
			opts:         Options{BinMethod: BinMethodCustom, CustomBins: decs("18.0", "25.0", "30.0", "35.0")},
			expected: []expectedBin{
				{sp("<="), nil, sp("18.0"), 1},
				{nil, sp("18.0"), sp("25.0"), 2},
				{nil, sp("25.0"), sp("30.0"), 7},
				{nil, sp("30.0"), sp("35.0"), 7},
				{sp(">"), sp("35.0"), nil, 102},
				{sp("NA"), nil, nil, 4},
			},
		},
		{
			name:         "LinearDataBinnerWithPediatricAgeCustomBinsSingleBoundary",
			mock:         "skcm_broad_AGE_AT_PROCUREMENT",
			attr:         "AGE_AT_PROCUREMENT",
			missingCases: 4,
			opts:         Options{BinMethod: BinMethodCustom, CustomBins: decs("30.0")},
			expected: []expectedBin{
				{sp("<="), nil, sp("30.0"), 10},
				{sp(">"), sp("30.0"), nil, 109},
				{sp("NA"), nil, nil, 4},
			},
		},
		{
			name:         "LinearDataBinnerWithPediatricAgeCustomBinsTwoBoundaries",
			mock:         "skcm_broad_AGE_AT_PROCUREMENT",
			attr:         "AGE_AT_PROCUREMENT",
			missingCases: 4,
			opts:         Options{BinMethod: BinMethodCustom, CustomBins: decs("20.0", "50.0")},
			expected: []expectedBin{
				{sp("<="), nil, sp("20.0"), 1},
				{nil, sp("20.0"), sp("50.0"), 56},
				{sp(">"), sp("50.0"), nil, 62},
				{sp("NA"), nil, nil, 4},
			},
		},
		{
			name:         "LinearDataBinnerWithPediatricAgeGenerateBins",
			mock:         "skcm_broad_AGE_AT_PROCUREMENT",
			attr:         "AGE_AT_PROCUREMENT",
			missingCases: 4,
			opts:         Options{BinMethod: BinMethodGenerate, BinsGeneratorConfig: &BinsGeneratorConfig{BinSize: dec("10"), AnchorValue: dec("50")}},
			expected: []expectedBin{
				{sp("<="), nil, sp("20.0"), 1},
				{nil, sp("20.0"), sp("30.0"), 9},
				{nil, sp("30.0"), sp("40.0"), 16},
				{nil, sp("40.0"), sp("50.0"), 31},
				{nil, sp("50.0"), sp("60.0"), 25},
				{nil, sp("60.0"), sp("70.0"), 24},
				{nil, sp("70.0"), sp("80.0"), 11},
				{sp(">"), sp("80.0"), nil, 2},
				{sp("NA"), nil, nil, 4},
			},
		},
		{
			name:         "LinearDataBinnerWithPediatricAgeMedianBins",
			mock:         "skcm_broad_AGE_AT_PROCUREMENT",
			attr:         "AGE_AT_PROCUREMENT",
			missingCases: 4,
			opts:         Options{BinMethod: BinMethodMedian},
			expected: []expectedBin{
				{sp("<="), nil, sp("51.0"), 61},
				{sp(">"), sp("51.0"), nil, 58},
				{sp("NA"), nil, nil, 4},
			},
		},
		{
			name:         "LinearDataBinnerWithPediatricAgeQuartileBins",
			mock:         "skcm_broad_AGE_AT_PROCUREMENT",
			attr:         "AGE_AT_PROCUREMENT",
			missingCases: 4,
			opts:         Options{BinMethod: BinMethodQuartile},
			expected: []expectedBin{
				{sp("<="), nil, sp("41.0"), 30},
				{nil, sp("41.0"), sp("51.0"), 31},
				{nil, sp("51.0"), sp("65.0"), 33},
				{sp(">"), sp("65.0"), nil, 25},
				{sp("NA"), nil, nil, 4},
			},
		},
		{
			name: "LinearDataBinnerQuartileBinsLowComplexitySeriesQ1Q2Q3Identical",
			mock: "q1q2q3_identical",
			attr: "AGE_AT_PROCUREMENT",
			opts: Options{BinMethod: BinMethodQuartile},
			expected: []expectedBin{
				{sp("<="), nil, sp("250"), 50},
			},
		},
		{
			name: "LinearDataBinnerQuartileBinsLowComplexitySeriesQ2Q3Identical",
			mock: "q2q3_identical",
			attr: "AGE_AT_PROCUREMENT",
			opts: Options{BinMethod: BinMethodQuartile},
			expected: []expectedBin{
				{sp("<="), nil, sp("15"), 6},
				{nil, sp("15"), sp("250"), 17},
			},
		},
		{
			name: "LinearDataBinnerWithBigNumbers",
			mock: "random_BIG_NUMBER",
			attr: "BIG_NUMBER",
			expected: []expectedBin{
				{nil, sp("1.0E+8"), sp("316227766"), 2},
				{nil, sp("316227766"), sp("1.0E+9"), 9},
				{nil, sp("1.0E+9"), sp("3.16227766E+9"), 25},
				{nil, sp("3.16227766E+9"), sp("1.0E+10"), 9},
			},
		},
		{
			name: "LinearDataBinnerWithPredefinedAttribute",
			mock: "crc_msk_2018_MSI_SCORE",
			attr: "MSI_SCORE",
			opts: Options{BinMethod: BinMethodCustom, CustomBins: decs("1.0", "2.0", "5.0", "10.0", "30.0")},
			expected: []expectedBin{
				{sp("<="), nil, sp("1.0"), 722},
				{nil, sp("1.0"), sp("2.0"), 203},
				{nil, sp("2.0"), sp("5.0"), 93},
				{nil, sp("5.0"), sp("10.0"), 12},
				{nil, sp("10.0"), sp("30.0"), 39},
				{sp(">"), sp("30.0"), nil, 57},
				{sp("NA"), nil, nil, 8},
			},
		},
		{
			name: "LinearDataBinnerWithNA",
			mock: "blca_tcga_LYMPH_NODE_EXAMINED_COUNT",
			attr: "LYMPH_NODE_EXAMINED_COUNT",
			expected: []expectedBin{
				{sp("<="), nil, sp("5.0"), 30},
				{nil, sp("5.0"), sp("10.0"), 41},
				{nil, sp("10.0"), sp("15.0"), 55},
				{nil, sp("15.0"), sp("20.0"), 39},
				{nil, sp("20.0"), sp("25.0"), 26},
				{nil, sp("25.0"), sp("30.0"), 32},
				{nil, sp("30.0"), sp("35.0"), 8},
				{nil, sp("35.0"), sp("40.0"), 11},
				{nil, sp("40.0"), sp("45.0"), 8},
				{nil, sp("45.0"), sp("50.0"), 9},
				{nil, sp("50.0"), sp("55.0"), 6},
				{nil, sp("55.0"), sp("60.0"), 4},
				{nil, sp("60.0"), sp("65.0"), 6},
				{sp(">"), sp("65.0"), nil, 28},
				{sp("NA"), nil, nil, 109},
			},
		},
		{
			name: "LinearDataBinnerWithZeroIQR",
			mock: "impact_DNA_INPUT",
			attr: "DNA_INPUT",
			checks: []check{
				{-1, "size", "19"},
				{0, "special", "<="},
				{0, "end", "60.0"},
				{1, "start", "60.0"},
				{1, "end", "70.0"},
				{18, "start", "230.0"},
				{18, "end", "240.0"},
			},
		},
		{
			name: "LinearDataBinnerWithAlwaysZeroIQR",
			mock: "recursively_always_zero_IQR",
			attr: "DNA_INPUT",
			opts: Options{DistinctValueThreshold: 5},
			checks: []check{
				{-1, "size", "1"},
				{0, "special", "<="},
				{0, "end", "66"},
				{0, "countAll", ""},
			},
		},
		{
			name: "DiscreteDataBinner",
			mock: "acyc_fmi_2014_ACTIONABLE_ALTERATIONS",
			attr: "ACTIONABLE_ALTERATIONS",
			checks: []check{
				{-1, "size", "5"},
				{0, "start", "0.0"},
				{0, "end", "0.0"},
				{0, "count", "16"},
				{1, "start", "1.0"},
				{1, "end", "1.0"},
				{1, "count", "6"},
				{2, "start", "2.0"},
				{2, "end", "2.0"},
				{2, "count", "4"},
				{3, "start", "3.0"},
				{3, "end", "3.0"},
				{3, "count", "1"},
				{4, "start", "5.0"},
				{4, "end", "5.0"},
				{4, "count", "1"},
			},
		},
		{
			name:         "ScientificDataBinner",
			mock:         "blca_dfarber_mskcc_2014_SILENT_RATE",
			attr:         "SILENT_RATE",
			missingCases: 6,
			checks: []check{
				{-1, "size", "5"},
				{0, "start", "1.0e-8"},
				{0, "end", "1.0e-7"},
				{0, "count", "1"},
				{1, "start", "1.0e-7"},
				{1, "end", "1.0e-6"},
				{1, "count", "16"},
				{2, "start", "1.0e-6"},
				{2, "count", "32"},
				{3, "special", ">"},
				{3, "count", "1"},
				{4, "special", "NA"},
				{2, "endRound5Ceil", "1.0e-5"},
				{3, "startRound5Ceil", "1.0e-5"},
				{4, "count", "9"},
			},
		},
		{
			name: "LogScaleNoOutlierDataBinner",
			mock: "llg_tcga_egfr_mrna_expression",
			attr: "egfr_mrna_expression",
			checks: []check{
				{-1, "size", "8"},
				{0, "start", "31.0"},
				{0, "end", "100.0"},
				{0, "count", "6"},
				{1, "start", "100.0"},
				{1, "end", "316.0"},
				{1, "count", "25"},
				{2, "start", "316.0"},
				{2, "end", "1000.0"},
				{2, "count", "84"},
				{3, "start", "1000.0"},
				{3, "end", "3162.0"},
				{3, "count", "222"},
				{4, "start", "3162.0"},
				{4, "end", "10000.0"},
				{4, "count", "148"},
				{5, "start", "10000.0"},
				{5, "end", "31622.0"},
				{5, "count", "30"},
				{6, "start", "31622.0"},
				{6, "end", "100000.0"},
				{6, "count", "13"},
				{7, "special", ">"},
				{7, "start", "100000.0"},
				{7, "count", "2"},
			},
		},
		{
			name: "LogScaleDataBinner",
			mock: "ampca_bcm_2016_DAYS_TO_LAST_FOLLOWUP",
			attr: "DAYS_TO_LAST_FOLLOWUP",
			checks: []check{
				{-1, "size", "7"},
				{0, "special", "<="},
				{0, "end", "10.0"},
				{0, "count", "1"},
				{1, "start", "10.0"},
				{1, "end", "31.0"},
				{1, "count", "3"},
				{2, "start", "31.0"},
				{2, "end", "100.0"},
				{2, "count", "5"},
				{3, "start", "100.0"},
				{3, "end", "316.0"},
				{3, "count", "23"},
				{4, "start", "316.0"},
				{4, "end", "1000.0"},
				{4, "count", "67"},
				{5, "start", "1000.0"},
				{5, "end", "3162.0"},
				{5, "count", "55"},
				{6, "start", "3162.0"},
				{6, "end", "10000.0"},
				{6, "count", "6"},
			},
		},
		{
			name: "LogScaleDisabledDataBinner",
			mock: "ampca_bcm_2016_DAYS_TO_LAST_FOLLOWUP",
			attr: "DAYS_TO_LAST_FOLLOWUP",
			opts: Options{DisableLogScale: true},
			checks: []check{
				{-1, "size", "17"},
				{0, "special", "<="},
				{0, "end", "200.0"},
				{0, "count", "17"},
				{1, "start", "200.0"},
				{1, "end", "400.0"},
				{1, "count", "24"},
				{16, "start", "3200.0"},
				{16, "special", ">"},
				{16, "count", "6"},
			},
		},
		{
			name: "NegativeLogScaleDataBinner",
			mock: "acc_tcga_DAYS_TO_BIRTH",
			attr: "DAYS_TO_BIRTH",
			checks: []check{
				{-1, "size", "2"},
				{0, "start", "-31622.0"},
				{0, "end", "-10000.0"},
				{0, "count", "78"},
				{1, "start", "-10000.0"},
				{1, "end", "-3162.0"},
				{1, "count", "14"},
			},
		},
		{
			name: "LogScaleDataBinnerWithSpecialOutliers",
			mock: "genie_INT_DOD",
			attr: "INT_DOD",
			checks: []check{
				{-1, "size", "5"},
				{0, "special", "<="},
				{0, "end", "6570.0"},
				{0, "count", "8"},
				{1, "start", "6570.0"},
				{1, "end", "10000.0"},
				{2, "start", "10000.0"},
				{2, "end", "31622.0"},
				{3, "start", "31622.0"},
				{3, "end", "32485.0"},
				{4, "start", "32485.0"},
				{4, "special", ">"},
				{4, "count", "5"},
			},
		},
		{
			name: "NegativeLogScaleDisabledDataBinner",
			mock: "acc_tcga_DAYS_TO_BIRTH",
			attr: "DAYS_TO_BIRTH",
			opts: Options{DisableLogScale: true},
			checks: []check{
				{-1, "size", "14"},
				{0, "special", "<="},
				{0, "end", "-30000.0"},
				{13, "start", "-6000.0"},
				{13, "special", ">"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, ok := mock[tc.mock]
			if !ok {
				t.Fatalf("missing mock data %q", tc.mock)
			}
			values := perCase(raw)
			values = append(values, Value{Value: "NA", Count: tc.missingCases})
			opts := tc.opts
			var bins []Bin
			var err error
			if tc.static {
				opts.Method = Static
				bins, err = Calculate(tc.attr, values, perCase(raw[:tc.filteredTo]), opts)
			} else {
				opts.Method = Dynamic
				bins, err = Calculate(tc.attr, nil, values, opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range bins {
				if b.AttributeID != tc.attr {
					t.Fatalf("attributeId = %q", b.AttributeID)
				}
			}
			if tc.expected != nil {
				assertBinsIdentical(t, tc.expected, bins)
			}
			for _, c := range tc.checks {
				runCheck(t, c, bins, len(raw))
			}
		})
	}
}

// assertBinsIdentical mirrors testBinsIdentical. Mockito's ReflectionEquals
// compares BigDecimal fields with compareTo, so edges are compared
// numerically here; exact scales are covered by the oracle test.
func assertBinsIdentical(t *testing.T, expected []expectedBin, bins []Bin) {
	t.Helper()
	if len(expected) != len(bins) {
		t.Fatalf("got %d bins, want %d: %s", len(bins), len(expected), formatBins(bins))
	}
	for i, e := range expected {
		b := bins[i]
		ok := equalStrPtr(e.special, b.SpecialValue) &&
			equalDecPtr(e.start, b.Start) &&
			equalDecPtr(e.end, b.End) &&
			e.count == b.Count
		if !ok {
			t.Errorf("element %d is not correct: got %s", i, formatBin(b))
		}
	}
}

func equalStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalDecPtr(want *string, got *Decimal) bool {
	if want == nil || got == nil {
		return want == nil && got == nil
	}
	return MustParseDecimal(*want).Cmp(*got) == 0
}

func runCheck(t *testing.T, c check, bins []Bin, nValues int) {
	t.Helper()
	if c.field == "size" {
		if got := len(bins); itoa(got) != c.want {
			t.Fatalf("size = %d, want %s: %s", got, c.want, formatBins(bins))
		}
		return
	}
	if c.bin >= len(bins) {
		t.Fatalf("no bin %d: %s", c.bin, formatBins(bins))
	}
	b := bins[c.bin]
	fail := func(got string) {
		t.Errorf("bin %d %s = %s, want %s (%s)", c.bin, c.field, got, c.want, formatBin(b))
	}
	switch c.field {
	case "special":
		if b.Special() != c.want || b.SpecialValue == nil {
			fail(b.Special())
		}
	case "start", "end":
		v := b.Start
		if c.field == "end" {
			v = b.End
		}
		if v == nil || !MustParseDecimal(c.want).identical(*v) {
			fail(fmtDec(v))
		}
	case "startInt", "endInt":
		v := b.Start
		if c.field == "endInt" {
			v = b.End
		}
		if v == nil || itoa(v.intValue()) != c.want {
			fail(fmtDec(v))
		}
	case "startNil", "endNil":
		v := b.Start
		if c.field == "endNil" {
			v = b.End
		}
		if v != nil {
			fail(fmtDec(v))
		}
	case "startRound5Ceil", "endRound5Ceil":
		v := b.Start
		if c.field == "endRound5Ceil" {
			v = b.End
		}
		if v == nil || MustParseDecimal(c.want).Cmp(round(*v, 5, roundCeiling)) != 0 {
			fail(fmtDec(v))
		}
	case "count":
		if itoa(b.Count) != c.want {
			fail(itoa(b.Count))
		}
	case "countAll":
		if b.Count != nValues {
			fail(itoa(b.Count))
		}
	default:
		t.Fatalf("unknown check %q", c.field)
	}
}

func itoa(i int) string { return fmtInt(int64(i)) }

func fmtInt(i int64) string {
	if i == math.MinInt64 {
		return "-9223372036854775808"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
		if i == 0 {
			break
		}
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func fmtDec(d *Decimal) string {
	if d == nil {
		return "null"
	}
	return d.String()
}

func formatBin(b Bin) string {
	s := "null"
	if b.SpecialValue != nil {
		s = *b.SpecialValue
	}
	return "{" + s + " " + fmtDec(b.Start) + " " + fmtDec(b.End) + " " + itoa(b.Count) + "}"
}

func formatBins(bins []Bin) string {
	s := ""
	for _, b := range bins {
		s += formatBin(b)
	}
	return s
}

func TestNonClinicalSkipsAgeFirstBin(t *testing.T) {
	var values []Value
	for i := 2; i <= 600; i += 6 {
		values = append(values, Value{Value: itoa(i), Count: 1})
	}
	age, err := Calculate("AGE", nil, values, Options{Method: Dynamic})
	if err != nil {
		t.Fatal(err)
	}
	// Recorded from the Java DataBinner: "<=18" then (18, 100.0].
	if age[0].End == nil || age[0].End.String() != "18" || age[1].Start.String() != "18" || age[1].End.String() != "100.0" {
		t.Fatalf("clinical AGE should start its first bin at 18: %s", formatBins(age))
	}
	plain, err := Calculate("X", nil, values, Options{Method: Dynamic})
	if err != nil {
		t.Fatal(err)
	}
	nonClinical, err := Calculate("AGE", nil, values, Options{Method: Dynamic, NonClinical: true})
	if err != nil {
		t.Fatal(err)
	}
	if formatBins(nonClinical) != formatBins(plain) {
		t.Errorf("non-clinical AGE bins %s differ from %s", formatBins(nonClinical), formatBins(plain))
	}
}

func TestEmptyInputs(t *testing.T) {
	for _, m := range []Method{Static, Dynamic} {
		bins, err := Calculate("X", nil, nil, Options{Method: m})
		if err != nil || bins == nil || len(bins) != 0 {
			t.Errorf("%s: got %v, %v; want empty non-nil", m, bins, err)
		}
	}
	if _, err := Calculate("X", nil, nil, Options{Method: "OTHER"}); err == nil {
		t.Error("unknown method should fail")
	}
}

func TestJavaExceptionsBecomeErrors(t *testing.T) {
	values := perCase([]string{"1", "2", "0x1A"})
	if _, err := Calculate("X", nil, values, Options{Method: Dynamic}); err == nil {
		t.Error("hex value should fail like new BigDecimal(\"0x1A\")")
	}
	nums := perCase([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"})
	if _, err := Calculate("X", nil, nums, Options{Method: Dynamic, BinMethod: BinMethodGenerate, BinsGeneratorConfig: &BinsGeneratorConfig{AnchorValue: dec("0")}}); err == nil {
		t.Error("missing bin size should fail")
	}
}
