package main

import (
	"strings"
	"testing"
)

func TestExpect(t *testing.T) {
	for _, c := range []struct {
		method, target, body string
		want                 string // substring of the options; "" for none
	}{
		{"GET", "/api/session/virtual_study", "", "known="},
		{"POST", "/api/filtered-samples/fetch", `{"customDataFilters":[]}`, "known="},
		{"POST", "/api/filtered-samples/fetch", `{"studyIds":["x"]}`, ""},
		{"GET", "/api/info", "", "ignore=gitBranch"},
		{"GET", "/api/info/", "", "ignore=gitBranch"},
		{"GET", "/api/v3/api-docs/public", "", "ignore=host,servers"},
		{"GET", "/api/v2/api-docs?group=default", "", "known="},
		{"GET", "/api/studies/", "", "known="},
		{"GET", "/api/studies/acc_tcga", "", "ignore=resourceCounts"},
		{"GET", "/api/studies/acc_tcga/samples", "", ""},
		{"POST", "/api/studies/fetch?projection=DETAILED", `["acc_tcga"]`, "ignore=allSampleCount,resourceCounts,treatmentCount"},
		{"POST", "/api/studies/fetch/", `["acc_tcga"]`, "ignore=allSampleCount,resourceCounts,treatmentCount"},
		{"GET", "/api/legacy/studies/acc_tcga/", "", "ignore=allSampleCount,resourceCounts,treatmentCount"},
		{"GET", "/api/legacy/studies?keyword=lung", "", "ignore=allSampleCount,resourceCounts,treatmentCount"},
		{"GET", "/api/legacy/studies?pageSize=7&pageNumber=3", "", "known="},
		{"GET", "/api/studies/s/clinical-data?pageNumber=6&pageSize=200", "", "known="},
		{"GET", "/api/studies/s/clinical-data?attributeId=AGE", "", ""},
		{"GET", "/api/samples?keyword=ar&pageSize=3", "", "known="},
		{"POST", "/api/clinical-data-table/fetch?pageSize=500&sortBy=X", "{}", "known="},
		{"POST", "/api/clinical-data-table/fetch?pageSize=500", "{}", "known="},
		{"POST", "/api/clinical-data-table/fetch", "{}", ""},
		{"POST", "/api/molecular-profile-sample-counts/fetch", "{}", "ignore=label"},
		{"POST", "/api/sample-lists-counts/fetch", "{}", "ignore=label"},
		{"POST", "/api/molecular-profiles/p/mutations/fetch?pageSize=1000&pageNumber=452&projection=DETAILED", `{"sampleListId":"l"}`, "known="},
		{"POST", "/api/molecular-profiles/p/mutations/fetch?pageSize=1000&sortBy=keyword", `{"sampleListId":"l"}`, ""},
		{"POST", "/api/molecular-profiles/p/mutations/fetch", `{"sampleListId":"l"}`, ""},
		{"GET", "/api/molecular-profiles/p/mutations?sampleListId=l&entrezGeneId=1&pageSize=5", "", "known="},
		{"GET", "/api/samples?pageSize=10&pageNumber=36761&projection=DETAILED", "", "known="},
		{"GET", "/api/patients?pageSize=10&pageNumber=29059", "", "known="},
		{"GET", "/api/studies/s/samples?pageSize=10", "", "known="},
		{"GET", "/api/samples?pageSize=10&projection=ID", "", ""},
		{"GET", "/api/samples?pageSize=10&sortBy=sampleId", "", ""},
		{"GET", "/api/patients", "", ""},
		{"POST", "/api/treatments/patient?tier=Agent", `{"studyIds": ["acbc_mskcc_2015"]}`, "known-if-empty="},
		{"POST", "/api/treatments/sample?tier=Agent", `{"sampleIdentifiers": [{"studyId": "s", "sampleId": "a"}]}`, "known-if-empty="},
		{"POST", "/api/treatments/sample", `{"studyIds": ["s"], "tiersBooleanMap": {}}`, "known-if-empty="},
		{"POST", "/api/treatments/patient-counts/fetch?tier=Agent", `{"studyIds":["s"]}`, ""},
		// Paging go adds.
		{"GET", "/api/molecular-profiles/p_gistic/discrete-copy-number?sampleListId=l&pageSize=10&pageNumber=1", "", "known="},
		{"GET", "/api/molecular-profiles/p_gistic/discrete-copy-number?sampleListId=l", "", ""},
		{"GET", "/api/sample-lists/l/sample-ids?pageSize=5", "", "known="},
		{"GET", "/api/sample-lists/l/sample-ids", "", ""},
		{"POST", "/api/molecular-data/fetch?pageSize=2&pageNumber=0", `{"molecularProfileIds":["p"]}`, "known="},
		{"POST", "/api/genes/fetch?sortBy=hugoGeneSymbol", `["TP53"]`, "known="},
		{"POST", "/api/genes/fetch?geneIdType=HUGO_GENE_SYMBOL", `["TP53"]`, ""},
		// Java 500s that go answers.
		{"POST", "/api/clinical-data/fetch", `{}`, "known="},
		{"POST", "/api/clinical-data/fetch?clinicalDataType=PATIENT", `{"attributeIds":["MSI_SCORE"]}`, "known="},
		{"POST", "/api/clinical-data/fetch", `{"identifiers":[{"studyId":"s","entityId":"a"}]}`, ""},
		{"POST", "/api/clinical-events-meta/fetch", `{"patientIdentifiers":[{"studyId":"s","patientId":"P01"},{"studyId":"s","patientId":"P02"}]}`, "known="},
		{"POST", "/api/clinical-events-meta/fetch", `{"patientIdentifiers":[{"studyId":"s","patientId":"P01"}]}`, ""},
		{"POST", "/api/survival-data/fetch", `{"attributeIdPrefix":"OS","patientIdentifiers":[{"studyId":"s","patientId":"P01"},{"studyId":"t","patientId":"P02"}]}`, "known="},
		// A slashed POST whose filter Java's interceptor leaves unparsed.
		{"POST", "/api/clinical-data/fetch/?clinicalDataType=SAMPLE", `{"identifiers":[{"studyId":"s","entityId":"a"}]}`, "known-if-b-fails="},
		{"POST", "/api/clinical-data/fetch/", `{}`, "known-if-b-fails="},
		{"POST", "/api/clinical-events-meta/fetch/", `{"patientIdentifiers":[{"studyId":"s","patientId":"P01"}]}`, "known-if-b-fails="},
		{"POST", "/api/survival-data/fetch/", `{"attributeIdPrefix":"OS"}`, "known-if-b-fails="},
		{"POST", "/api/generic_assay_data/fetch/", `{"molecularProfileIds":["p"]}`, "known-if-b-fails="},
		{"POST", "/api/legacy/mutations/fetch/", `{"molecularProfileIds":["p"]}`, "known-if-b-fails="},
		{"POST", "/api/treatments/patient/?tier=Agent", `{"studyIds":["s"]}`, "known-if-b-fails="},
		{"POST", "/api/mutations/fetch", `{"molecularProfileIds":["p"]}`, ""},
		{"POST", "/api/survival-data/fetch", `{"attributeIdPrefix":"OS"}`, ""},
		{"POST", "/api/treatments/patient?tier=Agent", `{"studyIds":["s"]}`, "known-if-empty="},
		{"GET", "/api/clinical-data/fetch/", "", ""},
		{"POST", "/api/clinical-data-counts/fetch/", `{"studyViewFilter":{"studyIds":["s"]}}`, ""},
		{"POST", "/api/studies/fetch/", `["s"]`, "ignore=allSampleCount,resourceCounts,treatmentCount"},
		// Java bugs in the data.
		{"POST", "/api/structuralvariant-counts/fetch", `{"studyIds":["s"]}`, "ignore=matchingGenePanelIds,numberOfProfiledCases"},
		{"GET", "/api/studies/s/significant-copy-number-regions?pageSize=10&projection=SUMMARY", "", "known="},
		{"GET", "/api/studies/s/significant-copy-number-regions?pageSize=10&sortBy=qValue", "", ""},
	} {
		got, _ := expect(c.method, c.target, c.body)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s %s: %q, want %q", c.method, c.target, got, c.want)
		}
	}
}

func TestSkip(t *testing.T) {
	for _, c := range []struct {
		method, target, body string
		want                 bool
	}{
		{"GET", "/api/molecular-profiles/p_gistic/discrete-copy-number?sampleListId=l&discreteCopyNumberEventType=ALL&projection=SUMMARY", "", true},
		{"GET", "/api/molecular-profiles/p_gistic/discrete-copy-number/?sampleListId=l&discreteCopyNumberEventType=ALL", "", true},
		{"POST", "/api/molecular-profiles/p_cna/discrete-copy-number/fetch?projection=SUMMARY&pageSize=3&discreteCopyNumberEventType=ALL", `{"sampleListId": "l"}`, true},
		{"POST", "/api/molecular-profiles/p_cna/discrete-copy-number/fetch?discreteCopyNumberEventType=ALL", `{"entrezGeneIds": [207], "sampleListId": "l"}`, false},
		{"GET", "/api/molecular-profiles/p_gistic/discrete-copy-number?sampleListId=l&entrezGeneIds=8880&discreteCopyNumberEventType=ALL", "", false},
		{"GET", "/api/molecular-profiles/p_gistic/discrete-copy-number?sampleListId=l", "", false},
		{"GET", "/api/molecular-profiles/p_gistic/discrete-copy-number-counts?discreteCopyNumberEventType=ALL", "", false},
		{"POST", "/api/molecular-profiles/p_gistic/molecular-data/fetch?projection=DETAILED&pageSize=500000&pageNumber=10", `{"sampleIds": ["s1", "s2"]}`, true},
		{"POST", "/api/molecular-data/fetch?pageNumber=1", `{"molecularProfileIds": ["p_mrna"]}`, true},
		{"GET", "/api/molecular-profiles/p_mrna/molecular-data?sampleListId=l&pageSize=10", "", true},
		{"POST", "/api/molecular-profiles/p_gistic/molecular-data/fetch?pageSize=10", `{"sampleIds": ["s1"], "entrezGeneIds": [7157]}`, false},
		{"POST", "/api/molecular-profiles/p_gistic/molecular-data/fetch", `{"sampleIds": ["s1"]}`, false},
	} {
		if got := skip(c.method, c.target, c.body); got != c.want {
			t.Errorf("%s %s %s: %v, want %v", c.method, c.target, c.body, got, c.want)
		}
	}
}

func TestAnnotate(t *testing.T) {
	in := "# head\n\n## get-info-1 unordered\nGET /api/info\n\n## post-x-1 unordered\nPOST /api/x/fetch\n{}\n" +
		"\n## get-session-1 unordered known=\"stale\"\nGET /api/session/a\n" +
		"\n# an old reason\n## get-studies-1 unordered ignore=old\nGET /api/studies/s\n" +
		"\n## get-dcn-1 unordered\nGET /api/molecular-profiles/p/discrete-copy-number?sampleListId=l&discreteCopyNumberEventType=ALL\n" +
		"\n## post-treatments-sample-1 unordered known=\"stale\"\nPOST /api/treatments/sample\n{\"studyIds\":[\"s\"]}\n" +
		"\n## post-clinical-1 unordered known-if-b-fails=\"stale\"\nPOST /api/clinical-data/fetch/\n{}\n" +
		"\n## post-mutations-1 unordered known-if-b-fails=\"stale\"\nPOST /api/mutations/fetch\n{}\n"
	var out, report strings.Builder
	if err := annotate(strings.NewReader(in), &out, &report); err != nil {
		t.Fatal(err)
	}
	opts := func(method, target, body string) string { o, _ := expect(method, target, body); return o }
	_, studyComment := expect("GET", "/api/studies/s", "")
	want := "# head\n\n# version fields describe the deployment\n## get-info-1 unordered ignore=" + versionFields +
		"\nGET /api/info\n\n## post-x-1 unordered\nPOST /api/x/fetch\n{}\n" +
		"\n## get-session-1 unordered " + opts("GET", "/api/session/a", "") + "\nGET /api/session/a\n" +
		"\n# " + studyComment + "\n## get-studies-1 unordered ignore=resourceCounts\nGET /api/studies/s\n" +
		"\n## post-treatments-sample-1 unordered " + opts("POST", "/api/treatments/sample", `{"studyIds":["s"]}`) +
		"\nPOST /api/treatments/sample\n{\"studyIds\":[\"s\"]}\n" +
		"\n## post-clinical-1 unordered " + opts("POST", "/api/clinical-data/fetch/", "{}") + "\nPOST /api/clinical-data/fetch/\n{}\n" +
		"\n## post-mutations-1 unordered\nPOST /api/mutations/fetch\n{}\n"
	if out.String() != want {
		t.Errorf("annotate:\n%s\nwant:\n%s", out.String(), want)
	}
	if !strings.Contains(report.String(), "left out 1 ") {
		t.Errorf("report: %q", report.String())
	}
}
