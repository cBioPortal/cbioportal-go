package contract

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// Contracts for study-view and comparison endpoints whose shapes, orders and
// statistics follow cBioPortal's Java services.

func TestContractGeneTablesEventKeys(t *testing.T) {
	requireAPI(t)
	var cna []map[string]json.RawMessage
	post(t, "/api/cna-genes/fetch", studyFilter(nil), &cna)
	if len(cna) == 0 {
		t.Fatal("no CNA genes")
	}
	for _, r := range cna {
		var entrez, alt int
		var key string
		json.Unmarshal(r["entrezGeneId"], &entrez)
		json.Unmarshal(r["alteration"], &alt)
		json.Unmarshal(r["uniqueEventKey"], &key)
		if want := strconv.Itoa(entrez) + strconv.Itoa(alt); key != want {
			t.Errorf("CNA uniqueEventKey = %q, want %q", key, want)
		}
		if _, ok := r["qValue"]; ok {
			t.Errorf("null qValue serialized: %v", r)
		}
	}

	var muts []struct {
		EntrezGeneID    int      `json:"entrezGeneId"`
		HugoGeneSymbol  string   `json:"hugoGeneSymbol"`
		UniqueEventKey  string   `json:"uniqueEventKey"`
		EntrezGeneIDs   []int    `json:"entrezGeneIds"`
		HugoGeneSymbols []string `json:"hugoGeneSymbols"`
	}
	post(t, "/api/mutated-genes/fetch", studyFilter(nil), &muts)
	if len(muts) == 0 {
		t.Fatal("no mutated genes")
	}
	for _, r := range muts {
		if r.UniqueEventKey != r.HugoGeneSymbol || len(r.EntrezGeneIDs) != 1 || r.EntrezGeneIDs[0] != r.EntrezGeneID ||
			len(r.HugoGeneSymbols) != 1 || r.HugoGeneSymbols[0] != r.HugoGeneSymbol {
			t.Errorf("mutated gene keys: %+v", r)
		}
	}
}

func TestContractSampleListCountsByListType(t *testing.T) {
	requireAPI(t)
	var d []struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}
	post(t, "/api/sample-lists-counts/fetch", studyFilter(nil), &d)
	// java.util.HashMap order of the list types (16 buckets).
	order := []string{"all", "testprofiled", "cnaseq", "cna", "custom", "sequenced"}
	known := map[string]bool{}
	for _, v := range order {
		known[v] = true
	}
	var values []string
	counts := map[string]int{}
	for _, r := range d {
		counts[r.Value] = r.Count
		if known[r.Value] {
			values = append(values, r.Value)
		}
		if strings.HasPrefix(r.Value, "study_es_0") {
			t.Errorf("list value %q keeps the study prefix", r.Value)
		}
	}
	wantEq(t, values, order)
	wantEq(t, counts["all"], 845)
}

func TestContractMolecularProfileSampleCountsShape(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/molecular-profile-sample-counts/fetch", studyFilter(nil), &d)
	var values []string
	for _, r := range d {
		values = append(values, r["value"].(string))
		if _, ok := r["uniqueCount"]; ok {
			t.Errorf("uniqueCount is not part of molecular profile counts: %v", r)
		}
	}
	// java.util.HashMap order of the profile types.
	wantEq(t, values[:4], []string{"treatment_ec50", "log2CNA", "gsva_pvalues", "structural_variants"})
}

func TestContractClinicalDataEnrichmentsJavaStats(t *testing.T) {
	requireAPI(t)
	group := func(name string, ids ...string) map[string]any {
		var s []map[string]string
		for _, id := range ids {
			s = append(s, map[string]string{"studyId": "study_es_0", "sampleId": id})
		}
		return map[string]any{"name": name, "sampleIdentifiers": s}
	}
	body := map[string]any{"groups": []any{
		group("a", "TCGA-A1-A0SB-01", "TCGA-A1-A0SD-01", "TCGA-A1-A0SE-01", "TCGA-A1-A0SF-01"),
		group("b", "TCGA-A1-A0SG-01", "TCGA-A1-A0SH-01", "TCGA-A1-A0SI-01", "TCGA-A1-A0SJ-01", "TCGA-A1-A0SK-01"),
	}}
	var d []map[string]json.RawMessage
	post(t, "/api/clinical-data-enrichments/fetch", body, &d)
	type row struct {
		id, method    string
		score, pValue float64
	}
	var got []row
	for _, r := range d {
		if _, ok := r["qValue"]; ok {
			t.Errorf("clinical enrichment carries a qValue: %s", r["qValue"])
		}
		var a struct {
			ID string `json:"clinicalAttributeId"`
		}
		var x row
		json.Unmarshal(r["clinicalAttribute"], &a)
		json.Unmarshal(r["method"], &x.method)
		json.Unmarshal(r["score"], &x.score)
		json.Unmarshal(r["pValue"], &x.pValue)
		x.id = a.ID
		got = append(got, x)
	}
	// Categorical attributes first, then numeric, in HashMap order; 2x2
	// chi-squared tables are Yates-corrected and Kruskal-Wallis is
	// tie-corrected, with Datumbox's distribution approximations.
	wantEq(t, got, []row{
		{"SUBTYPE", "Chi-squared Test", 0.052083333333333336, 0.81947697816352},
		{"SAMPLE_COUNT", "Chi-squared Test", 0.0140625, 0.9056038231290666},
		{"OS_STATUS", "Chi-squared Test", 0.0140625, 0.9056038231290666},
		{"MUTATION_COUNT", "Wilcoxon Test", 0.9999996375468236, 0.3173107614196182},
		{"DFS_MONTHS", "Wilcoxon Test", 0.08333351139001867, 0.7728300040423347},
		{"OS_MONTHS", "Wilcoxon Test", 0.059999739198192736, 0.8064959431205235},
	})
}

func TestContractViolinCurveIsDensity(t *testing.T) {
	requireAPI(t)
	var d struct {
		AxisStart float64 `json:"axisStart"`
		AxisEnd   float64 `json:"axisEnd"`
		Rows      []struct {
			Category         string    `json:"category"`
			NumSamples       int       `json:"numSamples"`
			CurveData        []float64 `json:"curveData"`
			IndividualPoints []any     `json:"individualPoints"`
		} `json:"rows"`
	}
	post(t, "/api/clinical-data-violin-plots/fetch?categoricalAttributeId=OS_STATUS&numericalAttributeId=OS_MONTHS&numCurvePoints=100&sigmaMultiplier=4",
		studyFilter(nil), &d)
	step := (d.AxisEnd - d.AxisStart) / 99
	for _, r := range d.Rows {
		if len(r.CurveData) == 0 {
			continue
		}
		mass := 0.0
		for _, v := range r.CurveData {
			mass += v * step
		}
		// A sum of normalized Gaussians integrates to at most the number of
		// non-outlier values (less the tails beyond the axis).
		n := float64(r.NumSamples - len(r.IndividualPoints))
		if mass > n*1.01 || mass < n*0.5 {
			t.Errorf("%s: curve mass %.2f for %v values", r.Category, mass, n)
		}
	}
}
