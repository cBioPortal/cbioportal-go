package main

import (
	"bytes"
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
		{"repeats", `[1,1,1,2,3]`, `[3,1,2,2]`, defaultOptions()},
		{"repeats unordered", `[{"id":1},{"id":1},{"id":2}]`, `[{"id":2},{"id":2}]`, unordered},
		{"more only than shown", `[9,8,7,6,5,4,3,2,1,0]`, `[0,10,11,12,13,14,15]`, defaultOptions()},
		{"more only than shown unordered", `[9,8,7,6,5,4,3,2,1,0]`, `[0,10,11,12,13,14,15]`, unordered},
		{"ties with scalars", `[1,{"g":1,"p":"x"},2,{"g":1,"p":"y"}]`, `[1,{"g":1,"p":"x"},2,{"g":1,"p":"y"}]`, ties},
	} {
		whole := diff(t, c.a, c.b, c.o)
		// In memory, and spilling every record to disk.
		for _, budget := range []int{sortBudget, 1} {
			prev := sortBudget
			sortBudget = budget
			got, ok := compareStream(bytes.NewReader([]byte(c.a)), bytes.NewReader([]byte(c.b)), c.o)
			sortBudget = prev
			if !ok {
				t.Errorf("%s: not streamed", c.name)
				continue
			}
			if got.Count != whole.Count || !reflect.DeepEqual(got.Diffs, whole.Diffs) {
				t.Errorf("%s (sort budget %d): streamed %d %q, whole %d %q", c.name, budget, got.Count, got.Diffs, whole.Count, whole.Diffs)
			}
		}
	}
	for _, body := range []string{`{"a":1}`, `[1]x`, `[1`, `"s"`} {
		if _, ok := compareStream(bytes.NewReader([]byte(body)), bytes.NewReader([]byte(`[1]`)), defaultOptions()); ok {
			t.Errorf("%s: streamed", body)
		}
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/x/r.json"
	rq := request{Method: "POST", Path: "/api/x", Body: `{"a":1}`}
	body := []byte("[1,\n2]")
	writeCache(path, rq, response{Status: 200, Body: body})
	r, ok := readCache(path)
	if !ok || r.Status != 200 || string(r.Body) != string(body) {
		t.Fatalf("read %v %d %q", ok, r.Status, r.Body)
	}
	// A body past spoolAbove is read from the cache file, and written again
	// from it.
	prev := spoolAbove
	spoolAbove = 3
	r, ok = readCache(path)
	spoolAbove = prev
	if b, err := r.bytes(); !ok || r.spool == nil || err != nil || string(b) != string(body) || string(r.head(2)) != "[1" {
		t.Fatalf("spooled read %v %v %q %v", ok, r.spool != nil, b, err)
	}
	copied := dir + "/copy.json"
	writeCache(copied, rq, r)
	r.release()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("release removed the cache file: %v", err)
	}
	if r, ok := readCache(copied); !ok || string(r.Body) != string(body) {
		t.Fatalf("copy %v %q", ok, r.Body)
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
	ra, rb := response{Status: 200, Body: []byte(a.String())}, response{Status: 200, Body: []byte(b.String())}
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
