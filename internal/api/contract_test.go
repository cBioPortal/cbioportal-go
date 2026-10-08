package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestUniqueKeyMatchesJavaEncoder(t *testing.T) {
	// Encoder.calculateBase64("AdCC1T", "acbc_mskcc_2015") as served by
	// www.cbioportal.org.
	if got := uniqueKey("acbc_mskcc_2015", "AdCC1T"); got != "QWRDQzFUOmFjYmNfbXNrY2NfMjAxNQ" {
		t.Fatalf("uniqueKey = %s", got)
	}
	study, id, ok := decodeUniqueKey("QWRDQzFUOmFjYmNfbXNrY2NfMjAxNQ")
	if !ok || study != "acbc_mskcc_2015" || id != "AdCC1T" {
		t.Fatalf("decode = %q %q %v", study, id, ok)
	}
	study, id, ok = decodeUniqueKey(uniqueKey("s1", "a:b"))
	if !ok || study != "s1" || id != "a:b" {
		t.Fatalf("decode with ':' in id = %q %q %v", study, id, ok)
	}
	if _, _, ok := decodeUniqueKey("!!"); ok {
		t.Fatal("invalid key decoded")
	}
}

func TestCheckParams(t *testing.T) {
	params := paged(projectionType, sampleSort)
	cases := map[string]string{
		"":                       "",
		"projection=":            "",
		"projection=DETAILED":    "",
		"projection=BOGUS":       "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.shared.enums.ProjectionType.BOGUS",
		"projection=summary":     "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.shared.enums.ProjectionType.summary",
		"sortBy=notAField":       "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.web.parameter.sort.SampleSortBy.notAField",
		"direction=UP":           "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.web.parameter.Direction.UP",
		"pageSize=-1":            "pageSize must be greater than or equal to 1",
		"pageSize=0":             "pageSize must be greater than or equal to 1",
		"pageSize=10000001":      "pageSize must be less than or equal to 10000000",
		"pageNumber=-1":          "pageNumber must be greater than or equal to 0",
		"pageSize=abc":           `Request parameter type mismatch: java.lang.NumberFormatException: For input string: "abc"`,
		"pageSize=-1&sortBy=bad": "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.legacy.web.parameter.sort.SampleSortBy.bad",
		// Repeated values: the first is bound; a non-number reports all.
		"pageSize=1&pageSize=x":        "",
		"pageSize=x&pageSize=1":        `Request parameter type mismatch: java.lang.NumberFormatException: For input string: "x,1"`,
		"projection=ID&projection=BAD": "",
		"projection=BAD&projection=ID": "Request parameter type mismatch: java.lang.IllegalArgumentException: No enum constant org.cbioportal.shared.enums.ProjectionType.BAD",
	}
	for query, want := range cases {
		q, _ := url.ParseQuery(query)
		if got := checkParams(params, q); got != want {
			t.Errorf("%q: got %q, want %q", query, got, want)
		}
	}
}

func TestSingleEntrezGeneID(t *testing.T) {
	for query, want := range map[string][]int64{
		"entrezGeneId=7157&entrezGeneId=5925": {7157},
		"entrezGeneId=5925&entrezGeneId=x":    {5925},
		"entrezGeneId=%207157":                {7157},
		"entrezGeneId=":                       nil,
		"":                                    nil,
	} {
		q, _ := url.ParseQuery(query)
		if got := singleEntrezGeneID(q); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", query, got, want)
		}
	}
}

func TestBodyValidators(t *testing.T) {
	genes := listBody("geneIds")
	if got := genes([]byte(`[]`)); got != "geneIds size must be between 1 and 10000000" {
		t.Errorf("empty list: %q", got)
	}
	if got := genes([]byte(`["TP53"]`)); got != "" {
		t.Errorf("non-empty list: %q", got)
	}
	if got := genes([]byte(`{bad`)); got != "" {
		t.Errorf("malformed body is left to the handler: %q", got)
	}
	cases := map[string]string{
		`{"uniqueSampleKeys": []}`: "uniqueSampleKeys size must be between 1 and 10000000",
		`{}`:                       "onlyOneTypeOfFilterPresent must be true",
		`{"sampleListIds": ["a"], "uniqueSampleKeys": ["b"]}`: "onlyOneTypeOfFilterPresent must be true",
		`{"sampleListIds": ["a"]}`:                            "",
	}
	for body, want := range cases {
		if got := sampleFilterBody([]byte(body)); got != want {
			t.Errorf("%s: got %q, want %q", body, got, want)
		}
	}
}

func TestReshape(t *testing.T) {
	s := &State{}
	body := []byte(`[{"sampleId":"a\"b","sampleType":"x","sequenced":true,"nested":{"k":[1,{"z":"}"}]},"studyId":"s"},{"sampleId":"c","studyId":"s"}]` + "\n")
	out, ok := s.reshape(body, []string{"sampleId", "studyId", "nested"}, true, "", "")
	want := `[{"sampleId":"a\"b","nested":{"k":[1,{"z":"}"}]},"studyId":"s"},{"sampleId":"c","studyId":"s"}]` + "\n"
	if !ok || string(out) != want {
		t.Fatalf("reshape array:\n got %s\nwant %s", out, want)
	}
	out, ok = s.reshape([]byte(`{"a":1, "b" : null ,"c":"x"}`), []string{"c", "b"}, true, "", "")
	if !ok || string(out) != `{"b":null,"c":"x"}`+"\n" {
		t.Fatalf("reshape object: %s", out)
	}
	if _, ok := s.reshape([]byte(`["x"]`), nil, true, "", ""); ok {
		t.Fatal("array of strings reshaped")
	}
	if out, ok := s.reshape([]byte(`[]`), nil, true, "", ""); !ok || string(out) != "[]\n" {
		t.Fatalf("empty array: %s", out)
	}
}

func TestMatchContract(t *testing.T) {
	rt, vals := matchContract("GET", "/api/studies/s1/patients/p1/samples")
	if rt == nil || rt.pattern != "/api/studies/{studyId}/patients/{patientId}/samples" ||
		vals["studyId"] != "s1" || vals["patientId"] != "p1" {
		t.Fatalf("match = %v %v", rt, vals)
	}
	if rt, _ := matchContract("POST", "/api/studies/s1/patients/p1/samples"); rt != nil {
		t.Fatal("method ignored")
	}
}

func TestReadBodyMalformed(t *testing.T) {
	h := bodyErrors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ids []string
		readBody(r, &ids)
		writeJSON(w, ids)
	}))
	cases := []struct {
		path, body string
		status     int
		resp       string
	}{
		{"/api/genes/fetch", `["TP53"]`, 200, `["TP53"]`},
		{"/api/genes/fetch", `{bad json`, 400, `{"message":"There is an error in the JSON format of the request payload"}`},
		{"/api/genes/fetch", `{"studyIds":["x"]}`, 400, `{"message":"There is an error in the JSON format of the request payload"}`},
		{"/api/genes/fetch", `[1]`, 200, `[""]`},
		{"/api/genes/fetch", ``, 200, `null`},
		{"/api/patients/fetch", `{bad json`, 200, ``},
		{"/api/patients/fetch", ``, 200, ``},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
		if rec.Code != c.status || strings.TrimSpace(rec.Body.String()) != c.resp {
			t.Errorf("%s %q: %d %s, want %d %s", c.path, c.body, rec.Code, rec.Body.String(), c.status, c.resp)
		}
	}
}
