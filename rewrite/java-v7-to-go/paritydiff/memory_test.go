package main

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// syntheticBody writes a JSON array of n molecular-data-like records: B
// (other) in reverse order with its keys swapped, the value of record 777
// changed and, with drop, record 5 left out.
func syntheticBody(w http.ResponseWriter, n int, other, drop bool) {
	bw := bufio.NewWriterSize(w, 64<<10)
	bw.WriteString("[")
	first := true
	for k := 0; k < n; k++ {
		i := k
		if other {
			i = n - 1 - k
			if drop && i == 5 {
				continue
			}
		}
		if !first {
			bw.WriteString(",")
		}
		first = false
		v := i
		if other && i == 777 {
			v = -1
		}
		if other {
			fmt.Fprintf(bw, `{"value":"%d.5","studyId":"study_x","sampleId":"s%07d","molecularProfileId":"study_x_mrna","entrezGeneId":%d}`, v, i, i%20000)
		} else {
			fmt.Fprintf(bw, `{"molecularProfileId":"study_x_mrna","sampleId":"s%07d","entrezGeneId":%d,"studyId":"study_x","value":"%d.5"}`, i, i%20000, v)
		}
	}
	bw.WriteString("]")
	bw.Flush()
}

// peakHeap runs f and returns the most heap it had allocated above the heap
// before it, sampled every millisecond.
func peakHeap(f func()) uint64 {
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	var peak atomic.Uint64
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > base && ms.HeapAlloc-base > peak.Load() {
				peak.Store(ms.HeapAlloc - base)
			}
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	f()
	close(done)
	<-sampled
	return peak.Load()
}

// TestLargeResponsesBoundedMemory fetches and compares two large array
// bodies (and two large object bodies) and checks that the heap stays far
// below their size: bodies are spooled to disk, element records sorted
// within sortBudget, and bodies that are not arrays compared byte for byte
// past decodeAbove.
func TestLargeResponsesBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("large bodies")
	}
	const n = 120_000 // about 14 MB per body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a/x", "/a/drop":
			syntheticBody(w, n, false, false)
		case "/b/x":
			syntheticBody(w, n, true, false)
		case "/b/drop":
			syntheticBody(w, n, true, true)
		case "/a/object", "/b/object":
			w.Write([]byte(`{"rows":`))
			syntheticBody(w, n, r.URL.Path == "/b/object", false)
			w.Write([]byte(`}`))
		}
	}))
	defer srv.Close()
	prevBudget, prevDir, prevDecode := sortBudget, spoolDir, decodeAbove
	defer func() { sortBudget, spoolDir, decodeAbove = prevBudget, prevDir, prevDecode }()
	sortBudget, spoolDir, decodeAbove = 1<<20, t.TempDir(), 8<<20

	fa := &fetcher{origin: srv.URL + "/a", client: srv.Client()}
	fb := &fetcher{origin: srv.URL + "/b", client: srv.Client()}
	const limit = 8 << 20
	for _, c := range []struct {
		path  string
		count int
		first string
	}{
		{"/x", 1, "["},
		{"/drop", 4, "$: length a=120000 b=119999"},
		{"/object", 1, "$: bodies of "},
	} {
		rq := request{Name: c.path, Method: "GET", Path: c.path, Opts: requestOptions{Unordered: true}}
		var res result
		var size int64
		peak := peakHeap(func() {
			ra, err := fa.get(rq)
			if err != nil {
				t.Fatal(err)
			}
			defer ra.release()
			rb, err := fb.get(rq)
			if err != nil {
				t.Fatal(err)
			}
			defer rb.release()
			size = ra.Size() + rb.Size()
			res = compareResponses(rq, ra, rb, defaultOptions())
		})
		t.Logf("%s: bodies %d MB, peak heap %.1f MB, %d differences %q", c.path, size>>20, float64(peak)/(1<<20), res.Count, res.Diffs)
		if size < 3*limit {
			t.Fatalf("%s: bodies of %d bytes are too small to show the bound", c.path, size)
		}
		if peak > limit {
			t.Errorf("%s: peak heap %d MB, want at most %d MB", c.path, peak>>20, limit>>20)
		}
		if res.Count != c.count || len(res.Diffs) == 0 || !strings.HasPrefix(res.Diffs[0], c.first) {
			t.Errorf("%s: %d differences %q, want %d starting %q", c.path, res.Count, res.Diffs, c.count, c.first)
		}
	}
}
