package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestCompareStreamMatchesWhole checks that the element-by-element comparison
// of large arrays finds what the whole-document comparison finds.
func TestCompareStreamMatchesWhole(t *testing.T) {
	unordered := defaultOptions()
	unordered.Unordered = true
	ties := defaultOptions()
	ties.Ties = []string{"g"}
	ignore := defaultOptions()
	ignore.Ignore = map[string]bool{"x": true}
	ignore.NullIsMissing = true
	nested := defaultOptions()
	nested.UnorderedKeys = map[string]bool{"s": true}
	for _, c := range []struct {
		name string
		a, b string
		o    compareOptions
	}{
		{"identical", `[{"id":1,"v":"x"},{"id":2,"v":"y"}]`, `[{"v":"x","id":1},{"id":2,"v":"y"}]`, defaultOptions()},
		{"field differs", `[{"id":1,"v":"x","extra":true}]`, `[{"id":1,"v":"y","more":null}]`, defaultOptions()},
		{"order only", `[{"id":1},{"id":2},{"id":3}]`, `[{"id":3},{"id":1},{"id":2}]`, defaultOptions()},
		{"order and content", `[{"id":1},{"id":2}]`, `[{"id":2},{"id":3}]`, defaultOptions()},
		{"unordered", `[{"id":1},{"id":2},{"id":3}]`, `[{"id":3},{"id":1},{"id":2}]`, unordered},
		{"unordered aligns by identity", `[{"studyId":"b","count":1},{"studyId":"a","count":5}]`, `[{"studyId":"a","count":9},{"studyId":"b","count":1}]`, unordered},
		{"unordered nested", `[{"g":"TP53","s":["b","a"]},{"g":"KRAS","s":[]}]`, `[{"g":"KRAS","s":[]},{"g":"TP53","s":["a","b"]}]`, unordered},
		{"length differs", `[{"id":1},{"id":2},{"id":4}]`, `[{"id":1},{"id":2}]`, defaultOptions()},
		{"length differs unordered", `[{"id":4},{"id":1}]`, `[{"id":1},{"id":2},{"id":3}]`, unordered},
		{"ties free within run", `[{"g":1,"p":"x"},{"g":1,"p":"y"},{"g":2,"p":"z"}]`, `[{"g":1,"p":"y"},{"g":1,"p":"x"},{"g":2,"p":"z"}]`, ties},
		{"ties keep run order", `[{"g":1,"p":"x"},{"g":1,"p":"y"},{"g":2,"p":"z"}]`, `[{"g":2,"p":"z"},{"g":1,"p":"x"},{"g":1,"p":"y"}]`, ties},
		{"ignore and null", `[{"id":1,"x":5,"n":null}]`, `[{"id":1,"x":6}]`, ignore},
		{"unordered keys", `[{"id":"l1","s":["b","a"]}]`, `[{"id":"l1","s":["a","b"]}]`, nested},
		{"float tolerance", `[0.30000000000000004, 1e-20]`, `[0.3, 0]`, defaultOptions()},
		{"scalars", `[1,"a",null,true]`, `[1,"b",null,false]`, defaultOptions()},
		{"empty", `[]`, `[ ]`, defaultOptions()},
	} {
		whole := diff(t, c.a, c.b, c.o)
		got, ok := compareStream([]byte(c.a), []byte(c.b), c.o)
		if !ok {
			t.Errorf("%s: not streamed", c.name)
			continue
		}
		if got.Count != whole.Count || !reflect.DeepEqual(got.Diffs, whole.Diffs) {
			t.Errorf("%s: streamed %d %q, whole %d %q", c.name, got.Count, got.Diffs, whole.Count, whole.Diffs)
		}
	}
	for _, body := range []string{`{"a":1}`, `[1]x`, `[1`, `"s"`} {
		if _, ok := compareStream([]byte(body), []byte(`[1]`), defaultOptions()); ok {
			t.Errorf("%s: streamed", body)
		}
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/x/r.json"
	rq := request{Method: "POST", Path: "/api/x", Body: `{"a":1}`}
	body := []byte("[1,\n2]")
	writeCache(path, rq, response{200, body})
	r, ok := readCache(path)
	if !ok || r.Status != 200 || string(r.Body) != string(body) {
		t.Fatalf("read %v %d %q", ok, r.Status, r.Body)
	}
	old := dir + "/old.json"
	if err := os.WriteFile(old, []byte(`{"Method":"GET","status":404,"body":"{\"m\":1}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, ok := readCache(old); !ok || r.Status != 404 || string(r.Body) != `{"m":1}` {
		t.Fatalf("old format: %v %d %q", ok, r.Status, r.Body)
	}
}

// TestCompareResponsesStreams checks that large array bodies take the
// element-by-element path and agree with the whole comparison.
func TestCompareResponsesStreams(t *testing.T) {
	var a, b strings.Builder
	a.WriteString("[")
	b.WriteString("[")
	for i := 0; i < 20000; i++ {
		if i > 0 {
			a.WriteString(",")
			b.WriteString(",")
		}
		fmt.Fprintf(&a, `{"sampleId":"s%d","value":%d}`, i, i)
		v := i
		if i == 777 {
			v = -1
		}
		fmt.Fprintf(&b, `{"value":%d,"sampleId":"s%d"}`, v, i)
	}
	a.WriteString("]")
	b.WriteString("]")
	rq := request{Name: "big", Opts: requestOptions{Unordered: true}}
	ra, rb := response{200, []byte(a.String())}, response{200, []byte(b.String())}
	prev := streamAbove
	defer func() { streamAbove = prev }()
	streamAbove = 1 << 30
	whole := compareResponses(rq, ra, rb, defaultOptions())
	streamAbove = 1 << 10
	streamed := compareResponses(rq, ra, rb, defaultOptions())
	if whole.Count != 1 || streamed.Count != whole.Count || !reflect.DeepEqual(streamed.Diffs, whole.Diffs) {
		t.Fatalf("whole %d %q, streamed %d %q", whole.Count, whole.Diffs, streamed.Count, streamed.Diffs)
	}
}
