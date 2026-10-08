package studyview

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

func TestGeneAlterationCountEventKeys(t *testing.T) {
	entrez, alt := int64(8086), int64(-2)
	cna := GeneAlterationCount{EntrezGeneID: &entrez, HugoGeneSymbol: "AAAS", Alteration: &alt, MatchingGenePanelIDs: []string{"WES"}}.withEventKeys()
	b, _ := json.Marshal(cna)
	for _, want := range []string{`"uniqueEventKey":"8086-2"`, `"entrezGeneIds":[8086]`, `"hugoGeneSymbols":["AAAS"]`, `"alteration":-2`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("CNA count %s lacks %s", b, want)
		}
	}
	if strings.Contains(string(b), "cytoband") || strings.Contains(string(b), "qValue") {
		t.Errorf("null fields must be omitted: %s", b)
	}

	tp53 := int64(7157)
	mut := GeneAlterationCount{EntrezGeneID: &tp53, HugoGeneSymbol: "TP53"}.withEventKeys()
	b, _ = json.Marshal(mut)
	for _, want := range []string{`"uniqueEventKey":"TP53"`, `"entrezGeneIds":[7157]`, `"hugoGeneSymbols":["TP53"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("mutation count %s lacks %s", b, want)
		}
	}
	if strings.Contains(string(b), "alteration") {
		t.Errorf("gene count must not carry an alteration: %s", b)
	}
}

func TestMergeCaseListCounts(t *testing.T) {
	got := mergeCaseListCounts([]CaseListDataCount{
		{Value: "all", Label: "All samples", Count: 12},
		{Value: "cna", Label: "Samples with CNA data", Count: 12},
		{Value: "cnaseq", Label: "Samples with mutation and CNA data", Count: 12},
		{Value: "sequenced", Label: "Samples with mutation data", Count: 12},
		{Value: "sv", Label: "Samples with SV data", Count: 12},
		{Value: "all", Label: "All tumors", Count: 3},
	})
	var values []string
	for _, c := range got {
		values = append(values, c.Value)
	}
	// java.util.HashMap iteration order, as cBioPortal returns them.
	if want := []string{"all", "cnaseq", "sv", "cna", "sequenced"}; !slices.Equal(values, want) {
		t.Errorf("order = %v, want %v", values, want)
	}
	if got[0].Count != 15 || got[0].Label != "All samples" {
		t.Errorf("merged all = %+v, want count 15 under the first label", got[0])
	}
}

func TestJavaHashMapOrderMolecularProfileCounts(t *testing.T) {
	got := core.JavaHashMapOrder([]string{"cna", "mutations", "structural_variants"})
	if want := []string{"mutations", "cna", "structural_variants"}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestClinicalEnrichmentAttributeOrder pins the order cBioPortal lists
// acbc_mskcc_2015's enrichments in: the HashMap order of attrId+patientFlag.
// ADJUVANT_CHEMOtrue shares a bucket with MYB_NFIB_CNAfalse, where order
// follows insertion (the attribute query's row order), so it is left out.
func TestClinicalEnrichmentAttributeOrder(t *testing.T) {
	meta := "ADJUVANT_CHEMO 1;ADJUVANT_TX 1;AGE 1;CANCER_TYPE 0;CANCER_TYPE_DETAILED 0;ER_STATUS_BY_IHC 0;" +
		"FOLLOWUP_YEARS 1;FUSION_STATUS 0;GENOMIC_ALTERATIONS 0;IHC_HER2 0;LOCAL_REGIONAL_RECURRENCE 1;" +
		"METASTATIC_SITE 0;METASTATIC_TUMOR_INDICATOR 0;MUTATION_COUNT 0;MYB_MYBL1_FUSION 0;MYB_NFIB_CNA 0;" +
		"MYB_NFIB_FISH 0;MYB_NFIB_NONSYNONYMOUS_COUNT 0;MYB_NFIB_REARRANGEMENT 1;NEOADJUVANT_CHEMO 1;" +
		"ONCOTREE_CODE 0;OVERALL_PATIENT_HISTOLOGY 1;PLATFORM 0;PRIMARY_SITE 0;PR_STATUS_BY_IHC 0;" +
		"RADIATION_THERAPY 1;SAMPLE_COUNT 1;SAMPLE_TYPE 0;SEX 1;SOMATIC_STATUS 0;STUDY 1;TIME_TO_MET_MONTHS 1;" +
		"TMB_NONSYNONYMOUS 0;TUMOR_SIZE 0;TUMOR_STAGE 0;TYPE_OF_SURGERY 0;VITAL_STATUS 1"
	var keys []string
	for _, m := range strings.Split(meta, ";") {
		id, flag, _ := strings.Cut(m, " ")
		keys = append(keys, id+strconv.FormatBool(flag == "1"))
	}
	tested := map[string]bool{}
	prod := []string{"TUMOR_STAGE", "MYB_NFIB_REARRANGEMENT", "MYB_NFIB_FISH", "TUMOR_SIZE",
		"MYB_NFIB_NONSYNONYMOUS_COUNT", "RADIATION_THERAPY", "METASTATIC_TUMOR_INDICATOR",
		"OVERALL_PATIENT_HISTOLOGY", "TYPE_OF_SURGERY", "NEOADJUVANT_CHEMO", "MYB_NFIB_CNA",
		"CANCER_TYPE", "ADJUVANT_TX", "VITAL_STATUS", "LOCAL_REGIONAL_RECURRENCE", "MYB_MYBL1_FUSION"}
	for _, id := range prod {
		tested[id] = true
	}
	var got []string
	for _, k := range core.JavaHashMapOrder(keys) {
		id := strings.TrimSuffix(strings.TrimSuffix(k, "true"), "false")
		if tested[id] {
			got = append(got, id)
		}
	}
	if !slices.Equal(got, prod) {
		t.Errorf("order = %v\nwant %v", got, prod)
	}
}

// TestViolinCurveNormalized checks the kernel density is a sum of normalized
// Gaussians (commons-math Gaussian(value, sigma)), against cBioPortal's curve
// for acbc_mskcc_2015 AGE by SEX (axis 0..100, 5 points, sigmaMultiplier 4).
func TestViolinCurveNormalized(t *testing.T) {
	ages := []float64{52, 53, 63, 57, 52, 78, 62, 67, 66, 77, 53, 69}
	points := []float64{0, 25, 50, 75, 100}
	curve := violinCurve(points, ages, 4*25.0, func(v float64) float64 { return v })
	want := []float64{0.03930694103237355, 0.04448610083331892, 0.04732044594999007, 0.04730909468022695, 0.04445428043809778}
	for i := range want {
		if curve[i] != want[i] {
			t.Errorf("curve[%d] = %v, want %v", i, curve[i], want[i])
		}
	}
}

func TestBigDecimalSum(t *testing.T) {
	cases := []struct {
		terms []float64
		want  float64
	}{
		{[]float64{0.1, 0.2}, 0.3},
		{[]float64{1e-300, 1}, 1},
		{[]float64{}, 0},
		{[]float64{0.5, 0, 0.25}, 0.75},
	}
	for _, c := range cases {
		if got := bigDecimalSum(c.terms); got != c.want {
			t.Errorf("bigDecimalSum(%v) = %v, want %v", c.terms, got, c.want)
		}
	}
}

func BenchmarkViolinCurve(b *testing.B) {
	data := make([]float64, 2000)
	for i := range data {
		data[i] = float64(i % 97)
	}
	points := make([]float64, 100)
	for i := range points {
		points[i] = float64(i)
	}
	for b.Loop() {
		violinCurve(points, data, 4, func(v float64) float64 { return v })
	}
}

func TestBigDecimalSumMatchesJava(t *testing.T) {
	// BigDecimal sums of these terms (shortest decimals) round differently
	// from the exact binary sum.
	ages := []float64{52, 53, 63, 57, 52, 78, 62, 67, 66, 77, 53, 69}
	sigma := 100.0
	var terms []float64
	for _, a := range ages {
		terms = append(terms, 1/(sigma*math.Sqrt(2*math.Pi))*math.Exp(-(50-a)*(50-a)*(1/(2*sigma*sigma))))
	}
	if got, want := bigDecimalSum(terms), 0.04732044594999007; got != want {
		t.Errorf("bigDecimalSum = %v, want %v", got, want)
	}
	if got := bigDecimalSum([]float64{-0.1, 0.3}); got != 0.2 {
		t.Errorf("mixed signs: %v", got)
	}
}
