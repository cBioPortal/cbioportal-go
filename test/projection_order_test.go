package contract

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Contracts for the field order of each projection, as Jackson writes the
// Java models, and for the projection parameter itself. They hold for the
// Java backend too (test/java_oracle.sh).

// member is one member of a JSON object, in document order.
type member struct {
	key string
	val any
}

// decodeOrdered decodes JSON keeping object member order: objects become
// []member, arrays []any.
func decodeOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		var obj []member
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeOrdered(dec)
			if err != nil {
				return nil, err
			}
			obj = append(obj, member{k.(string), v})
		}
		_, err := dec.Token()
		return obj, err
	case json.Delim('['):
		arr := []any{}
		for dec.More() {
			v, err := decodeOrdered(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token()
		return arr, err
	}
	return tok, nil
}

// fieldOrder returns the member names of the first object of the response
// (the response itself when it is an object), or of its member at path.
func fieldOrder(t *testing.T, method, url, body string, path ...string) []string {
	t.Helper()
	resp, b := rawDo(t, method, url, body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s %s -> %d: %s", method, url, resp.StatusCode, b)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := decodeOrdered(dec)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	first := func(v any) any {
		if arr, ok := v.([]any); ok {
			if len(arr) == 0 {
				t.Fatalf("%s %s: empty list", method, url)
			}
			return arr[0]
		}
		return v
	}
	v = first(v)
	for _, p := range path {
		obj, _ := v.([]member)
		var next any
		for _, m := range obj {
			if m.key == p {
				next = m.val
			}
		}
		if next == nil {
			t.Fatalf("%s %s: no member %s", method, url, p)
		}
		v = first(next)
	}
	obj, ok := v.([]member)
	if !ok {
		t.Fatalf("%s %s: not an object", method, url)
	}
	keys := make([]string, len(obj))
	for i, m := range obj {
		keys[i] = m.key
	}
	return keys
}

func split(s string) []string { return strings.Split(s, ",") }

func TestOracleProjectionFieldOrder(t *testing.T) {
	requireAPI(t)
	const (
		studyCounts = "sequencedSampleCount,cnaSampleCount,mrnaRnaSeqSampleCount,mrnaRnaSeqV2SampleCount,mrnaMicroarraySampleCount," +
			"miRnaSampleCount,methylationHm27SampleCount,rppaSampleCount,massSpectrometrySampleCount,completeSampleCount"
		studyHead = "studyId,cancerTypeId,name,description,publicStudy,pmid,citation,groups,status,importDate"
	)
	cases := []struct {
		method, url, body string
		path              []string
		want              string
	}{
		// Column-store study DTO and its resource counts.
		{"GET", "/api/studies/study_es_0", "", nil,
			studyHead + ",allSampleCount," + studyCounts + ",referenceGenome,treatmentCount,structuralVariantCount,cancerType,readPermission,resourceCounts"},
		{"GET", "/api/studies/study_es_0", "", []string{"resourceCounts"},
			"resourceId,displayName,description,resourceType,priority,openByDefault,cancerStudyIdentifier,sampleCount,patientCount"},
		// Legacy CancerStudy: cancerType after importDate, the treatment and SV counts after readPermission.
		{"POST", "/api/studies/fetch?projection=DETAILED", `["study_es_0"]`, nil,
			studyHead + ",cancerType,allSampleCount," + studyCounts + ",referenceGenome,readPermission,treatmentCount,structuralVariantCount,resourceCounts"},
		{"POST", "/api/studies/fetch?projection=SUMMARY", `["study_es_0"]`, nil,
			studyHead + ",allSampleCount,referenceGenome,readPermission,resourceCounts"},
		{"POST", "/api/studies/fetch?projection=SUMMARY", `["study_es_0"]`, []string{"resourceCounts"},
			"resourceId,displayName,description,resourceType,priority,openByDefault,sampleCount,patientCount,cancerStudyIdentifier"},
		{"GET", "/api/studies/study_es_0/sample-lists?projection=SUMMARY&pageSize=1", "", nil, "sampleListId,studyId,category,name,description"},
		{"GET", "/api/studies/study_es_0/sample-lists?projection=DETAILED&pageSize=1", "", nil,
			"sampleListId,studyId,category,name,description,sampleCount,sampleIds"},
		{"GET", "/api/clinical-attributes?projection=ID&pageSize=1", "", nil, "displayName,patientAttribute,clinicalAttributeId,studyId"},
		{"GET", "/api/clinical-attributes?projection=SUMMARY&pageSize=1", "", nil,
			"displayName,description,datatype,patientAttribute,priority,clinicalAttributeId,studyId"},
		{"GET", "/api/studies/study_es_0/samples/TCGA-A1-A0SB-01/clinical-data?projection=DETAILED&pageSize=1", "", nil,
			"uniqueSampleKey,uniquePatientKey,sampleId,patientId,studyId,clinicalAttribute,patientAttribute,clinicalAttributeId,value"},
		{"GET", "/api/studies/study_es_0/samples/TCGA-A1-A0SB-01/clinical-data?projection=DETAILED&pageSize=1", "", []string{"clinicalAttribute"},
			"displayName,description,datatype,patientAttribute,priority,clinicalAttributeId,studyId"},
		// Clinical events: ID has neither dates nor attributes.
		{"GET", "/api/studies/lgg_ucsf_2014_test_generic_assay/patients/P04/clinical-events?projection=ID&pageSize=1", "", nil,
			"studyId,patientId,uniquePatientKey,eventType"},
		// Sorted so the page holds the same event (one with an end date) on
		// every database build.
		{"GET", "/api/studies/lgg_ucsf_2014_test_generic_assay/patients/P04/clinical-events?projection=SUMMARY&pageSize=1&sortBy=endNumberOfDaysSinceDiagnosis&direction=DESC", "", nil,
			"studyId,patientId,uniquePatientKey,eventType,startNumberOfDaysSinceDiagnosis,endNumberOfDaysSinceDiagnosis,attributes"},
		// Molecular profiles: the DETAILED study follows showProfileInAnalysisTab.
		{"POST", "/api/molecular-profiles/fetch?projection=SUMMARY", `{"molecularProfileIds":["lgg_ucsf_2014_test_generic_assay_hla"]}`, nil,
			"molecularProfileId,studyId,molecularAlterationType,genericAssayType,datatype,name,description,showProfileInAnalysisTab,patientLevel"},
		{"POST", "/api/molecular-profiles/fetch?projection=DETAILED", `{"molecularProfileIds":["lgg_ucsf_2014_test_generic_assay_hla"]}`, nil,
			"molecularProfileId,studyId,molecularAlterationType,genericAssayType,datatype,name,description,showProfileInAnalysisTab,study,patientLevel"},
		// Mutations: the unique keys first (UniqueKeyBase), then Alteration's and Mutation's fields.
		{"POST", "/api/mutations/fetch?projection=ID", `{"molecularProfileIds":["ascn_test_study_mutations"]}`, nil,
			"uniqueSampleKey,uniquePatientKey,molecularProfileId,sampleId,patientId,entrezGeneId,studyId"},
		{"POST", "/api/mutations/fetch?projection=DETAILED", `{"molecularProfileIds":["ascn_test_study_mutations"]}`, nil,
			"uniqueSampleKey,uniquePatientKey,molecularProfileId,sampleId,patientId,entrezGeneId,gene,studyId,center,mutationStatus," +
				"validationStatus,tumorAltCount,tumorRefCount,normalAltCount,normalRefCount,chr,startPosition,endPosition,referenceAllele," +
				"variantAllele,proteinChange,mutationType,ncbiBuild,variantType,refseqMrnaId,proteinPosStart,proteinPosEnd,alleleSpecificCopyNumber"},
		{"POST", "/api/generic_assay_data/lgg_ucsf_2014_test_generic_assay_hla/fetch?projection=SUMMARY",
			`{"sampleIds":["P01_Pri"],"genericAssayStableIds":["HLA-A"]}`, nil,
			"uniqueSampleKey,uniquePatientKey,molecularProfileId,sampleId,patientId,studyId,value,genericAssayStableId,patientLevel,stableId"},
		{"GET", "/api/generic-assay-meta/generic-assay/HLA-A?projection=ID", "", nil, "stableId"},
		{"GET", "/api/generic-assay-meta/generic-assay/HLA-A?projection=SUMMARY", "", nil, "stableId,entityType,genericEntityMetaProperties"},
		// The properties HashMap's order.
		{"GET", "/api/generic-assay-meta/generic-assay/confidence_1", "", []string{"genericEntityMetaProperties"}, "name,description,confidenceStatement"},
		{"GET", "/api/generic-assay-meta/lgg_ucsf_2014_test_generic_assay_mutational_signature_contribution_DBS", "",
			[]string{"genericEntityMetaProperties"}, "DESCRIPTION,URL,NAME"},
		// Resource data: the unique keys after the url.
		{"GET", "/api/studies/study_es_0/resource-data-all?projection=DETAILED&resourceId=PATHOLOGY_SLIDE&pageSize=1", "", nil,
			"sampleId,patientId,studyId,resourceId,url,uniqueSampleKey,uniquePatientKey,resourceDefinition"},
		// Gistic: the renamed studyId and qValue last.
		{"GET", "/api/studies/study_es_0/significant-copy-number-regions?projection=ID&pageSize=1", "", nil,
			"chromosome,cytoband,widePeakStart,widePeakEnd,amp,studyId,qValue"},
		{"GET", "/api/studies/study_es_0/significant-copy-number-regions?projection=SUMMARY&pageSize=1", "", nil,
			"chromosome,cytoband,widePeakStart,widePeakEnd,amp,genes,studyId,qValue"},
		{"POST", "/api/mutation-data-counts/fetch?projection=DETAILED",
			`{"genomicDataFilters":[{"hugoGeneSymbol":"TP53","profileType":"mutations"}],"studyViewFilter":{"studyIds":["study_es_0"]}}`,
			[]string{"counts"}, "label,value,count,uniqueCount"},
	}
	for _, c := range cases {
		got := fieldOrder(t, c.method, c.url, c.body, c.path...)
		if want := split(c.want); strings.Join(got, ",") != c.want {
			t.Errorf("%s %s %v:\n want %v\n got  %v", c.method, c.url, c.path, want, got)
		}
	}
}

// Every projection parameter converts like Spring's: an unknown value is a
// 400 naming the endpoint's enum class.
func TestOracleInvalidProjection(t *testing.T) {
	requireAPI(t)
	legacy := "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant " +
		"org.cbioportal.legacy.web.parameter.Projection.BOGUS"
	for _, c := range []struct{ method, url, body, msg string }{
		{"POST", "/api/copy-number-segments/fetch?projection=BOGUS", `[{"studyId":"study_es_0","sampleId":"TCGA-A1-A0SB-01"}]`, legacy},
		{"POST", "/api/generic-assay-meta/fetch?projection=BOGUS", `{"molecularProfileIds":["lgg_ucsf_2014_test_generic_assay_hla"]}`, legacy},
		{"GET", "/api/generic-assay-meta/lgg_ucsf_2014_test_generic_assay_hla?projection=BOGUS", "", legacy},
		{"GET", "/api/generic-assay-meta/generic-assay/HLA-A?projection=BOGUS", "", legacy},
		{"GET", "/api/generic-assay-data/lgg_ucsf_2014_test_generic_assay_hla/generic-assay/HLA-A?projection=BOGUS", "", legacy},
		{"GET", "/api/genesets?projection=BOGUS", "", legacy},
		{"POST", "/api/mutation-data-counts/fetch?projection=BOGUS",
			`{"genomicDataFilters":[{"hugoGeneSymbol":"TP53","profileType":"mutations"}],"studyViewFilter":{"studyIds":["study_es_0"]}}`, legacy},
		{"POST", "/api/resource-definitions/fetch?projection=BOGUS", `["study_es_0"]`, legacy},
		{"POST", "/api/treatments/sample-counts/fetch?projection=BOGUS", `{"studyIds":["lgg_ucsf_2014_test_generic_assay"]}`,
			"Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.shared.enums.ProjectionType.BOGUS"},
	} {
		wantError(t, c.method, c.url, c.body, 400, c.msg)
	}
	wantError(t, "GET", "/api/genesets?pageSize=0", "", 400, "pageSize must be greater than or equal to 1")
}

// The resource controllers throw UnsupportedOperationException for META.
func TestOracleResourceMetaNotImplemented(t *testing.T) {
	requireAPI(t)
	for _, url := range []string{
		"/api/studies/study_es_0/samples/TCGA-A2-A04P-01/resource-data?projection=META",
		"/api/studies/study_es_0/patients/TCGA-A2-A04P/resource-data?projection=META",
		"/api/studies/study_es_0/resource-data?projection=META",
		"/api/studies/study_es_0/resource-data-all?projection=META",
		"/api/studies/study_es_0/resource-definitions?projection=META",
	} {
		wantError(t, "GET", url, "", 501, "Requested API is not implemented yet")
	}
}

func TestOracleProjectionBehaviour(t *testing.T) {
	requireAPI(t)
	// The column-store studies list answers META with an empty JSON list.
	resp, body := rawDo(t, "GET", "/api/studies?projection=META", "")
	wantEq(t, string(body), "[]")
	if resp.Header.Get("total-count") == "" {
		t.Errorf("studies META without total-count: %v", resp.Header)
	}

	// Only SUMMARY counts mutated / not mutated samples; ID counts by type as DETAILED does.
	filter := `{"genomicDataFilters":[{"hugoGeneSymbol":"TP53","profileType":"mutations"}],"studyViewFilter":{"studyIds":["study_es_0"]}}`
	var items []struct {
		Counts []struct {
			Value string `json:"value"`
		} `json:"counts"`
	}
	_, body = rawDo(t, "POST", "/api/mutation-data-counts/fetch?projection=ID", filter)
	json.Unmarshal(body, &items)
	for _, c := range items[0].Counts {
		if c.Value == "MUTATED" || c.Value == "NOT_MUTATED" || c.Value == "NOT_PROFILED" {
			t.Errorf("ID mutation-data-counts has %s", c.Value)
		}
	}

	// A region's genes come by entrez id.
	var regions []struct {
		Genes []struct {
			EntrezGeneID int `json:"entrezGeneId"`
		} `json:"genes"`
	}
	get(t, "/api/studies/study_es_0/significant-copy-number-regions", &regions)
	for _, r := range regions {
		for i := 1; i < len(r.Genes); i++ {
			if r.Genes[i-1].EntrezGeneID > r.Genes[i].EntrezGeneID {
				t.Errorf("region genes out of entrez order: %v", r.Genes)
			}
		}
	}

	// Allele-specific copy number Float fields are written as Float.toString.
	_, body = rawDo(t, "POST", "/api/mutations/fetch?projection=DETAILED", `{"molecularProfileIds":["ascn_test_study_mutations"]}`)
	if !bytes.Contains(body, []byte(`"ccfExpectedCopiesUpper":1.0,`)) {
		t.Error(`ASCN ccfExpectedCopiesUpper 1 is not written "1.0"`)
	}

	// The CNA namespaces (and their columns) in the importer's HashMap order.
	_, body = rawDo(t, "GET", "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number?sampleListId=study_es_0_all&projection=SUMMARY", "")
	if !bytes.Contains(body, []byte(`"namespaceColumns":{"MyNamespace2":{"blarp":"bluirp2"},"MyNamespace":{"column1":"value1b","column2":"value2b"}}`)) {
		t.Error("CNA namespaceColumns not in HashMap order")
	}

	// A failed @Size names GenericAssayFilter's field.
	wantError(t, "POST", "/api/generic_assay_data/study_es_0_generic_assay_patient_test/fetch",
		`{"sampleListId":"study_es_0_all","genericAssayStableIds":[]}`, 400, "genericAssayStableId size must be between 1 and 10000000")
}
