package core

import "testing"

// The filters InvolvedCancerStudyExtractorInterceptor reads with a strict
// ObjectMapper: a property the class lacks, a value of the wrong type or an
// unknown enum constant fails the read (Java then answers an empty 200).
func TestReadInterceptedFilters(t *testing.T) {
	for _, c := range []struct {
		name string
		fs   *filterSchema
		body string
		ok   bool
		viol string
	}{
		{"patients", patientFilterSchema, `{"patientIdentifiers":[{"studyId":"s","patientId":"p"}]}`, true, ""},
		{"patients unknown", patientFilterSchema, `{"patientIds":["p"]}`, false, ""},
		{"patients element unknown", patientFilterSchema, `{"patientIdentifiers":[{"studyId":"s","patientId":"p","x":1}]}`, false, ""},
		{"patients none", patientFilterSchema, `{}`, true, "must be true"},
		{"patients empty keys", patientFilterSchema, `{"uniquePatientKeys":[]}`, true, "size must be between 1 and 10000000"},
		{"profiles", molecularProfileFilterSchema, `{"studyIds":["s"]}`, true, ""},
		{"profiles projection", molecularProfileFilterSchema, `{"studyIds":["s"],"projection":"DETAILED"}`, false, ""},
		{"profiles scalar list", molecularProfileFilterSchema, `{"studyIds":"s"}`, false, ""},
		{"segments", sampleIdentifierListSchema, `[{"sampleId":"a","studyId":"s"}]`, true, ""},
		{"segments empty", sampleIdentifierListSchema, `[]`, true, ""},
		{"segments object", sampleIdentifierListSchema, `{"studyId":"s"}`, false, ""},
		{"segments unknown", sampleIdentifierListSchema, `[{"stableSampleId":"a"}]`, false, ""},
		{"survival", survivalRequestSchema, `{"attributeIdPrefix":"OS","patientIdentifiers":[{"studyId":"s","patientId":"p"}],` +
			`"startEventRequestIdentifier":{"position":"FIRST","clinicalEventRequests":[{"eventType":"TREATMENT","attributes":[{"key":"AGENT","value":"X"}]}]}}`, true, ""},
		{"survival unknown", survivalRequestSchema, `{"sampleListId":"l"}`, false, ""},
		{"survival enum", survivalRequestSchema, `{"startEventRequestIdentifier":{"position":"MIDDLE"}}`, false, ""},
		{"events meta", clinicalEventAttributeRequestSchema, `{"patientIdentifiers":[{"studyId":"s","patientId":"p"}],"clinicalEventRequests":[{"eventType":"TREATMENT"}]}`, true, ""},
		{"events meta unknown", clinicalEventAttributeRequestSchema, `{"patientIdentifiers":[],"clinicalEventRequests":[{"clinicalEventType":"TREATMENT"}]}`, false, ""},
	} {
		ok, _, viol := c.fs.readFilter([]byte(c.body), false)
		if ok != c.ok || viol != c.viol {
			t.Errorf("%s: ok=%v violation=%q, want %v %q", c.name, ok, viol, c.ok, c.viol)
		}
	}
}

// go reads an intercepted filter skipping the properties it does not
// declare, where Java's interceptor gives up; wrong types still fail.
func TestReadInterceptedFiltersIgnoreUnknown(t *testing.T) {
	for _, c := range []struct {
		name string
		fs   *filterSchema
		body string
		ok   bool
		viol string
	}{
		{"profiles projection", molecularProfileFilterSchema, `{"studyIds":["s"],"projection":"DETAILED"}`, true, ""},
		{"profiles scalar list", molecularProfileFilterSchema, `{"studyIds":"s"}`, false, ""},
		{"segments unknown", sampleIdentifierListSchema, `[{"stableSampleId":"a"}]`, true, ""},
		{"segments object", sampleIdentifierListSchema, `{"studyId":"s"}`, false, ""},
		{"survival enum", survivalRequestSchema, `{"startEventRequestIdentifier":{"position":"MIDDLE"}}`, false, ""},
		{"events meta unknown", clinicalEventAttributeRequestSchema, `{"patientIdentifiers":[{"studyId":"s","patientId":"p"}],"clinicalEventRequests":[{"clinicalEventType":"TREATMENT"}]}`, true, ""},
	} {
		ok, _, viol := c.fs.readFilter([]byte(c.body), true)
		if ok != c.ok || viol != c.viol {
			t.Errorf("%s: ok=%v violation=%q, want %v %q", c.name, ok, viol, c.ok, c.viol)
		}
	}
}
