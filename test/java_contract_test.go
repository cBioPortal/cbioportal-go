package contract

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"testing"
)

// Contracts for the Java request handling of the core resources: unique
// keys, projections, the {"message": ...} error bodies of
// GlobalExceptionHandler and request validation.

// wantError asserts a status and Java ErrorResponse message.
func wantError(t *testing.T, method, path, body string, status int, msg string) {
	t.Helper()
	resp, b := rawDo(t, method, path, body)
	var e struct {
		Message string `json:"message"`
	}
	json.Unmarshal(b, &e)
	if resp.StatusCode != status || e.Message != msg {
		t.Errorf("%s %s: %d %s, want %d %q", method, path, resp.StatusCode, b, status, msg)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s %s: content-type %q", method, path, ct)
	}
}

// keys returns the sorted top-level keys of an object.
func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestContractUniqueKeysAreIdColonStudy(t *testing.T) {
	requireAPI(t)
	var samples []map[string]any
	get(t, "/api/studies/study_es_0/samples?pageSize=1", &samples)
	wantEq(t, samples[0]["uniqueSampleKey"], base64.RawStdEncoding.EncodeToString([]byte("TCGA-A1-A0SB-01:study_es_0")))
	wantEq(t, samples[0]["uniquePatientKey"], base64.RawStdEncoding.EncodeToString([]byte("TCGA-A1-A0SB:study_es_0")))

	var patients []map[string]any
	get(t, "/api/studies/study_es_0/patients?pageSize=1", &patients)
	if _, ok := patients[0]["uniqueSampleKey"]; ok {
		t.Errorf("patient carries a uniqueSampleKey: %v", patients[0])
	}
}

func TestContractSampleProjections(t *testing.T) {
	requireAPI(t)
	want := map[string][]string{
		"ID":       {"patientId", "sampleId", "studyId", "uniquePatientKey", "uniqueSampleKey"},
		"SUMMARY":  {"patientId", "sampleId", "sampleType", "studyId", "uniquePatientKey", "uniqueSampleKey"},
		"DETAILED": {"copyNumberSegmentPresent", "patientId", "sampleId", "sampleType", "sequenced", "studyId", "uniquePatientKey", "uniqueSampleKey"},
	}
	for proj, fields := range want {
		var d []map[string]any
		d = nil
		get(t, "/api/studies/study_es_0/samples?pageSize=1&projection="+proj, &d)
		wantEq(t, keys(d[0]), fields)
		d = nil
		post(t, "/api/samples/fetch?projection="+proj, map[string]any{"sampleListIds": []string{"study_es_0_all"}}, &d)
		wantEq(t, keys(d[0]), fields)
	}
	var one map[string]any
	get(t, "/api/studies/study_es_0/samples/TCGA-A1-A0SB-01", &one)
	wantEq(t, keys(one), want["DETAILED"])
}

func TestContractStudyProjections(t *testing.T) {
	requireAPI(t)
	var ids []map[string]any
	get(t, "/api/studies?projection=ID", &ids)
	wantEq(t, len(ids), 0)

	var summary []map[string]any
	get(t, "/api/studies?keyword=study_es_0&projection=SUMMARY", &summary)
	for _, k := range []string{"cancerType", "sequencedSampleCount", "treatmentCount"} {
		if _, ok := summary[0][k]; ok {
			t.Errorf("SUMMARY study has DETAILED field %s", k)
		}
	}
	for _, k := range []string{"importDate", "allSampleCount", "resourceCounts", "readPermission"} {
		if _, ok := summary[0][k]; !ok {
			t.Errorf("SUMMARY study lacks %s", k)
		}
	}

	var study map[string]any
	get(t, "/api/studies/study_es_0", &study)
	ct, _ := study["cancerType"].(map[string]any)
	wantEq(t, keys(ct), []string{"dedicatedColor", "id", "name", "parent", "shortName"})
	if _, ok := study["sequencedSampleCount"]; !ok {
		t.Error("study lacks sequencedSampleCount")
	}

	var fetched []map[string]any
	fetched = nil
	post(t, "/api/studies/fetch?projection=ID", []string{"study_es_0"}, &fetched)
	wantEq(t, keys(fetched[0]), []string{"readPermission", "studyId"})
	fetched = nil
	post(t, "/api/studies/fetch?projection=DETAILED", []string{"study_es_0"}, &fetched)
	ct, _ = fetched[0]["cancerType"].(map[string]any)
	wantEq(t, keys(ct), []string{"cancerTypeId", "dedicatedColor", "name", "parent", "shortName"})

	resp, body := rawDo(t, "GET", "/api/studies?projection=META", "")
	wantEq(t, string(body), "[]")
	if resp.Header.Get("total-count") == "" || resp.Header.Get("sample-count") != "" {
		t.Errorf("studies META headers: %v", resp.Header)
	}
}

func TestContractPatientProjections(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	d = nil
	get(t, "/api/studies/study_es_0/patients?pageSize=1&projection=DETAILED", &d)
	cs, _ := d[0]["cancerStudy"].(map[string]any)
	wantEq(t, cs["studyId"], "study_es_0")
	d = nil
	get(t, "/api/studies/study_es_0/patients?pageSize=1", &d)
	wantEq(t, keys(d[0]), []string{"patientId", "studyId", "uniquePatientKey"})

	var one map[string]any
	get(t, "/api/studies/study_es_0/patients/TCGA-A1-A0SB", &one)
	if one["cancerStudy"] == nil {
		t.Errorf("single patient lacks cancerStudy: %v", one)
	}
}

func TestContractOtherProjections(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	d = nil
	get(t, "/api/studies/study_es_0/sample-lists?projection=ID", &d)
	wantEq(t, keys(d[0]), []string{"sampleListId"})
	d = nil
	get(t, "/api/studies/study_es_0/sample-lists", &d)
	wantEq(t, keys(d[0]), []string{"category", "description", "name", "sampleListId", "studyId"})

	d = nil
	post(t, "/api/clinical-attributes/fetch?projection=ID", []string{"study_es_0"}, &d)
	wantEq(t, keys(d[0]), []string{"clinicalAttributeId", "displayName", "patientAttribute", "studyId"})

	d = nil
	get(t, "/api/studies/study_es_0/clinical-data?projection=ID&pageSize=1", &d)
	wantEq(t, keys(d[0]), []string{"clinicalAttributeId", "patientId", "sampleId", "studyId", "uniquePatientKey", "uniqueSampleKey"})
	d = nil
	get(t, "/api/studies/study_es_0/clinical-data?pageSize=1", &d)
	if _, ok := d[0]["patientAttribute"]; ok {
		t.Errorf("SUMMARY clinical data has patientAttribute: %v", d[0])
	}
	d = nil
	get(t, "/api/studies/study_es_0/clinical-data?pageSize=1&projection=DETAILED", &d)
	if d[0]["clinicalAttribute"] == nil || d[0]["patientAttribute"] != false {
		t.Errorf("DETAILED clinical data: %v", d[0])
	}

	d = nil
	get(t, "/api/studies/study_es_0/molecular-profiles?projection=ID", &d)
	wantEq(t, keys(d[0]), []string{"molecularProfileId", "studyId"})
	d = nil
	get(t, "/api/studies/study_es_0/molecular-profiles?projection=DETAILED", &d)
	if d[0]["study"] == nil {
		t.Errorf("DETAILED molecular profile lacks study: %v", d[0])
	}

	var genes []map[string]any
	genes = nil
	post(t, "/api/genes/fetch?geneIdType=HUGO_GENE_SYMBOL", []string{"TP53"}, &genes)
	wantEq(t, keys(genes[0]), []string{"entrezGeneId", "hugoGeneSymbol", "type"})
	genes = nil
	post(t, "/api/genes/fetch?geneIdType=HUGO_GENE_SYMBOL&projection=ID", []string{"TP53"}, &genes)
	wantEq(t, keys(genes[0]), []string{"entrezGeneId", "hugoGeneSymbol"})

	d = nil
	get(t, "/api/gene-panels", &d)
	wantEq(t, keys(d[0]), []string{"description", "genePanelId"})
}

func TestContractNotFoundMessages(t *testing.T) {
	requireAPI(t)
	cases := []struct{ path, msg string }{
		{"/api/studies/no_such_study", "Study not found: no_such_study"},
		{"/api/studies/no_such_study/samples", "Study not found: no_such_study"},
		{"/api/studies/no_such_study/clinical-attributes", "Study not found: no_such_study"},
		{"/api/studies/study_es_0/samples/NOPE", "Sample not found in study study_es_0: NOPE"},
		{"/api/studies/study_es_0/patients/NOPE", "Patient not found in study study_es_0: NOPE"},
		{"/api/studies/study_es_0/clinical-attributes/NOPE", "Clinical attribute not found in study study_es_0: NOPE"},
		{"/api/genes/NOTAGENE123", "Gene not found: NOTAGENE123"},
		{"/api/molecular-profiles/no_such_profile", "Molecular profile not found: no_such_profile"},
		{"/api/sample-lists/no_such_list", "Sample list not found: no_such_list"},
		{"/api/cancer-types/no_such_type", "Cancer type not found: no_such_type"},
		{"/api/gene-panels/no_such_panel", "Gene panel not found: no_such_panel"},
	}
	for _, c := range cases {
		wantError(t, "GET", c.path, "", 404, c.msg)
	}
	wantError(t, "POST", "/api/molecular-profiles/no_such_prof/mutations/fetch",
		`{"sampleListId":"study_es_0_all","entrezGeneIds":[7157]}`, 404, "Molecular profile not found: no_such_prof")
}

func TestContractRequestValidation(t *testing.T) {
	requireAPI(t)
	const enum = "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant "
	wantError(t, "GET", "/api/studies/study_es_0/samples?projection=BOGUS", "", 400, enum+"org.cbioportal.shared.enums.ProjectionType.BOGUS")
	wantError(t, "GET", "/api/studies/study_es_0/patients?projection=BOGUS", "", 400, enum+"org.cbioportal.legacy.web.parameter.Projection.BOGUS")
	wantError(t, "GET", "/api/studies/study_es_0/samples?sortBy=notAField", "", 400, enum+"org.cbioportal.legacy.web.parameter.sort.SampleSortBy.notAField")
	wantError(t, "GET", "/api/cancer-types?sortBy=typeOfCancerId", "", 400, enum+"org.cbioportal.legacy.web.parameter.sort.CancerTypeSortBy.typeOfCancerId")
	wantError(t, "POST", "/api/clinical-data/fetch?clinicalDataType=FOO", `{"identifiers":[{"entityId":"TCGA-A1-A0SB-01","studyId":"study_es_0"}]}`,
		400, enum+"org.cbioportal.legacy.web.parameter.ClinicalDataType.FOO")
	wantError(t, "GET", "/api/studies/study_es_0/samples?pageSize=-1", "", 400, "pageSize must be greater than or equal to 1")
	wantError(t, "POST", "/api/genes/fetch?geneIdType=ENTREZ_GENE_ID", `[]`, 400, "geneIds size must be between 1 and 10000000")
	wantError(t, "POST", "/api/samples/fetch?projection=DETAILED", `{"uniqueSampleKeys":[]}`, 400, "uniqueSampleKeys size must be between 1 and 10000000")

	const badJSON = "There is an error in the JSON format of the request payload"
	wantError(t, "POST", "/api/samples/fetch", `{bad json`, 400, badJSON)
	wantError(t, "POST", "/api/mutated-genes/fetch", `{bad json`, 400, badJSON)
	wantError(t, "POST", "/api/clinical-attributes/fetch", `{"studyIds":["study_es_0"]}`, 400, badJSON)

	// The legacy controllers behind InvolvedCancerStudyExtractorInterceptor
	// answer an unparsable filter with an empty 200.
	resp, body := rawDo(t, "POST", "/api/clinical-data/fetch?clinicalDataType=SAMPLE", `{bad json`)
	if resp.StatusCode != 200 || len(body) != 0 {
		t.Errorf("clinical-data/fetch malformed: %d %q", resp.StatusCode, body)
	}
}

// TestContractRequiredParams checks the GET (and POST) endpoints whose Java
// controllers declare required request parameters; a String one is present
// even when blank (and names no sample list), an Integer one is not.
func TestContractRequiredParams(t *testing.T) {
	requireAPI(t)
	const missing = "Request parameter is missing: "
	data := "/api/molecular-profiles/study_es_0_gistic/molecular-data"
	wantError(t, "GET", data+"?sampleListId=study_es_0_all", "", 400, missing+"entrezGeneId")
	wantError(t, "GET", data+"?entrezGeneId=1", "", 400, missing+"sampleListId")
	wantError(t, "GET", data+"?sampleListId=study_es_0_all&entrezGeneId=", "", 400, missing+"entrezGeneId")
	wantError(t, "GET", data+"?sampleListId=study_es_0_all&entrezGeneId=x", "", 400,
		`Request parameter type mismatch: java.lang.NumberFormatException: For input string: "x"`)
	wantError(t, "GET", "/api/molecular-profiles/no_such_prof/molecular-data", "", 400, missing+"sampleListId")
	wantError(t, "GET", "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number", "", 400, missing+"sampleListId")
	wantError(t, "GET", "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number?sampleListId=study_es_0_all&discreteCopyNumberEventType=X",
		"", 400, "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.web.parameter.DiscreteCopyNumberEventType.X")
	for _, path := range []string{data + "?sampleListId=&entrezGeneId=116983",
		"/api/molecular-profiles/study_es_0_gistic/discrete-copy-number?sampleListId=",
		"/api/molecular-profiles/study_es_0_mutations/mutations?sampleListId=&entrezGeneId=672"} {
		var out []any
		if resp, b := rawDo(t, "GET", path, ""); resp.StatusCode != 200 || json.Unmarshal(b, &out) != nil || len(out) != 0 {
			t.Errorf("GET %s: %d %.100s, want []", path, resp.StatusCode, b)
		}
	}
	wantError(t, "POST", "/api/molecular-profiles/co-expressions/fetch?molecularProfileIdA=study_es_0_gistic", "{}", 400, missing+"molecularProfileIdB")
	wantError(t, "POST", "/api/clinical-data-density-plot/fetch?xAxisAttributeId=AGE", "{}", 400, missing+"yAxisAttributeId")
	wantError(t, "POST", "/api/clinical-data-violin-plots/fetch?categoricalAttributeId=CANCER_TYPE", "{}", 400, missing+"numericalAttributeId")
	wantError(t, "POST", "/api/geneset-hierarchy/fetch", "{}", 400, missing+"geneticProfileId")
	wantError(t, "POST", "/api/genesets/x/expression-correlation/fetch", "{}", 400, missing+"geneticProfileId")
	wantError(t, "POST", "/api/molecular-profiles/study_es_0_mrna/mrna-percentile/fetch", "{}", 400, missing+"sampleId")
}

// TestContractFilterValidation checks the filter bodies as Jackson reads
// them (unknown properties and uncoercible values fail) and Bean Validation
// checks them, with the Java answers.
func TestContractFilterValidation(t *testing.T) {
	requireAPI(t)
	const badJSON = "There is an error in the JSON format of the request payload"
	const either = "eitherSampleListIdOrSampleIdsPresent must be true"
	for _, path := range []string{
		"/api/molecular-profiles/study_es_0_mutations/mutations/fetch",
		"/api/molecular-profiles/study_es_0_gistic/molecular-data/fetch",
		"/api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch",
		"/api/molecular-profiles/study_es_0_gistic/gene-panel-data/fetch",
		"/api/molecular-profiles/study_es_0_mutations/mutation-spectrums/fetch",
		"/api/generic_assay_data/study_es_0_mutational_signature/fetch",
	} {
		wantError(t, "POST", path, `{}`, 400, either)
		wantError(t, "POST", path, `{"sampleListId":"study_es_0_all","sampleIds":["TCGA-A1-A0SB-01"]}`, 400, either)
		wantError(t, "POST", path, `{"sampleListId":"study_es_0_all","foo":1}`, 400, badJSON)
		wantError(t, "POST", path, `{"sampleIds":"TCGA-A1-A0SB-01"}`, 400, badJSON)
		wantError(t, "POST", path, `{"sampleListId":["study_es_0_all"]}`, 400, badJSON)
		wantError(t, "POST", path, ``, 400, badJSON)
		wantError(t, "POST", path, `{"sampleIds":[]}`, 400, "sampleIds size must be between 1 and 10000000")
	}
	mutations := "/api/molecular-profiles/study_es_0_mutations/mutations/fetch"
	wantError(t, "POST", mutations, `{"sampleListId":"study_es_0_all","entrezGeneIds":[]}`, 400, "entrezGeneIds size must be between 1 and 10000000")
	wantError(t, "POST", mutations, `{"sampleListId":"study_es_0_all","entrezGeneIds":["x"]}`, 400, badJSON)
	wantError(t, "POST", mutations, `{"sampleListId":"study_es_0_all","entrezGeneIds":[2147483648]}`, 400, badJSON)
	wantError(t, "POST", "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch",
		`{"sampleListId":"study_es_0_all","entrezGeneIds":[]}`, 400, "entrezGeneIds size must be between 1 and 50000")
	// Jackson coerces scalars: "672" is an Integer, 5 the sample list "5".
	for _, body := range []string{`{"sampleListId":"study_es_0_all","entrezGeneIds":["672", 672.9, null]}`, `{"sampleListId":5}`, `{"sampleListId":""}`} {
		if resp, b := rawDo(t, "POST", mutations, body); resp.StatusCode != 200 {
			t.Errorf("%s: %d %.100s", body, resp.StatusCode, b)
		}
	}
	// An empty sample list name selects no sample.
	var none []any
	if resp, b := rawDo(t, "POST", mutations, `{"sampleListId":""}`); json.Unmarshal(b, &none) != nil || len(none) != 0 {
		t.Errorf(`{"sampleListId":""}: %d %.100s`, resp.StatusCode, b)
	}

	// Filters InvolvedCancerStudyExtractorInterceptor reads: an unreadable
	// one is an empty 200, violations name the controller parameter.
	wantError(t, "POST", "/api/mutations/fetch", `{}`, 400, "interceptedMutationMultipleStudyFilter must be true")
	wantError(t, "POST", "/api/mutations/fetch", `{"molecularProfileIds":["study_es_0_mutations"],"entrezGeneIds":[]}`,
		400, "interceptedMutationMultipleStudyFilter size must be between 1 and 10000000")
	wantError(t, "POST", "/api/molecular-data/fetch", `{"sampleMolecularIdentifiers":[]}`, 400,
		"interceptedMolecularDataMultipleStudyFilter size must be between 1 and 10000000")
	wantError(t, "POST", "/api/gene-panel-data/fetch", `{}`, 400, "interceptedGenePanelDataMultipleStudyFilter must be true")
	wantError(t, "POST", "/api/structural-variant/fetch", `{"molecularProfileIds":[]}`, 400, "interceptedStructuralVariantFilter must be true")
	wantError(t, "POST", "/api/mutations/fetch?projection=X", `{}`, 400,
		"Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.web.parameter.Projection.X")
	for _, c := range []struct{ path, body string }{
		{"/api/mutations/fetch", `{"molecularProfileIds":["study_es_0_mutations"],"foo":1}`},
		{"/api/mutations/fetch", `{"molecularProfileIds":"study_es_0_mutations"}`},
		{"/api/molecular-data/fetch", `{"sampleMolecularIdentifiers":[{"molecularProfileId":"study_es_0_gistic","sampleId":"x","foo":1}]}`},
		{"/api/gene-panel-data/fetch", `{"molecularProfileIds":["study_es_0_mutations"],"entrezGeneIds":[]}`},
		{"/api/generic_assay_data/fetch", `{"molecularProfileIds":["study_es_0_mutational_signature"],"foo":1}`},
	} {
		if resp, b := rawDo(t, "POST", c.path, c.body); resp.StatusCode != 200 || len(b) != 0 {
			t.Errorf("POST %s %s: %d %.100q, want an empty 200", c.path, c.body, resp.StatusCode, b)
		}
	}
}

func TestContractMetaSampleCountOnlyForMutations(t *testing.T) {
	requireAPI(t)
	resp, _ := rawDo(t, "GET", "/api/studies/study_es_0/samples?projection=META", "")
	if resp.Header.Get("total-count") == "" || resp.Header.Get("sample-count") != "" {
		t.Errorf("samples META headers: %v", resp.Header)
	}
}

func TestContractInfo(t *testing.T) {
	requireAPI(t)
	var info map[string]any
	get(t, "/api/info", &info)
	for _, k := range []string{"portalVersion", "dbVersion", "genesetVersion", "geneTableVersion", "gitBranch",
		"gitCommitId", "gitCommitIdDescribe", "gitCommitIdDescribeShort", "gitCommitMessageFull",
		"gitCommitMessageShort", "gitCommitMessageUserEmail", "gitCommitMessageUserName", "gitDirty"} {
		if _, ok := info[k]; !ok {
			t.Errorf("info lacks %s", k)
		}
	}
}
