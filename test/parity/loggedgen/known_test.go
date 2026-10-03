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
		{"GET", "/api/v3/api-docs/public", "", "ignore=host,servers"},
		{"GET", "/api/studies/", "", "known="},
		{"GET", "/api/studies/acc_tcga", "", ""},
		{"GET", "/api/studies/s/clinical-data?pageNumber=6&pageSize=200", "", "known="},
		{"GET", "/api/studies/s/clinical-data?attributeId=X", "", ""},
		{"GET", "/api/samples?keyword=ar&pageSize=3", "", "known="},
		{"POST", "/api/clinical-data-table/fetch?pageSize=500&sortBy=X", "{}", "known="},
		{"POST", "/api/clinical-data-table/fetch?pageSize=500", "{}", ""},
		{"POST", "/api/molecular-profile-sample-counts/fetch", "{}", "ignore=label"},
	} {
		got, _ := expect(c.method, c.target, c.body)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s %s: %q, want %q", c.method, c.target, got, c.want)
		}
	}
}

func TestAnnotate(t *testing.T) {
	in := "# head\n\n## get-info-1 unordered\nGET /api/info\n\n## post-x-1 unordered\nPOST /api/x/fetch\n{}\n" +
		"\n## get-session-1 unordered known=\"kept\"\nGET /api/session/a\n"
	var out strings.Builder
	if err := annotate(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	want := "# head\n\n# version fields describe the deployment\n## get-info-1 unordered ignore=" + versionFields +
		"\nGET /api/info\n\n## post-x-1 unordered\nPOST /api/x/fetch\n{}\n" +
		"\n## get-session-1 unordered known=\"kept\"\nGET /api/session/a\n"
	if out.String() != want {
		t.Errorf("annotate:\n%s\nwant:\n%s", out.String(), want)
	}
}
