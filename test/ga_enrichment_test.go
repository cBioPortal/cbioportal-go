package contract

import (
	"math"
	"net/http"
	"strings"
	"testing"
)

// Contracts for /generic-assay-categorical-enrichments/fetch and
// /generic-assay-binary-enrichments/fetch on the lgg_ucsf_2014_test_generic_assay
// fixture. Expected values come from cbioportal's ExpressionEnrichmentUtil,
// ExpressionEnrichmentServiceImpl sorting and calcqValue (commons-math 3.6.1)
// run on the study's data files with the same groups.

const lggGA = "lgg_ucsf_2014_test_generic_assay"

var lggSamples = []string{"P01_Pri", "P01_Rec", "P02_Pri", "P02_Rec", "P04_Pri", "P04_Rec1", "P04_Rec2", "P04_Rec3",
	"P05_Pri", "P05_Rec", "P06_Pri", "P06_Rec", "P07_Pri", "P07_Rec", "P08_Pri", "P08_Rec", "P09_Pri", "P09_Rec",
	"P10_Pri", "P10_Rec", "P11_Pri", "P11_Rec", "P12_Pri", "P12_Rec", "P13_Pri", "P13_Rec", "P15_Pri", "P15_Rec",
	"P16_Pri", "P16_Rec", "P17_Pri_A", "P17_Pri_B", "P17_Pri_C", "P17_Rec1_A", "P17_Rec1_B", "P17_Rec1_C", "P17_Rec1_D",
	"P18_Pri_A", "P18_Pri_B", "P18_Pri_C", "P18_Pri_D", "P18_Rec", "P21_Pri", "P21_Rec", "P24_Pri", "P24_Rec1",
	"P24_Rec2", "P25_Pri", "P25_Rec", "P26_Pri_A", "P26_Pri_B", "P26_Rec1_A", "P26_Rec1_B", "P27_Pri_A", "P27_Pri_B",
	"P27_Rec1_A", "P27_Rec1_B", "P28_Pri", "P28_Rec", "P29_Pri", "P29_Rec"}

type gaGroup struct {
	name    string
	samples []string
}

// lggPriRec splits the samples into primary and recurrence groups.
func lggPriRec() []gaGroup {
	var pri, rec []string
	for _, s := range lggSamples {
		if strings.Contains(s, "_Pri") {
			pri = append(pri, s)
		} else {
			rec = append(rec, s)
		}
	}
	return []gaGroup{{"Primary", pri}, {"Recurrence", rec}}
}

// lggThirds splits the samples, in order, into groups A (20), B (20), C (21).
func lggThirds() []gaGroup {
	return []gaGroup{{"A", lggSamples[:20]}, {"B", lggSamples[20:40]}, {"C", lggSamples[40:]}}
}

func gaBody(profile string, groups []gaGroup) []map[string]any {
	out := make([]map[string]any, len(groups))
	for i, g := range groups {
		ids := make([]map[string]string, len(g.samples))
		for j, s := range g.samples {
			ids[j] = map[string]string{"caseId": s, "molecularProfileId": lggGA + "_" + profile}
		}
		out[i] = map[string]any{"name": g.name, "molecularProfileCaseIdentifiers": ids}
	}
	return out
}

func requireLgg(t *testing.T) {
	t.Helper()
	requireAPI(t)
	resp, _ := rawDo(t, "GET", "/api/studies/"+lggGA, "")
	if resp.StatusCode != http.StatusOK {
		t.Skipf("%s not loaded", lggGA)
	}
}

func wantClose(t *testing.T, what string, got, want float64) {
	t.Helper()
	if got != want && math.Abs(got-want) > 1e-12*math.Abs(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

type gaGroupStat struct {
	Name              string   `json:"name"`
	MeanExpression    *float64 `json:"meanExpression"`
	StandardDeviation *float64 `json:"standardDeviation"`
}

type gaCount struct {
	Count      int    `json:"count"`
	Name       string `json:"name"`
	TotalCount int    `json:"totalCount"`
}

type gaEnrichment struct {
	StableID                    string            `json:"stableId"`
	Name                        *string           `json:"name"`
	PValue                      float64           `json:"pValue"`
	QValue                      float64           `json:"qValue"`
	GroupsStatistics            []gaGroupStat     `json:"groupsStatistics"`
	Counts                      []gaCount         `json:"counts"`
	GenericEntityMetaProperties map[string]string `json:"genericEntityMetaProperties"`
}

func stableIDs(rows []gaEnrichment) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.StableID
	}
	return out
}

func groupNames(r gaEnrichment) []string {
	out := make([]string, len(r.GroupsStatistics))
	for i, g := range r.GroupsStatistics {
		out[i] = g.Name
	}
	return out
}

func TestContractGenericAssayCategoricalEnrichmentArmLevelCNA(t *testing.T) {
	requireLgg(t)
	var rows []gaEnrichment
	post(t, "/api/generic-assay-categorical-enrichments/fetch?enrichmentType=SAMPLE", gaBody("armlevel_cna", lggPriRec()), &rows)
	// Every entity is reported, sorted by p-value with ties in storage order.
	wantEq(t, stableIDs(rows), []string{"11q_status", "6q_status", "7q_status", "2q_status", "8p_status", "4p_status",
		"20q_status", "11p_status", "20p_status", "1p_status", "15q_status", "14q_status", "19q_status", "1q_status",
		"16q_status", "18p_status", "10q_status", "22q_status", "17q_status", "9p_status", "21q_status", "8q_status",
		"3p_status", "7p_status", "12q_status", "4q_status", "16p_status", "2p_status", "6p_status", "12p_status",
		"19p_status", "3q_status", "9q_status", "17p_status", "10p_status", "13q_status", "5p_status", "5q_status",
		"18q_status"})
	want := []struct{ p, q float64 }{
		{0.10403371626055691, 0.9341323757068193},
		{0.11749575239143617, 0.9341323757068193},
		{0.2565679723337805, 0.9341323757068193},
	}
	for i, w := range want {
		wantClose(t, rows[i].StableID+" pValue", rows[i].PValue, w.p)
		wantClose(t, rows[i].StableID+" qValue", rows[i].QValue, w.q)
	}
	for _, r := range rows {
		// Groups in Java HashMap order; categorical statistics carry only the name.
		wantEq(t, groupNames(r), []string{"Recurrence", "Primary"})
		for _, g := range r.GroupsStatistics {
			if g.MeanExpression != nil || g.StandardDeviation != nil {
				t.Errorf("%s: categorical group statistics carry values: %+v", r.StableID, g)
			}
		}
		if r.Name != nil || r.Counts != nil {
			t.Errorf("%s: unexpected name/counts", r.StableID)
		}
	}
	wantEq(t, rows[0].GenericEntityMetaProperties["NAME"], "11q")
}

func TestContractGenericAssayCategoricalEnrichmentPatientLevelHLA(t *testing.T) {
	requireLgg(t)
	// hla is patient_level: each group keeps one sample per patient and every
	// sample carries its patient's value.
	var rows []gaEnrichment
	post(t, "/api/generic-assay-categorical-enrichments/fetch", gaBody("hla", lggThirds()), &rows)
	wantEq(t, stableIDs(rows), []string{"HLA_DPB1", "HLA_DRB1", "HLA_DQA1", "HLA-C", "HLA-B", "HLA-A", "HLA_DQB1", "HLA_DPA1"})
	wantP := []float64{0.3029755472585085, 0.3034623135207739, 0.4382313046794595, 0.4731104966789714,
		0.4896440138431153, 0.5422550928553986, 0.6195292547807327, 0.7303103382513572}
	wantQ := []float64{0.7080334340351231, 0.7080334340351231, 0.7080334340351231, 0.7080334340351231,
		0.7080334340351231, 0.7080334340351231, 0.7080334340351231, 0.7303103382513572}
	for i, r := range rows {
		wantClose(t, r.StableID+" pValue", r.PValue, wantP[i])
		wantClose(t, r.StableID+" qValue", r.QValue, wantQ[i])
		wantEq(t, groupNames(r), []string{"A", "B", "C"})
	}

	// Every sample of a patient in both groups: identical rows, p = 1.
	rows = nil
	post(t, "/api/generic-assay-categorical-enrichments/fetch", gaBody("hla", lggPriRec()), &rows)
	wantEq(t, len(rows), 8)
	for _, r := range rows {
		wantEq(t, r.PValue, 1.0)
		wantEq(t, r.QValue, 1.0)
	}
}

func TestContractGenericAssayBinaryEnrichment(t *testing.T) {
	requireLgg(t)
	var rows []gaEnrichment
	post(t, "/api/generic-assay-binary-enrichments/fetch?enrichmentType=SAMPLE", gaBody("mutational_signature_binary_SBS", lggPriRec()), &rows)
	p := "mutational_signature_binary_SBS"
	var want []string
	for _, s := range []string{"11", "7a", "19", "7b", "21", "16", "18", "7d", "15", "24", "2", "6", "12", "13", "22",
		"5", "10b", "25", "23", "17a", "10a", "1", "9", "20"} {
		want = append(want, p+s)
	}
	// Entities whose t statistic is undefined (no variance) are left out.
	wantEq(t, stableIDs(rows), want)
	type stat struct {
		name     string
		mean, sd float64
		count    gaCount
	}
	exp := []struct {
		p, q  float64
		stats []stat
	}{
		{0.01174725041476874, 0.28193400995444976, []stat{
			{"Recurrence", 0.19354838709677416, 0.4016096644512495, gaCount{6, "Recurrence", 31}},
			{"Primary", 0, 0, gaCount{0, "Primary", 30}}}},
		{0.025957688972848576, 0.311492267674182912, []stat{
			{"Recurrence", 0.22580645161290322, 0.42502371850324144, gaCount{7, "Recurrence", 31}},
			{"Primary", 0.03333333333333333, 0.18257418583505533, gaCount{1, "Primary", 30}}}},
		{0.0436068788423549, 0.34316148043857512, []stat{
			{"Recurrence", 0.25806451612903225, 0.44480272297456924, gaCount{8, "Recurrence", 31}},
			{"Primary", 0.06666666666666668, 0.2537081317024625, gaCount{2, "Primary", 30}}}},
	}
	for i, e := range exp {
		r := rows[i]
		wantClose(t, r.StableID+" pValue", r.PValue, e.p)
		wantClose(t, r.StableID+" qValue", r.QValue, e.q)
		if len(r.GroupsStatistics) != len(e.stats) || len(r.Counts) != len(e.stats) {
			t.Fatalf("%s: %d stats / %d counts", r.StableID, len(r.GroupsStatistics), len(r.Counts))
		}
		for j, s := range e.stats {
			g := r.GroupsStatistics[j]
			wantEq(t, g.Name, s.name)
			if g.MeanExpression == nil || g.StandardDeviation == nil {
				t.Fatalf("%s: missing mean/sd", r.StableID)
			}
			wantClose(t, r.StableID+" mean", *g.MeanExpression, s.mean)
			wantClose(t, r.StableID+" sd", *g.StandardDeviation, s.sd)
			wantEq(t, r.Counts[j], s.count)
		}
	}
	wantEq(t, rows[0].GenericEntityMetaProperties["NAME"], "SBS11 (TMZ)")

	// Three groups: one-way ANOVA.
	rows = nil
	post(t, "/api/generic-assay-binary-enrichments/fetch", gaBody("mutational_signature_binary_SBS", lggThirds()), &rows)
	wantEq(t, len(rows), 24)
	wantEq(t, rows[0].StableID, p+"19")
	wantClose(t, "SBS19 pValue", rows[0].PValue, 0.0538727129030554)
	wantClose(t, "SBS19 qValue", rows[0].QValue, 0.625527941179875)
	wantEq(t, rows[0].Counts, []gaCount{{5, "A", 20}, {0, "B", 20}, {5, "C", 21}})
}

func TestContractGenericAssayCategoricalBinaryErrors(t *testing.T) {
	requireLgg(t)
	cat := "/api/generic-assay-categorical-enrichments/fetch"
	bin := "/api/generic-assay-binary-enrichments/fetch"
	two := `[{"name":"a","molecularProfileCaseIdentifiers":[{"caseId":"P01_Pri","molecularProfileId":"` + lggGA + `_hla"},` +
		`{"caseId":"P01_Pri","molecularProfileId":"` + lggGA + `_armlevel_cna"}]}]`
	wantError(t, "POST", cat, two, http.StatusNotImplemented, "Requested API is not implemented yet")
	binProfile := `[{"name":"a","molecularProfileCaseIdentifiers":[{"caseId":"P01_Pri","molecularProfileId":"` + lggGA + `_mutational_signature_binary_SBS"}]}]`
	wantError(t, "POST", cat, binProfile, http.StatusNotFound, "Molecular profile not found: "+lggGA+"_mutational_signature_binary_SBS")
	catProfile := strings.ReplaceAll(binProfile, "mutational_signature_binary_SBS", "hla")
	wantError(t, "POST", bin, catProfile, http.StatusNotFound, "Molecular profile not found: "+lggGA+"_hla")
	wantError(t, "POST", bin+"?enrichmentType=sample", catProfile, http.StatusBadRequest,
		"Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.model.EnrichmentType.sample")
}
