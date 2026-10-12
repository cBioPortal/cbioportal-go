package contract

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// Contracts for the generic-assay data / meta endpoints, the numeric bin
// counts of the study view, the treatment tables and the clinical-data table.
// Expected values come from the fixture files (study_es_0,
// lgg_ucsf_2014_test_generic_assay) read through the Java backend's rules.

func javaKey(id, study string) string {
	return base64.RawStdEncoding.EncodeToString([]byte(id + ":" + study))
}

// postNumbers posts body and decodes the JSON reply keeping numbers as
// written (BigDecimal bin bounds keep their scale).
func postNumbers(t *testing.T, path string, body any, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, b := rawDo(t, http.MethodPost, path, string(raw))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s -> %d: %s", path, resp.StatusCode, b)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func esSamples(ids ...string) []map[string]string {
	out := make([]map[string]string, len(ids))
	for i, id := range ids {
		out[i] = map[string]string{"studyId": "study_es_0", "sampleId": id}
	}
	return out
}

// ------------------------------------------------- generic assay data ----

type gaDatum struct {
	GenericAssayStableID string `json:"genericAssayStableId"`
	StableID             string `json:"stableId"`
	MolecularProfileID   string `json:"molecularProfileId"`
	SampleID             string `json:"sampleId"`
	PatientID            string `json:"patientId"`
	StudyID              string `json:"studyId"`
	PatientLevel         bool   `json:"patientLevel"`
	Value                string `json:"value"`
	UniqueSampleKey      string `json:"uniqueSampleKey"`
	UniquePatientKey     string `json:"uniquePatientKey"`
}

func gaCells(rows []gaDatum) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SampleID + " " + r.GenericAssayStableID + " " + r.Value
	}
	return out
}

// GenericAssayServiceImpl.fetchGenericAssayData: samples in stable-id order
// (SampleMapper), each with the profile's entities in import order; values
// truncated to 4 decimals by the mapper; empty / NA cells dropped by the
// controller (Irinotecan is NA for TCGA-A1-A0SB-01).
func TestContractGenericAssayDataInProfile(t *testing.T) {
	requireAPI(t)
	var rows []gaDatum
	post(t, "/api/generic_assay_data/study_es_0_treatment_ic50/fetch", map[string]any{
		"sampleIds":             []string{"TCGA-A1-A0SD-01", "TCGA-A1-A0SB-01"},
		"genericAssayStableIds": []string{"Irinotecan", "AEW541", "17-AAG"},
	}, &rows)
	wantEq(t, gaCells(rows), []string{
		"TCGA-A1-A0SB-01 17-AAG 0.2281",
		"TCGA-A1-A0SB-01 AEW541 >8",
		"TCGA-A1-A0SD-01 17-AAG 0.3297",
		"TCGA-A1-A0SD-01 AEW541 2.3299",
		"TCGA-A1-A0SD-01 Irinotecan 0.0808",
	})
	wantEq(t, rows[0], gaDatum{
		GenericAssayStableID: "17-AAG", StableID: "17-AAG", MolecularProfileID: "study_es_0_treatment_ic50",
		SampleID: "TCGA-A1-A0SB-01", PatientID: "TCGA-A1-A0SB", StudyID: "study_es_0", Value: "0.2281",
		UniqueSampleKey: javaKey("TCGA-A1-A0SB-01", "study_es_0"), UniquePatientKey: javaKey("TCGA-A1-A0SB", "study_es_0"),
	})

	// Without a sample filter: every sample of the profile, in internal id
	// (sample file) order, as getSamplesByInternalIds returns them.
	get(t, "/api/generic-assay-data/study_es_0_treatment_ic50/generic-assay/17-AAG", &rows)
	wantEq(t, gaCells(rows), []string{
		"TCGA-A2-A04U-01 17-AAG 0.1501", "TCGA-BH-A18P-01 17-AAG 0.2795", "TCGA-B6-A0RS-01 17-AAG 0.4226",
		"TCGA-BH-A0HP-01 17-AAG 0.1518", "TCGA-A1-A0SE-01 17-AAG 0.053", "TCGA-A1-A0SD-01 17-AAG 0.3297",
		"TCGA-A1-A0SH-01 17-AAG 0.0708", "TCGA-A1-A0SB-01 17-AAG 0.2281",
	})
}

// A patient-level profile's matrix columns are patients; the importer links
// every sample of the patient, so each sample carries the patient's value.
func TestContractGenericAssayDataPatientLevel(t *testing.T) {
	requireAPI(t)
	var rows []gaDatum
	post(t, "/api/generic_assay_data/study_es_0_generic_assay_patient_test/fetch", map[string]any{
		"sampleListId":          "study_es_0_all",
		"genericAssayStableIds": []string{"test_patient_generic_assay_1"},
	}, &rows)
	wantEq(t, gaCells(rows), []string{
		"TCGA-A1-A0SB-01 test_patient_generic_assay_1 0.3703",
		"TCGA-A1-A0SB-02 test_patient_generic_assay_1 0.3703",
		"TCGA-A1-A0SD-01 test_patient_generic_assay_1 0.0104",
	})
	for _, r := range rows {
		if !r.PatientLevel {
			t.Errorf("%s: patientLevel false", r.SampleID)
		}
	}
	wantEq(t, rows[1].PatientID, "TCGA-A1-A0SB")
}

// The multi-profile form reads each requested sample from every requested
// profile of its study (Java pairs samples with profiles by study), and needs
// exactly one of molecularProfileIds / sampleMolecularIdentifiers.
func TestContractGenericAssayDataMultipleProfiles(t *testing.T) {
	requireAPI(t)
	var rows []gaDatum
	post(t, "/api/generic_assay_data/fetch", map[string]any{
		"sampleMolecularIdentifiers": []map[string]string{
			{"molecularProfileId": "study_es_0_treatment_ic50", "sampleId": "TCGA-A1-A0SD-01"},
			{"molecularProfileId": "study_es_0_treatment_ec50", "sampleId": "TCGA-A1-A0SB-01"},
		},
		"genericAssayStableIds": []string{"17-AAG"},
	}, &rows)
	var got []string
	for _, r := range rows {
		got = append(got, r.SampleID+" "+r.MolecularProfileID)
	}
	if len(rows) != 4 || rows[0].SampleID != "TCGA-A1-A0SB-01" || rows[1].SampleID != "TCGA-A1-A0SB-01" {
		t.Fatalf("samples not in stable-id order: %v", got)
	}
	sort.Strings(got)
	wantEq(t, got, []string{
		"TCGA-A1-A0SB-01 study_es_0_treatment_ec50", "TCGA-A1-A0SB-01 study_es_0_treatment_ic50",
		"TCGA-A1-A0SD-01 study_es_0_treatment_ec50", "TCGA-A1-A0SD-01 study_es_0_treatment_ic50",
	})

	for _, body := range []string{
		`{"genericAssayStableIds":["17-AAG"]}`,
		`{"molecularProfileIds":["study_es_0_treatment_ic50"],"sampleMolecularIdentifiers":[{"molecularProfileId":"study_es_0_treatment_ic50","sampleId":"TCGA-A1-A0SD-01"}]}`,
	} {
		resp, b := rawDo(t, http.MethodPost, "/api/generic_assay_data/fetch", body)
		wantEq(t, resp.StatusCode, http.StatusBadRequest)
		wantEq(t, len(b), 0)
	}
}

// ------------------------------------------------- generic assay meta ----

type gaMeta struct {
	StableID   string            `json:"stableId"`
	EntityType string            `json:"entityType"`
	Props      map[string]string `json:"genericEntityMetaProperties"`
}

// GenericAssayMapper orders entities by the number an id starts with, then
// by id (byte order), so 17-AAG comes last and LBW242 before Lapatinib.
func TestContractGenericAssayMeta(t *testing.T) {
	requireAPI(t)
	var metas []gaMeta
	get(t, "/api/generic-assay-meta/study_es_0_treatment_ic50", &metas)
	var ids []string
	for _, m := range metas {
		ids = append(ids, m.StableID)
	}
	wantEq(t, ids, []string{"AEW541", "AZD0530", "AZD6244", "Erlotinib", "Irinotecan", "L-685458", "LBW242", "Lapatinib", "Nilotinib", "17-AAG"})
	wantEq(t, metas[0], gaMeta{StableID: "AEW541", EntityType: "GENERIC_ASSAY",
		Props: map[string]string{"NAME": "Name of AEW541", "DESCRIPTION": "Desc of AEW541", "URL": "Url of AEW541"}})

	// The path segment is a molecular profile id only.
	get(t, "/api/generic-assay-meta/AEW541", &metas)
	wantEq(t, len(metas), 0)

	get(t, "/api/generic-assay-meta/generic-assay/AEW541", &metas)
	wantEq(t, len(metas), 1)
	wantEq(t, metas[0].StableID, "AEW541")

	// Neither profiles nor entities: nothing.
	post(t, "/api/generic-assay-meta/fetch", map[string]any{}, &metas)
	wantEq(t, len(metas), 0)
}

// ----------------------------------------- generic assay data counts ----

type gaCountItem struct {
	StableID string `json:"stableId"`
	Counts   []struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	} `json:"counts"`
}

func gaCountMap(it gaCountItem) map[string]int {
	m := map[string]int{}
	for _, c := range it.Counts {
		m[c.Value] = c.Count
	}
	return m
}

// mutational_signature_binary_SBS1 is true for 57 and false for 4 of the 61
// lgg samples. With entity filters getGenericAssayDataCounts appends an NA
// row (total - counted) even when it is 0; by profile type, NA rows appear
// only when positive.
func TestContractGenericAssayDataCounts(t *testing.T) {
	requireAPI(t)
	lgg := map[string]any{"studyIds": []string{"lgg_ucsf_2014_test_generic_assay"}}
	var items []gaCountItem
	post(t, "/api/generic-assay-data-counts/fetch", map[string]any{
		"genericAssayDataFilters": []map[string]string{{"stableId": "mutational_signature_binary_SBS1", "profileType": "mutational_signature_binary_SBS"}},
		"studyViewFilter":         lgg,
	}, &items)
	wantEq(t, len(items), 1)
	wantEq(t, gaCountMap(items[0]), map[string]int{"true": 57, "false": 4, "NA": 0})
	wantEq(t, items[0].Counts[len(items[0].Counts)-1].Value, "NA")

	post(t, "/api/generic-assay-data-counts/fetch", map[string]any{
		"genericAssayDataFilters": []map[string]string{{"stableId": "mutational_signature_binary_SBS2", "profileType": "mutational_signature_binary_SBS"}},
		"studyViewFilter":         lgg,
	}, &items)
	wantEq(t, gaCountMap(items[0]), map[string]int{"true": 7, "false": 54, "NA": 0})

	post(t, "/api/generic-assay-data-counts/fetch", map[string]any{
		"profileType": "mutational_signature_category_DBS", "studyViewFilter": lgg,
	}, &items)
	var ids []string
	for _, it := range items {
		ids = append(ids, it.StableID)
		if _, ok := gaCountMap(it)["NA"]; ok {
			t.Errorf("%s: NA row without NA samples", it.StableID)
		}
	}
	wantEq(t, ids[:4], []string{"mutational_signature_category_DBS1", "mutational_signature_category_DBS10",
		"mutational_signature_category_DBS11", "mutational_signature_category_DBS2"})
	wantEq(t, gaCountMap(items[0]), map[string]int{"highly significant": 47, "significant": 10, "not significant": 3, "marginally significant": 1})

	resp, b := rawDo(t, http.MethodPost, "/api/generic-assay-data-counts/fetch", `{"studyViewFilter":{"studyIds":["study_es_0"]}}`)
	wantEq(t, resp.StatusCode, http.StatusBadRequest)
	wantEq(t, len(b), 0)
}

// ------------------------------------------------------ bin counts ----

func binRows(bins []map[string]any, keep ...string) []string {
	var out []string
	for _, b := range bins {
		var parts []string
		for _, k := range append([]string{"specialValue", "start", "end", "count"}, keep...) {
			if v, ok := b[k]; ok {
				parts = append(parts, k+"="+toString(v))
			}
		}
		out = append(out, strings.Join(parts, " "))
	}
	return out
}

func toString(v any) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case string:
		return x
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Four samples, three with values: at most 10 distinct values make one bin
// per value (DiscreteDataBinner), ">8" becomes the upper outlier bin and the
// sample without a value the NA bin. Bounds keep the stored decimals.
func TestContractGenericAssayDataBinCounts(t *testing.T) {
	requireAPI(t)
	var bins []map[string]any
	postNumbers(t, "/api/generic-assay-data-bin-counts/fetch", map[string]any{
		"genericAssayDataBinFilters": []map[string]string{{"stableId": "AEW541", "profileType": "treatment_ic50"}},
		"studyViewFilter": map[string]any{"sampleIdentifiers": esSamples(
			"TCGA-A1-A0SB-01", "TCGA-A1-A0SD-01", "TCGA-A1-A0SE-01", "TCGA-A1-A0SB-02")},
	}, &bins)
	wantEq(t, binRows(bins, "stableId", "profileType"), []string{
		"start=2.329924107 end=2.329924107 count=1 stableId=AEW541 profileType=treatment_ic50",
		"start=2.68212986 end=2.68212986 count=1 stableId=AEW541 profileType=treatment_ic50",
		"specialValue=> start=8 count=1 stableId=AEW541 profileType=treatment_ic50",
		"specialValue=NA count=1 stableId=AEW541 profileType=treatment_ic50",
	})
}

func TestContractGenomicDataBinCounts(t *testing.T) {
	requireAPI(t)
	body := map[string]any{
		"genomicDataBinFilters": []map[string]string{{"hugoGeneSymbol": "MMP2", "profileType": "mrna_median_Zscores"}},
		"studyViewFilter": map[string]any{"sampleIdentifiers": esSamples(
			"TCGA-A1-A0SD-01", "TCGA-A1-A0SE-01", "TCGA-A1-A0SH-01", "TCGA-A1-A0SB-01")},
	}
	want := []string{
		"start=0.6180 end=0.6180 count=1 hugoGeneSymbol=MMP2 profileType=mrna_median_Zscores",
		"start=1.1741 end=1.1741 count=1 hugoGeneSymbol=MMP2 profileType=mrna_median_Zscores",
		"start=1.9648 end=1.9648 count=1 hugoGeneSymbol=MMP2 profileType=mrna_median_Zscores",
		"specialValue=NA count=1 hugoGeneSymbol=MMP2 profileType=mrna_median_Zscores",
	}
	for _, method := range []string{"", "?dataBinMethod=STATIC"} {
		var bins []map[string]any
		postNumbers(t, "/api/genomic-data-bin-counts/fetch"+method, body, &bins)
		wantEq(t, binRows(bins, "hugoGeneSymbol", "profileType"), want)
	}
}

// Each gene or generic assay entity of a bin-count request gets its own NA
// bin. Java's StudyViewMapper adds one NA bin for the whole request, to
// whichever gene or entity ClickHouse lists first (Fixed in go;
// docs/rewrite/java-quirks.md).
func TestBinCountsNAPerGene(t *testing.T) {
	requireAPI(t)
	samples := esSamples("TCGA-A1-A0SD-01", "TCGA-A1-A0SE-01", "TCGA-A1-A0SH-01", "TCGA-A1-A0SB-01")
	var bins []map[string]any
	postNumbers(t, "/api/genomic-data-bin-counts/fetch", map[string]any{
		"genomicDataBinFilters": []map[string]string{
			{"hugoGeneSymbol": "ZHX3", "profileType": "mrna_median_Zscores"},
			{"hugoGeneSymbol": "MMP2", "profileType": "mrna_median_Zscores"},
			{"hugoGeneSymbol": "BRCA1", "profileType": "mrna_median_Zscores"},
		},
		"studyViewFilter": map[string]any{"sampleIdentifiers": samples},
	}, &bins)
	var na []string
	for _, row := range binRows(bins, "hugoGeneSymbol") {
		if strings.HasPrefix(row, "specialValue=NA ") {
			na = append(na, row)
		}
	}
	wantEq(t, na, []string{
		"specialValue=NA count=1 hugoGeneSymbol=ZHX3",
		"specialValue=NA count=1 hugoGeneSymbol=MMP2",
		"specialValue=NA count=4 hugoGeneSymbol=BRCA1",
	})

	var gaBins []map[string]any
	postNumbers(t, "/api/generic-assay-data-bin-counts/fetch", map[string]any{
		"genericAssayDataBinFilters": []map[string]string{
			{"stableId": "AEW541", "profileType": "treatment_ic50"},
			{"stableId": "AZD0530", "profileType": "treatment_ic50"},
		},
		"studyViewFilter": map[string]any{"sampleIdentifiers": esSamples(
			"TCGA-A1-A0SB-01", "TCGA-A1-A0SD-01", "TCGA-A1-A0SE-01", "TCGA-A1-A0SB-02")},
	}, &gaBins)
	na = nil
	for _, row := range binRows(gaBins, "stableId") {
		if strings.HasPrefix(row, "specialValue=NA ") {
			na = append(na, row)
		}
	}
	wantEq(t, na, []string{"specialValue=NA count=1 stableId=AEW541", "specialValue=NA count=1 stableId=AZD0530"})
}

// Custom (session) numeric data binned over the filtered samples; the
// sample of the filter without a value is the NA bin.
func TestContractCustomDataBinCounts(t *testing.T) {
	requireAPI(t)
	var created struct {
		ID string `json:"id"`
	}
	post(t, "/api/session/custom_data", map[string]any{
		"displayName": "contract numeric", "datatype": "NUMBER", "patientAttribute": false,
		"origin": []string{"study_es_0"},
		"data": []map[string]any{
			{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SB-01", "patientId": "TCGA-A1-A0SB", "value": "10"},
			{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SD-01", "patientId": "TCGA-A1-A0SD", "value": "20"},
			{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SE-01", "patientId": "TCGA-A1-A0SE", "value": "20"},
		},
	}, &created)
	var bins []map[string]any
	postNumbers(t, "/api/custom-data-bin-counts/fetch", map[string]any{
		"attributes": []map[string]string{{"attributeId": created.ID}},
		"studyViewFilter": map[string]any{"sampleIdentifiers": esSamples(
			"TCGA-A1-A0SB-01", "TCGA-A1-A0SD-01", "TCGA-A1-A0SE-01", "TCGA-A1-A0SH-01")},
	}, &bins)
	wantEq(t, binRows(bins, "attributeId"), []string{
		"start=10 end=10 count=1 attributeId=" + created.ID,
		"start=20 end=20 count=2 attributeId=" + created.ID,
		"specialValue=NA count=1 attributeId=" + created.ID,
	})
}

// ------------------------------------------------------- treatments ----

type treatmentRow struct {
	Time      string           `json:"time"`
	Treatment string           `json:"treatment"`
	Count     int              `json:"count"`
	Samples   []map[string]any `json:"samples"`
}

func sampleSet(samples []map[string]any) []string {
	var out []string
	for _, s := range samples {
		v := s["sampleId"].(string)
		if tt, ok := s["timeTaken"]; ok {
			v += ":" + toString(tt)
		}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

var lggFilter = map[string]any{"studyIds": []string{"lgg_ucsf_2014_test_generic_assay"}}

// lgg's AGENT timeline: a sample is Pre a treatment when taken on or before
// the patient's first dose of it (TreatmentRowTriplet). P26_Pri_B's
// specimen event is recorded under P24. Rows come in the order of
// TreatmentServiceImpl's HashMap keyed by treatment + Pre/Post.
func TestContractTreatmentsSample(t *testing.T) {
	requireAPI(t)
	var rows []treatmentRow
	post(t, "/api/treatments/sample", lggFilter, &rows)
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Treatment+"|"+r.Time)
		if r.Count != len(r.Samples) {
			t.Errorf("%s %s: count %d, %d samples", r.Treatment, r.Time, r.Count, len(r.Samples))
		}
	}
	wantEq(t, keys, []string{"CCNU + thalidomide|Pre", "TMZ + accutane|Pre", "TMZ + accutane|Post",
		"TMZ + thalidomide|Pre", "TMZ|Post", "TMZ|Pre", "TMZ + thalidomide|Post"})
	byKey := map[string][]string{}
	for _, r := range rows {
		byKey[r.Treatment+"|"+r.Time] = sampleSet(r.Samples)
	}
	wantEq(t, byKey["CCNU + thalidomide|Pre"], []string{"P01_Pri:124", "P01_Rec:1063"})
	wantEq(t, byKey["TMZ|Post"], []string{"P01_Rec:1063", "P04_Rec2:1075", "P04_Rec3:1343", "P05_Rec:1687",
		"P06_Pri:574", "P06_Rec:1545", "P10_Rec:758", "P11_Rec:3954", "P17_Rec1_A:1214", "P17_Rec1_B:1214",
		"P17_Rec1_C:1214", "P17_Rec1_D:1214", "P18_Rec:2829", "P21_Rec:628", "P24_Rec2:3045"})
	wantEq(t, byKey["TMZ|Pre"], []string{"P01_Pri:124", "P04_Pri:0", "P04_Rec1:461", "P05_Pri:0", "P10_Pri:0",
		"P11_Pri:0", "P17_Pri_A:311", "P17_Pri_B:311", "P17_Pri_C:311", "P18_Pri_A:0", "P18_Pri_B:0",
		"P18_Pri_C:0", "P18_Pri_D:0", "P21_Pri:0", "P24_Pri:0", "P24_Rec1:2630", "P26_Pri_B:0"})
	for _, s := range rows[0].Samples {
		if s["patientId"] != "P01" || s["studyId"] != "lgg_ucsf_2014_test_generic_assay" {
			t.Errorf("sample %v", s)
		}
	}
}

// Patient rows: the number of treated patients and all filtered samples of
// those patients, without acquisition times (getAllShallowSamples).
func TestContractTreatmentsPatient(t *testing.T) {
	requireAPI(t)
	var rows []treatmentRow
	post(t, "/api/treatments/patient", lggFilter, &rows)
	var got []string
	for _, r := range rows {
		got = append(got, r.Treatment+"|"+toString(r.Count))
	}
	wantEq(t, got, []string{"TMZ|10", "CCNU + thalidomide|1", "TMZ + accutane|1", "TMZ + thalidomide|1"})
	wantEq(t, sampleSet(rows[1].Samples), []string{"P01_Pri", "P01_Rec"})

	resp, body := rawDo(t, http.MethodPost, "/api/treatments/patient?tier=Drug", `{}`)
	wantEq(t, resp.StatusCode, http.StatusBadRequest)
	wantEq(t, string(bytes.TrimSpace(body)),
		`{"message":"Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.model.ClinicalEventKeyCode.Drug"}`)

	// No AGENT_CLASS events in the fixtures.
	post(t, "/api/treatments/patient?tier=AgentClass", lggFilter, &rows)
	wantEq(t, len(rows), 0)
}

// ClickhouseTreatmentMapper.getSampleTreatmentCounts: per treatment a Pre
// and a Post row (zero rows dropped); samples only with DETAILED. The total
// is the distinct acquisition samples of treated patients.
func TestContractTreatmentSampleCounts(t *testing.T) {
	requireAPI(t)
	var rep struct {
		TotalSamples int            `json:"totalSamples"`
		Treatments   []treatmentRow `json:"treatments"`
	}
	post(t, "/api/treatments/sample-counts/fetch", lggFilter, &rep)
	wantEq(t, rep.TotalSamples, 32)
	counts := map[string]int{}
	for i, r := range rep.Treatments {
		counts[r.Treatment+"|"+r.Time] = r.Count
		if r.Samples == nil || len(r.Samples) != 0 {
			t.Errorf("SUMMARY samples %v", r.Samples)
		}
		if r.Time == "Post" && i > 0 && rep.Treatments[i-1].Treatment == r.Treatment && rep.Treatments[i-1].Time != "Pre" {
			t.Errorf("row %d: Post not after Pre", i)
		}
	}
	wantEq(t, counts, map[string]int{"CCNU + thalidomide|Pre": 2, "TMZ|Pre": 17, "TMZ|Post": 15,
		"TMZ + accutane|Pre": 1, "TMZ + accutane|Post": 1, "TMZ + thalidomide|Pre": 1, "TMZ + thalidomide|Post": 1})

	post(t, "/api/treatments/sample-counts/fetch?projection=DETAILED", lggFilter, &rep)
	for _, r := range rep.Treatments {
		if r.Treatment == "CCNU + thalidomide" {
			wantEq(t, sampleSet(r.Samples), []string{"P01_Pri:124", "P01_Rec:1063"})
		}
	}
}

// -------------------------------------------------- clinical-data table ----

// StudyViewController.fetchClinicalDataClinicalTable: samples paged by the
// sort attribute (NUMBER attributes numerically, missing values last), keyed
// by unique sample key, cells without unique keys or attribute metadata
// (toDtosForTable); the total-count header counts all matching samples.
func TestContractClinicalDataTable(t *testing.T) {
	requireAPI(t)
	raw, _ := json.Marshal(lggFilter)
	resp, body := rawDo(t, http.MethodPost,
		"/api/clinical-data-table/fetch?pageSize=3&pageNumber=0&sortBy=MUTATION_COUNT&direction=DESC", string(raw))
	wantEq(t, resp.StatusCode, http.StatusOK)
	wantEq(t, resp.Header.Get("total-count"), "61")
	var table struct {
		ByUniqueSampleKey map[string][]map[string]any `json:"byUniqueSampleKey"`
	}
	if err := json.Unmarshal(body, &table); err != nil {
		t.Fatal(err)
	}
	wantEq(t, len(table.ByUniqueSampleKey), 3)
	top := table.ByUniqueSampleKey[javaKey("P10_Rec", "lgg_ucsf_2014_test_generic_assay")]
	if len(top) == 0 {
		t.Fatalf("P10_Rec (most mutations) not on the first page: %v", table.ByUniqueSampleKey)
	}
	fields := map[string]bool{}
	attrs := map[string]bool{}
	for _, cell := range top {
		for k := range cell {
			fields[k] = true
		}
		attrs[cell["clinicalAttributeId"].(string)] = true
	}
	wantEq(t, fields, map[string]bool{"sampleId": true, "patientId": true, "studyId": true, "clinicalAttributeId": true, "value": true})
	// Sample and patient attributes both.
	if !attrs["GRADE"] || !attrs["AGE"] {
		t.Errorf("attributes %v", attrs)
	}

	// The search is a case-sensitive LIKE.
	for term, want := range map[string]string{"P01_Pri": "1", "p01_pri": "0"} {
		resp, _ := rawDo(t, http.MethodPost, "/api/clinical-data-table/fetch?searchTerm="+term, string(raw))
		wantEq(t, resp.Header.Get("total-count"), want)
	}
}
