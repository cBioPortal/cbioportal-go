package contract

import (
	"encoding/json"
	"net/http"
	"testing"
)

type resourceDefinition struct {
	ResourceID     string  `json:"resourceId"`
	DisplayName    string  `json:"displayName"`
	Description    *string `json:"description"`
	ResourceType   *string `json:"resourceType"`
	Priority       *string `json:"priority"`
	OpenByDefault  *bool   `json:"openByDefault"`
	StudyID        string  `json:"studyId"`
	CustomMetaData *string `json:"customMetaData"`
}

type resourceData struct {
	UniqueSampleKey    string              `json:"uniqueSampleKey"`
	UniquePatientKey   string              `json:"uniquePatientKey"`
	SampleID           string              `json:"sampleId"`
	PatientID          string              `json:"patientId"`
	StudyID            string              `json:"studyId"`
	ResourceID         string              `json:"resourceId"`
	URL                *string             `json:"url"`
	ResourceDefinition *resourceDefinition `json:"resourceDefinition"`
}

// study_es_0 ships data_resource_definition.txt with three definitions
// (PATHOLOGY_SLIDE/SAMPLE/TRUE/1, PATIENT_NOTES/PATIENT/FALSE/2,
// STUDY_SPONSORS/STUDY/TRUE/3); priority serializes as a string.
func TestContractResourceDefinitions(t *testing.T) {
	requireAPI(t)
	var defs []resourceDefinition
	get(t, "/api/studies/study_es_0/resource-definitions", &defs)
	if len(defs) != 3 {
		t.Fatalf("want 3 resource definitions, got %d", len(defs))
	}
	type summary struct{ id, typ, prio string }
	var got []summary
	for _, d := range defs {
		if d.ResourceType == nil || d.Priority == nil || d.OpenByDefault == nil || d.Description == nil {
			t.Fatalf("SUMMARY projection missing fields: %+v", d)
		}
		got = append(got, summary{d.ResourceID, *d.ResourceType, *d.Priority})
	}
	wantEq(t, got, []summary{{"PATHOLOGY_SLIDE", "SAMPLE", "1"}, {"PATIENT_NOTES", "PATIENT", "2"}, {"STUDY_SPONSORS", "STUDY", "3"}})
	if !*defs[0].OpenByDefault || *defs[1].OpenByDefault {
		t.Errorf("openByDefault: want true/false, got %v/%v", *defs[0].OpenByDefault, *defs[1].OpenByDefault)
	}
	if defs[0].CustomMetaData != nil {
		t.Errorf("customMetaData should be omitted when absent")
	}

	var ids []map[string]any
	get(t, "/api/studies/study_es_0/resource-definitions?projection=ID", &ids)
	if len(ids) != 3 || len(ids[0]) != 3 {
		t.Errorf("ID projection: want 3 rows of {resourceId, displayName, studyId}, got %v", ids)
	}

	var one resourceDefinition
	get(t, "/api/studies/study_es_0/resource-definitions/PATIENT_NOTES", &one)
	if one.DisplayName != "Patient Notes" || one.ResourceType == nil || *one.ResourceType != "PATIENT" {
		t.Errorf("single definition: got %+v", one)
	}
	wantEq(t, statusOf(t, "/api/studies/study_es_0/resource-definitions/NO_SUCH_RESOURCE"), http.StatusNotFound)
	wantEq(t, statusOf(t, "/api/studies/no_such_study/resource-definitions"), http.StatusNotFound)

	var fetched []resourceDefinition
	post(t, "/api/resource-definitions/fetch", []string{"study_es_0"}, &fetched)
	wantEq(t, len(fetched), 3)
}

func TestContractResourceData(t *testing.T) {
	requireAPI(t)
	var study []resourceData
	get(t, "/api/studies/study_es_0/resource-data", &study)
	if len(study) != 1 || study[0].ResourceID != "STUDY_SPONSORS" || study[0].URL == nil ||
		*study[0].URL != "http://url-to-study-sponsors" || study[0].PatientID != "" || study[0].SampleID != "" {
		t.Errorf("study resource data: got %+v", study)
	}

	var all []resourceData
	get(t, "/api/studies/study_es_0/resource-data-all?projection=DETAILED", &all)
	counts := map[string]int{}
	for _, d := range all {
		counts[d.ResourceID]++
		if d.ResourceDefinition == nil || d.ResourceDefinition.ResourceID != d.ResourceID {
			t.Errorf("DETAILED projection must nest the definition: %+v", d)
		}
	}
	wantEq(t, counts, map[string]int{"STUDY_SPONSORS": 1, "PATIENT_NOTES": 3, "PATHOLOGY_SLIDE": 3})
	if len(all) > 0 && all[0].ResourceID != "STUDY_SPONSORS" {
		t.Errorf("resource-data-all lists study-level resources first, got %s", all[0].ResourceID)
	}

	var patient []resourceData
	get(t, "/api/studies/study_es_0/patients/TCGA-A1-A0SK/resource-data", &patient)
	if len(patient) != 1 || patient[0].PatientID != "TCGA-A1-A0SK" || patient[0].UniquePatientKey == "" ||
		patient[0].UniqueSampleKey != "" || *patient[0].URL != "http://url-to-patient-notes-patient2" {
		t.Errorf("patient resource data: got %+v", patient)
	}

	var sample []resourceData
	get(t, "/api/studies/study_es_0/samples/TCGA-A1-A0SK-01/resource-data?resourceId=PATHOLOGY_SLIDE", &sample)
	if len(sample) != 1 || sample[0].SampleID != "TCGA-A1-A0SK-01" || sample[0].PatientID != "TCGA-A1-A0SK" ||
		sample[0].UniqueSampleKey == "" || *sample[0].URL != "http://url-to-slide-sample2" {
		t.Errorf("sample resource data: got %+v", sample)
	}
	var none []resourceData
	get(t, "/api/studies/study_es_0/samples/TCGA-A1-A0SK-01/resource-data?resourceId=PATIENT_NOTES", &none)
	wantEq(t, len(none), 0)
	wantEq(t, statusOf(t, "/api/studies/study_es_0/samples/NO_SUCH_SAMPLE/resource-data"), http.StatusNotFound)
}

// study_es_0 ships data_gistic_genes_amp.txt: 11 amplified regions.
func TestContractSignificantCopyNumberRegions(t *testing.T) {
	requireAPI(t)
	type gene struct {
		EntrezGeneID   int    `json:"entrezGeneId"`
		HugoGeneSymbol string `json:"hugoGeneSymbol"`
	}
	type gistic struct {
		StudyID       string  `json:"studyId"`
		Chromosome    int     `json:"chromosome"`
		Cytoband      string  `json:"cytoband"`
		WidePeakStart int     `json:"widePeakStart"`
		WidePeakEnd   int     `json:"widePeakEnd"`
		QValue        float64 `json:"qValue"`
		Amp           bool    `json:"amp"`
		Genes         []gene  `json:"genes"`
	}
	var regions []gistic
	get(t, "/api/studies/study_es_0/significant-copy-number-regions", &regions)
	if len(regions) != 11 {
		t.Fatalf("want 11 GISTIC regions, got %d", len(regions))
	}
	var erbb2 *gistic
	for i := range regions {
		if !regions[i].Amp || regions[i].StudyID != "study_es_0" || len(regions[i].Genes) == 0 {
			t.Errorf("region shape: %+v", regions[i])
		}
		if regions[i].Cytoband == "17q12" {
			erbb2 = &regions[i]
		}
	}
	if erbb2 == nil {
		t.Fatal("17q12 region missing")
	}
	// The importer parses q-values as Java floats: 8.5344e-137 reads 0.
	wantEq(t, []any{erbb2.Chromosome, erbb2.WidePeakStart, erbb2.WidePeakEnd, erbb2.QValue},
		[]any{17, 37863585, 37870539, 0.0})
	found := false
	for _, g := range erbb2.Genes {
		found = found || (g.HugoGeneSymbol == "ERBB2" && g.EntrezGeneID == 2064)
	}
	if !found {
		t.Errorf("17q12 region should contain ERBB2 (2064): %+v", erbb2.Genes)
	}

	var ids []map[string]any
	get(t, "/api/studies/study_es_0/significant-copy-number-regions?projection=ID&pageSize=3", &ids)
	if len(ids) != 3 || ids[0]["genes"] != nil {
		t.Errorf("ID projection with pageSize=3: want 3 rows without genes, got %v", ids)
	}

	resp, err := http.Get(apiBase() + "/api/studies/study_es_0/significant-copy-number-regions?projection=META")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	wantEq(t, resp.Header.Get("total-count"), "11")
	wantEq(t, statusOf(t, "/api/studies/no_such_study/significant-copy-number-regions"), http.StatusNotFound)
}

// study_es_0 has no MutSig file: GET returns [] (and the endpoint is a GET).
func TestContractSignificantlyMutatedGenes(t *testing.T) {
	requireAPI(t)
	var rows []map[string]any
	get(t, "/api/studies/study_es_0/significantly-mutated-genes", &rows)
	wantEq(t, len(rows), 0)
	wantEq(t, statusOf(t, "/api/studies/no_such_study/significantly-mutated-genes"), http.StatusNotFound)
}

// study_es_0's meta_study.txt points tags_file at study_tags.yml.
func TestContractStudyTags(t *testing.T) {
	requireAPI(t)
	var tags map[string]any
	get(t, "/api/studies/study_es_0/tags", &tags)
	wantEq(t, tags["Load id"], 34.0)
	wantEq(t, tags["Analyst"], map[string]any{"name": "Jack", "email": "jack@xyz.com"})
	sponsors, _ := tags["Study sponsors"].([]any)
	wantEq(t, len(sponsors), 2)

	var empty map[string]any
	get(t, "/api/studies/no_such_study/tags", &empty)
	wantEq(t, len(empty), 0)

	var fetched []struct {
		StudyID string `json:"studyId"`
		Tags    string `json:"tags"`
	}
	post(t, "/api/studies/tags/fetch", []string{"study_es_0", "no_such_study"}, &fetched)
	if len(fetched) != 1 || fetched[0].StudyID != "study_es_0" {
		t.Fatalf("tags fetch: got %+v", fetched)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(fetched[0].Tags), &parsed); err != nil || parsed["Loaded by"] != "Jill" {
		t.Errorf("tags must be a JSON string of the YAML document: %q (%v)", fetched[0].Tags, err)
	}
}

// study_es_0 ships data_cancer_type.txt defining brca-es0.
func TestContractStudyCancerType(t *testing.T) {
	requireAPI(t)
	type cancerType struct {
		CancerTypeID   string `json:"cancerTypeId"`
		Name           string `json:"name"`
		DedicatedColor string `json:"dedicatedColor"`
		ShortName      string `json:"shortName"`
		Parent         string `json:"parent"`
	}
	var ct cancerType
	get(t, "/api/cancer-types/brca-es0", &ct)
	wantEq(t, ct, cancerType{"brca-es0", "Breast Invasive Carcinoma", "HotPink", "brca-es0", "breast"})

	var all []cancerType
	get(t, "/api/cancer-types", &all)
	n := 0
	for _, c := range all {
		if c.CancerTypeID == "brca-es0" {
			n++
		}
	}
	wantEq(t, n, 1)
	wantEq(t, statusOf(t, "/api/cancer-types/no_such_cancer_type"), http.StatusNotFound)

	var study struct {
		CancerType cancerType `json:"cancerType"`
	}
	get(t, "/api/studies/study_es_0", &study)
	wantEq(t, study.CancerType.Name, "Breast Invasive Carcinoma")
}

// The resource table endpoints over study_es_0, whose resource files carry
// no display names or metadata.
func TestContractResourceTable(t *testing.T) {
	requireAPI(t)
	type tab struct {
		ResourceID   string `json:"resourceId"`
		Label        string `json:"label"`
		TotalCount   int    `json:"totalCount"`
		PatientCount int    `json:"patientCount"`
		SampleCount  int    `json:"sampleCount"`
	}
	var tabs []tab
	post(t, "/api/resource-table/tabs/fetch", map[string]any{"studyIds": []string{"study_es_0"}}, &tabs)
	want := []tab{
		{"PATHOLOGY_SLIDE", "Pathology Slide", 3, 3, 3},
		{"PATIENT_NOTES", "Patient Notes", 3, 3, 0},
		{"STUDY_SPONSORS", "Study Sponsors", 1, 0, 0},
	}
	if len(tabs) != len(want) {
		t.Fatalf("tabs %+v", tabs)
	}
	for i := range want {
		if tabs[i] != want[i] {
			t.Errorf("tab %d: got %+v, want %+v", i, tabs[i], want[i])
		}
	}

	query := map[string]any{"studyIds": []string{"study_es_0"}, "resourceId": "PATHOLOGY_SLIDE", "pageNumber": 0, "pageSize": 2}
	var rows []map[string]any
	post(t, "/api/resource-table/query/fetch", query, &rows)
	if len(rows) != 2 || rows[0]["patientId"] != "TCGA-A1-A0SK" || rows[0]["url"] != "http://url-to-slide-sample2" ||
		rows[0]["resourceDisplayName"] != "Pathology Slide" || rows[0]["resourceType"] != "SAMPLE" || rows[1]["patientId"] != "TCGA-A2-A04P" {
		t.Errorf("rows %v", rows)
	}
	if _, ok := rows[0]["displayName"]; ok {
		t.Errorf("a row without a display name leaves it out: %v", rows[0])
	}
	if m, ok := rows[0]["metadata"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("a row without metadata has metadata {}: %v", rows[0])
	}

	var meta struct {
		Columns []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"columns"`
		TotalRowCount        int            `json:"totalRowCount"`
		FilteredPatientCount int            `json:"filteredPatientCount"`
		FilteredSampleCount  int            `json:"filteredSampleCount"`
		Facets               map[string]any `json:"facets"`
		FacetRanges          map[string]any `json:"facetRanges"`
		DistinctValueCounts  map[string]int `json:"distinctValueCounts"`
	}
	query["filters"] = []map[string]any{{"columnId": "sampleId", "operator": "in", "values": []string{"TCGA-A2-A04P-01"}}}
	post(t, "/api/resource-table/metadata/fetch", query, &meta)
	if len(meta.Columns) != 4 || meta.Columns[0].ID != "patientId" || meta.Columns[3].ID != "displayName" ||
		meta.TotalRowCount != 1 || meta.FilteredPatientCount != 1 || meta.FilteredSampleCount != 1 ||
		meta.Facets == nil || len(meta.Facets) != 0 || meta.FacetRanges == nil ||
		meta.DistinctValueCounts["resourceType"] != 1 || meta.DistinctValueCounts["displayName"] != 1 {
		t.Errorf("metadata %+v", meta)
	}
}
