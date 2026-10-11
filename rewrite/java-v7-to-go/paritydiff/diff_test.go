package main

import (
	"errors"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, s string) any {
	t.Helper()
	v, err := decodeJSON([]byte(s))
	if err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return v
}

func diff(t *testing.T, a, b string, o compareOptions) diffResult {
	t.Helper()
	return compareJSON(mustJSON(t, a), mustJSON(t, b), o)
}

func TestSelfCheck(t *testing.T) {
	rq := request{Name: "r", Opts: requestOptions{Unordered: true}}
	ok := func(body string) response { return response{Status: 200, Body: []byte(body)} }
	for _, c := range []struct {
		name         string
		a, b, b2     response
		errB2        error
		outcome, ref string
	}{
		{"stable and identical", ok(`[1,2]`), ok(`[2,1]`), ok(`[1,2]`), nil, identical, refSame},
		{"stable reference, A differs", ok(`[1]`), ok(`[2]`), ok(`[2]`), nil, differs, refSame},
		{"unstable reference, A differs", ok(`[1]`), ok(`[2]`), ok(`[3]`), nil, nondeterministic, refDiffers},
		{"unstable reference, A agrees", ok(`[1]`), ok(`[1]`), ok(`[3]`), nil, identical, refDiffers},
		{"unstable status", ok(`[1]`), response{Status: 500, Body: []byte("x")}, ok(`[1]`), nil, nondeterministic, refDiffers},
		{"gateway on B'", ok(`[1]`), ok(`[2]`), response{Status: 503}, nil, differs, refUnverified},
		{"transport error on B'", ok(`[1]`), ok(`[2]`), response{}, errors.New("reset"), differs, refUnverified},
	} {
		res := compareResponses(rq, c.a, c.b, defaultOptions())
		res = selfCheck(res, rq, c.b, c.b2, c.errB2, defaultOptions())
		if res.Outcome != c.outcome || res.Reference != c.ref {
			t.Errorf("%s: outcome %s, reference %s; want %s, %s", c.name, res.Outcome, res.Reference, c.outcome, c.ref)
		}
	}
}

func TestCompareKeyOrderIrrelevant(t *testing.T) {
	r := diff(t, `{"a":1,"b":{"x":"y","z":[1,2]}}`, `{"b":{"z":[1,2],"x":"y"},"a":1}`, defaultOptions())
	if r.Count != 0 {
		t.Fatalf("want identical, got %v", r.Diffs)
	}
}

func TestCompareFloatTolerance(t *testing.T) {
	o := defaultOptions()
	if r := diff(t, `[0.30000000000000004, 1e-20, 12345678901234567]`, `[0.3, 0, 12345678901234568]`, o); r.Count != 0 {
		t.Fatalf("within tolerance: %v", r.Diffs)
	}
	if r := diff(t, `[0.3001]`, `[0.3]`, o); r.Count != 1 {
		t.Fatalf("outside tolerance: want 1 difference, got %v", r.Diffs)
	}
	if r := diff(t, `[1]`, `["1"]`, o); r.Count != 1 {
		t.Fatalf("number vs string must differ: %v", r.Diffs)
	}
}

func TestCompareOrderOnlyReportedOnce(t *testing.T) {
	r := diff(t, `[{"id":1},{"id":2},{"id":3}]`, `[{"id":3},{"id":1},{"id":2}]`, defaultOptions())
	if r.Count != 1 || !strings.Contains(r.Diffs[0], "different order") {
		t.Fatalf("want one order difference, got %v", r.Diffs)
	}
	o := defaultOptions()
	o.Unordered = true
	if r := diff(t, `[{"id":1},{"id":2},{"id":3}]`, `[{"id":3},{"id":1},{"id":2}]`, o); r.Count != 0 {
		t.Fatalf("unordered: %v", r.Diffs)
	}
}

func TestCompareUnorderedNested(t *testing.T) {
	o := defaultOptions()
	o.Unordered = true
	a := `[{"g":"TP53","s":["b","a"]},{"g":"KRAS","s":[]}]`
	b := `[{"g":"KRAS","s":[]},{"g":"TP53","s":["a","b"]}]`
	if r := diff(t, a, b, o); r.Count != 0 {
		t.Fatalf("nested arrays sorted too: %v", r.Diffs)
	}
}

func TestCompareUnorderedAlignsByIdentity(t *testing.T) {
	o := defaultOptions()
	o.Unordered = true
	a := `[{"studyId":"b","count":1},{"studyId":"a","count":5}]`
	b := `[{"studyId":"a","count":9},{"studyId":"b","count":1}]`
	r := diff(t, a, b, o)
	if r.Count != 1 || !strings.Contains(r.Diffs[0], "[0].count: a=5 b=9") {
		t.Fatalf("records should line up by studyId: %v", r.Diffs)
	}
}

func TestCompareTiesKeepRunOrder(t *testing.T) {
	o := defaultOptions()
	o.Ties = []string{"g"}
	a := `[{"g":1,"p":"x"},{"g":1,"p":"y"},{"g":2,"p":"z"}]`
	if r := diff(t, a, `[{"g":1,"p":"y"},{"g":1,"p":"x"},{"g":2,"p":"z"}]`, o); r.Count != 0 {
		t.Fatalf("order within a tie is free: %v", r.Diffs)
	}
	if r := diff(t, a, `[{"g":2,"p":"z"},{"g":1,"p":"x"},{"g":1,"p":"y"}]`, o); r.Count != 1 {
		t.Fatalf("order of the tied runs must still match: %v", r.Diffs)
	}
}

func TestCompareUnorderedKeys(t *testing.T) {
	o := defaultOptions()
	o.UnorderedKeys = map[string]bool{"sampleIds": true}
	a := `[{"id":"l1","sampleIds":["b","a"]},{"id":"l2","sampleIds":[]}]`
	if r := diff(t, a, `[{"id":"l1","sampleIds":["a","b"]},{"id":"l2","sampleIds":[]}]`, o); r.Count != 0 {
		t.Fatalf("sampleIds unordered: %v", r.Diffs)
	}
	if r := diff(t, a, `[{"id":"l2","sampleIds":[]},{"id":"l1","sampleIds":["a","b"]}]`, o); r.Count != 1 {
		t.Fatalf("outer list still ordered: %v", r.Diffs)
	}
}

func TestCompareLengthMismatchListsExtras(t *testing.T) {
	r := diff(t, `[{"id":1},{"id":2},{"id":4}]`, `[{"id":1},{"id":2}]`, defaultOptions())
	if r.Count != 2 || !strings.Contains(r.Diffs[0], "length a=3 b=2") || !strings.Contains(r.Diffs[1], `only in a: {"id":4}`) {
		t.Fatalf("got %v", r.Diffs)
	}
}

func TestCompareFieldDiffPaths(t *testing.T) {
	r := diff(t, `[{"id":1,"v":"x","extra":true}]`, `[{"id":1,"v":"y","more":null}]`, defaultOptions())
	want := []string{`$[0].extra: only in a (true)`, `$[0].more: only in b (null)`, `$[0].v: a="x" b="y"`}
	if r.Count != 3 {
		t.Fatalf("got %v", r.Diffs)
	}
	for i, w := range want {
		if strings.TrimPrefix(r.Diffs[i], "$") != strings.TrimPrefix(w, "$") {
			t.Errorf("diff %d: got %q want %q", i, r.Diffs[i], w)
		}
	}
}

func TestCompareNullIsMissingAndIgnore(t *testing.T) {
	o := defaultOptions()
	o.NullIsMissing = true
	o.Ignore = map[string]bool{"importDate": true}
	r := diff(t, `{"a":null,"importDate":"2024","b":[{"importDate":1,"k":2}]}`, `{"importDate":"2025","b":[{"k":2}]}`, o)
	if r.Count != 0 {
		t.Fatalf("got %v", r.Diffs)
	}
}

func TestCompareMaxDiffsCapsListNotCount(t *testing.T) {
	o := defaultOptions()
	o.MaxDiffs = 2
	r := diff(t, `[1,2,3,4]`, `[5,6,7,8]`, o)
	if r.Count != 4 || len(r.Diffs) != 2 {
		t.Fatalf("count %d diffs %v", r.Count, r.Diffs)
	}
}

func TestParseCorpus(t *testing.T) {
	src := `# comment
## studies unordered ignore=importDate,readPermission known="www is newer"
GET /api/studies?projection=SUMMARY

## mutations nullmissing
POST /api/mutations/fetch
{
  "entrezGeneIds": [7157]
}
`
	reqs, err := parseCorpus(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d requests", len(reqs))
	}
	s := reqs[0]
	if s.Name != "studies" || s.Method != "GET" || s.Path != "/api/studies?projection=SUMMARY" || s.Body != "" ||
		!s.Opts.Unordered || len(s.Opts.Ignore) != 2 || s.Opts.Known != "www is newer" {
		t.Fatalf("studies: %+v", s)
	}
	m := reqs[1]
	if m.Method != "POST" || !m.Opts.NullIsMissing || !strings.Contains(m.Body, `"entrezGeneIds": [7157]`) {
		t.Fatalf("mutations: %+v", m)
	}
	if _, err := parseCorpus(strings.NewReader("## a\nGET /x\n## a\nGET /y\n")); err == nil {
		t.Fatal("duplicate names must fail")
	}
	if reqs, err := parseCorpus(strings.NewReader("## a unordered=sampleIds ties=g,h\nGET /x\n")); err != nil ||
		len(reqs[0].Opts.UnorderedKeys) != 1 || len(reqs[0].Opts.Ties) != 2 {
		t.Fatalf("keyed options: %+v %v", reqs, err)
	}
	if _, err := parseCorpus(strings.NewReader("## a bogus\nGET /x\n")); err == nil {
		t.Fatal("unknown option must fail")
	}
	if reqs, err := parseCorpus(strings.NewReader("## a unordered known-if-empty=\"no rows, Java's \" ignore=b\nGET /x\n")); err != nil ||
		reqs[0].Opts.KnownIfEmpty != "no rows, Java's " || reqs[0].Opts.Known != "" || len(reqs[0].Opts.Ignore) != 1 {
		t.Fatalf("known-if-empty: %+v %v", reqs, err)
	}
	if _, err := parseCorpus(strings.NewReader("## a known-if-empty=\"open\nGET /x\n")); err == nil {
		t.Fatal("unterminated known-if-empty must fail")
	}
	if reqs, err := parseCorpus(strings.NewReader("## a known-if-b-fails=\"Java's 500\" unordered\nGET /x\n")); err != nil ||
		reqs[0].Opts.KnownIfBFails != "Java's 500" || reqs[0].Opts.Known != "" || !reqs[0].Opts.Unordered {
		t.Fatalf("known-if-b-fails: %+v %v", reqs, err)
	}
}

func TestCompareResponsesOutcomes(t *testing.T) {
	o := defaultOptions()
	rq := request{Name: "x"}
	if r := compareResponses(rq, response{Status: 200, Body: []byte(`{"a":1}`)}, response{Status: 200, Body: []byte(`{"a":1}`)}, o); r.Outcome != identical {
		t.Fatalf("got %+v", r)
	}
	if r := compareResponses(rq, response{Status: 200, Body: []byte(`{}`)}, response{Status: 400, Body: []byte(`{}`)}, o); r.Outcome != status {
		t.Fatalf("got %+v", r)
	}
	rq.Opts.Known = "reason"
	if r := compareResponses(rq, response{Status: 200, Body: []byte(`{"a":1}`)}, response{Status: 200, Body: []byte(`{"a":2}`)}, o); r.Outcome != known {
		t.Fatalf("got %+v", r)
	}

	rq.Opts.Known, rq.Opts.KnownIfEmpty = "", "only when A is empty"
	for a, want := range map[string]string{"[]": known, " [ ]\n": known, `[{"a":1}]`: differs, `{}`: differs} {
		r := compareResponses(rq, response{Status: 200, Body: []byte(a)}, response{Status: 200, Body: []byte(`[{"a":2}]`)}, o)
		if r.Outcome != want || (want == known && r.Known != "only when A is empty") {
			t.Errorf("a=%q: got %+v, want %s", a, r, want)
		}
	}

	rq.Opts.KnownIfEmpty, rq.Opts.KnownIfBFails = "", "only when B fails"
	for _, c := range []struct {
		a, b int
		want string
	}{
		{200, 500, known}, {200, 400, known}, {204, 404, known},
		{200, 502, status}, {200, 503, status}, {400, 500, status}, {500, 200, status}, {200, 302, status},
	} {
		r := compareResponses(rq, response{Status: c.a, Body: []byte(`[]`)}, response{Status: c.b, Body: []byte(`{}`)}, o)
		if r.Outcome != c.want || (c.want == known && r.Known != "only when B fails") {
			t.Errorf("a=%d b=%d: got %+v, want %s", c.a, c.b, r, c.want)
		}
	}
	if r := compareResponses(rq, response{Status: 200, Body: []byte(`[1]`)}, response{Status: 200, Body: []byte(`[2]`)}, o); r.Outcome != differs {
		t.Errorf("same status: got %+v, want %s", r, differs)
	}
}

func TestTransient(t *testing.T) {
	for _, c := range []struct {
		r    result
		want bool
	}{
		{result{Outcome: failed}, true},
		{result{Outcome: status, StatusA: 502, StatusB: 200}, true},
		{result{Outcome: status, StatusA: 200, StatusB: 504}, true},
		{result{Outcome: status, StatusA: 500, StatusB: 200}, false},
		{result{Outcome: status, StatusA: 200, StatusB: 500}, true},
		{result{Outcome: status, StatusA: 404, StatusB: 500}, false},
		{result{Outcome: differs, StatusA: 200, StatusB: 200}, false},
		{result{Outcome: known, StatusA: 503, StatusB: 200}, false},
	} {
		if got := transient(c.r); got != c.want {
			t.Errorf("transient(%+v) = %v, want %v", c.r, got, c.want)
		}
	}
}

func TestSettleJavaFailed(t *testing.T) {
	for _, c := range []struct {
		r    result
		want string
	}{
		{result{Outcome: status, StatusA: 200, StatusB: 500}, javaFailed},
		{result{Outcome: status, StatusA: 200, StatusB: 504}, javaFailed},
		{result{Outcome: status, StatusA: 500, StatusB: 200}, status},
		{result{Outcome: status, StatusA: 200, StatusB: 404}, status},
		{result{Outcome: known, StatusA: 200, StatusB: 500}, known},
	} {
		if got := settleJavaFailed(c.r).Outcome; got != c.want {
			t.Errorf("settleJavaFailed(%+v) = %s, want %s", c.r, got, c.want)
		}
	}
}

func TestKnownIfBEmpty(t *testing.T) {
	rq := request{Name: "r", Opts: requestOptions{KnownIfBEmpty: "interceptor"}}
	var o compareOptions
	for _, c := range []struct {
		a, b response
		want string
	}{
		{response{Status: 200, Body: []byte(`[1]`)}, response{Status: 200, Body: []byte(``)}, known},
		{response{Status: 400, Body: []byte(`{"message":"x"}`)}, response{Status: 200, Body: []byte("\n")}, known},
		{response{Status: 200, Body: []byte(`[1]`)}, response{Status: 200, Body: []byte(`[]`)}, differs},
		{response{Status: 400, Body: []byte(`{}`)}, response{Status: 500, Body: []byte(``)}, status},
	} {
		if r := compareResponses(rq, c.a, c.b, o); r.Outcome != c.want {
			t.Errorf("a=%d %q b=%d %q: %s, want %s", c.a.Status, c.a.Body, c.b.Status, c.b.Body, r.Outcome, c.want)
		}
	}
}
