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

// StudyViewFilter ignores properties it does not declare; where validated,
// exactly one of studyIds and sampleIdentifiers is present, and not empty.
func TestReadStudyViewFilter(t *testing.T) {
	const size = "size must be between 1 and 2147483647"
	clinical := `"clinicalDataFilters":[{"attributeId":"ONCOTREE_CODE","values":[{"value":"OS"}]}]`
	for _, c := range []struct {
		name string
		fs   *filterSchema
		body string
		ok   bool
		prop string
		viol string
	}{
		{"studies", studyViewFilterSchema, `{"studyIds":["s"],` + clinical + `,"x":1}`, true, "", ""},
		{"samples", studyViewFilterSchema, `{"sampleIdentifiers":[{"studyId":"s","sampleId":"a","x":1}]}`, true, "", ""},
		{"neither", studyViewFilterSchema, `{` + clinical + `}`, true, "eitherSampleIdentifiersOrStudyIdsPresent", "must be true"},
		{"both", studyViewFilterSchema, `{"studyIds":["s"],"sampleIdentifiers":[{"studyId":"s","sampleId":"a"}]}`, true, "eitherSampleIdentifiersOrStudyIdsPresent", "must be true"},
		{"both empty", studyViewFilterSchema, `{"studyIds":[],"sampleIdentifiers":[]}`, true, "eitherSampleIdentifiersOrStudyIdsPresent", "must be true"},
		{"empty studies", studyViewFilterSchema, `{"studyIds":[]}`, true, "studyIds", size},
		{"empty samples", studyViewFilterSchema, `{"sampleIdentifiers":[]}`, true, "sampleIdentifiers", size},
		{"scalar studies", studyViewFilterSchema, `{"studyIds":"s"}`, false, "", ""},
		{"unvalidated neither", studyViewFilterReadSchema, `{` + clinical + `}`, true, "", ""},
		{"unvalidated scalar studies", studyViewFilterReadSchema, `{"studyIds":"s"}`, false, "", ""},
	} {
		ok, prop, viol := c.fs.readFilter([]byte(c.body), false)
		if ok != c.ok || prop != c.prop || viol != c.viol {
			t.Errorf("%s: ok=%v %q %q, want %v %q %q", c.name, ok, prop, viol, c.ok, c.prop, c.viol)
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
