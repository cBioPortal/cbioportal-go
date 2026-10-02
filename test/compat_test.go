package contract

import (
	"net/http"
	"strings"
	"testing"
)

func TestContractLegacyAliasMatches(t *testing.T) {
	requireAPI(t)
	var direct, legacy []map[string]any
	body := es0(nil)
	post(t, "/api/filtered-samples/fetch", body, &direct)
	post(t, "/api/legacy/filtered-samples/fetch", body, &legacy)
	wantEq(t, len(legacy), len(direct))
}

func TestContractMrnaPercentile(t *testing.T) {
	requireAPI(t)
	var d []struct {
		EntrezGeneID int64   `json:"entrezGeneId"`
		Percentile   float64 `json:"percentile"`
		PatientID    string  `json:"patientId"`
	}
	post(t, "/api/molecular-profiles/study_es_0_mrna_median_Zscores/mrna-percentile/fetch?sampleId=TCGA-A1-A0SD-01",
		[]int{6205}, &d)
	if len(d) != 1 || d[0].Percentile <= 0 || d[0].Percentile > 100 || d[0].PatientID == "" {
		t.Fatalf("unexpected percentile rows: %+v", d)
	}
}

func TestContractMolecularDataGet(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	get(t, "/api/molecular-profiles/study_es_0_gistic/molecular-data?sampleListId=study_es_0_all&entrezGeneId=2073", &d)
	if len(d) == 0 || d[0]["uniquePatientKey"] == "" {
		t.Fatalf("unexpected molecular data: %v", d)
	}
}

func TestContractStudyDataTypeCounts(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	get(t, "/api/studies?projection=DETAILED", &d)
	for _, st := range d {
		if st["studyId"] != "study_es_0" {
			continue
		}
		wantEq(t, []any{st["sequencedSampleCount"], st["cnaSampleCount"], st["referenceGenome"], len(st["resourceCounts"].([]any))},
			[]any{31.0, 788.0, "hg19", 2})
		return
	}
	t.Fatal("study_es_0 missing")
}

func TestContractMutationMetaSampleCount(t *testing.T) {
	requireAPI(t)
	body := `{"sampleListId":"study_es_0_cnaseq","entrezGeneIds":[7157]}`
	resp, err := http.Post(apiBase()+"/api/molecular-profiles/study_es_0_mutations/mutations/fetch?projection=META",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	wantEq(t, []string{resp.Header.Get("total-count"), resp.Header.Get("sample-count")}, []string{"2", "1"})
}

func TestContractGenericAssayEnrichments(t *testing.T) {
	requireAPI(t)
	var samples []struct {
		SampleID string `json:"sampleId"`
	}
	post(t, "/api/samples/fetch", map[string]any{"sampleListIds": []string{"study_es_0_all"}}, &samples)
	group := func(name string, from, to int) map[string]any {
		var ids []map[string]any
		for _, s := range samples[from:to] {
			ids = append(ids, map[string]any{"caseId": s.SampleID, "molecularProfileId": "study_es_0_treatment_ic50"})
		}
		return map[string]any{"name": name, "molecularProfileCaseIdentifiers": ids}
	}
	var d []map[string]any
	post(t, "/api/generic-assay-enrichments/fetch?enrichmentType=SAMPLE",
		[]map[string]any{group("A", 0, 400), group("B", 400, len(samples))}, &d)
	if len(d) == 0 {
		t.Fatal("no generic assay enrichments")
	}
}
