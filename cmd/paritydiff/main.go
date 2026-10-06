// Command paritydiff sends a corpus of API requests to two cBioPortal origins
// and compares the responses, to measure and track API parity between this
// Go API and the Java API (e.g. www.cbioportal.org, which serves the same data
// as the go.cbioportal.org mirror).
//
//	go run ./cmd/paritydiff -a http://localhost:18091 -b https://www.cbioportal.org \
//	    -corpus test/parity/corpus.txt
//
// JSON responses are compared after normalization: key order is irrelevant,
// numbers match within a relative tolerance, and per-request options in the
// corpus sort arrays whose order Java leaves unspecified, drop keys, or
// treat null as absent (see parseCorpus). Responses from -b are cached on
// disk (-cache) and fetched at most -rate requests per second, so reruns do
// not hit the remote origin again. With -retry, requests that failed in
// transport or with a gateway status (502, 503, 504, as while a deployment
// rolls out) are sent once more after the given pause.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// userAgent is sent with every request: a browser's by default, as www
// routes non-browser clients elsewhere; api.cbioportal.org refuses browsers
// (-ua).
var userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

type response struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

type fetcher struct {
	origin   string
	client   *http.Client
	cacheDir string // empty: no cache
	refresh  bool
	interval time.Duration
	last     time.Time
	fetched  int
}

func (f *fetcher) cachePath(rq request) string {
	h := sha256.Sum256([]byte(rq.Method + " " + rq.Path + "\n" + rq.Body))
	u, _ := url.Parse(f.origin)
	return filepath.Join(f.cacheDir, u.Host, hex.EncodeToString(h[:12])+".json")
}

func (f *fetcher) get(rq request) (response, error) {
	var path string
	if f.cacheDir != "" {
		path = f.cachePath(rq)
		if !f.refresh {
			if b, err := os.ReadFile(path); err == nil {
				var r response
				if json.Unmarshal(b, &r) == nil {
					return r, nil
				}
			}
		}
	}
	if f.interval > 0 {
		if wait := f.interval - time.Since(f.last); wait > 0 {
			time.Sleep(wait)
		}
		defer func() { f.last = time.Now() }()
	}
	var body io.Reader
	if rq.Body != "" {
		body = strings.NewReader(rq.Body)
	}
	req, err := http.NewRequest(rq.Method, f.origin+rq.Path, body)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if rq.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, err
	}
	f.fetched++
	r := response{Status: resp.StatusCode, Body: string(b)}
	// Server errors and throttling are not cached, so a rerun retries them.
	if path != "" && r.Status < 500 && r.Status != 429 {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			entry := struct {
				Method, Path, Request string
				response
			}{rq.Method, rq.Path, rq.Body, r}
			if out, err := json.Marshal(entry); err == nil {
				_ = os.WriteFile(path, out, 0o644)
			}
		}
	}
	return r, nil
}

// outcome classes, in report order.
const (
	identical = "identical"
	known     = "known"
	differs   = "differs"
	status    = "status"
	failed    = "error"
)

type result struct {
	Name    string   `json:"name"`
	Request string   `json:"request"`        // method and path
	Body    string   `json:"body,omitempty"` // the request body, cut to 200 bytes
	Outcome string   `json:"outcome"`
	Count   int      `json:"differences,omitempty"`
	Diffs   []string `json:"diffs,omitempty"`
	Known   string   `json:"known,omitempty"`
	StatusA int      `json:"statusA"`
	StatusB int      `json:"statusB"`
	MillisA int64    `json:"msA"`
	Retried bool     `json:"retried,omitempty"`
}

// transient reports whether a result may be a passing failure of either
// origin rather than a difference: a transport error or a gateway status.
func transient(r result) bool {
	gateway := func(code int) bool { return code == 502 || code == 503 || code == 504 }
	return r.Outcome == failed || (r.Outcome == status && (gateway(r.StatusA) || gateway(r.StatusB)))
}

func compareResponses(rq request, ra, rb response, opts compareOptions) result {
	res := result{Name: rq.Name, StatusA: ra.Status, StatusB: rb.Status}
	if ra.Status != rb.Status {
		res.Outcome = status
		if rq.Opts.Known != "" {
			res.Outcome, res.Known = known, rq.Opts.Known
		}
		res.Diffs = []string{fmt.Sprintf("a: %.200s", oneLine(ra.Body)), fmt.Sprintf("b: %.200s", oneLine(rb.Body))}
		return res
	}
	if rq.Opts.StatusOnly || ra.Status >= 300 {
		res.Outcome = identical
		return res
	}
	opts.Unordered = opts.Unordered || rq.Opts.Unordered
	opts.NullIsMissing = opts.NullIsMissing || rq.Opts.NullIsMissing
	opts.Ignore = setOf(rq.Opts.Ignore)
	opts.UnorderedKeys = setOf(rq.Opts.UnorderedKeys)
	opts.Ties = rq.Opts.Ties
	ja, errA := decodeJSON([]byte(ra.Body))
	jb, errB := decodeJSON([]byte(rb.Body))
	var d diffResult
	switch {
	case errA == nil && errB == nil:
		d = compareJSON(ja, jb, opts)
	case strings.TrimSpace(ra.Body) != strings.TrimSpace(rb.Body):
		d.add(&opts, "", "non-JSON bodies differ: a=%.80q b=%.80q", ra.Body, rb.Body)
	}
	res.Count, res.Diffs = d.Count, d.Diffs
	switch {
	case d.Count == 0:
		res.Outcome = identical
	case rq.Opts.Known != "":
		res.Outcome, res.Known = known, rq.Opts.Known
	default:
		res.Outcome = differs
	}
	return res
}

func setOf(keys []string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// cut returns s cut to max bytes (0: whole).
func cut(s string, max int) string {
	if max > 0 && len(s) > max {
		return s[:max]
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func main() {
	a := flag.String("a", "http://localhost:18091", "origin A (the API under test)")
	b := flag.String("b", "https://www.cbioportal.org", "origin B (the reference)")
	corpus := flag.String("corpus", "test/parity/corpus.txt", "corpus file")
	cache := flag.String("cache", ".parity/cache", "cache directory for origin B responses (empty: no cache)")
	refresh := flag.Bool("refresh", false, "refetch origin B instead of reading the cache")
	rate := flag.Float64("rate", 2, "maximum requests per second to origin B (0: unlimited)")
	run := flag.String("run", "", "only requests whose name matches this regexp")
	maxDiffs := flag.Int("n", 5, "differences shown per request")
	quiet := flag.Bool("q", false, "list only requests that are not identical")
	report := flag.String("json", "", "also write the results as JSON to this file")
	dump := flag.String("dump", "", "write both bodies of every non-identical response to this directory (<name>.a.json, <name>.b.json)")
	dumpMax := flag.Int("dump-max", 0, "cut dumped bodies to this many bytes (0: whole)")
	dumpUnexplained := flag.Bool("dump-unexplained", false, "dump only responses that differ without a known reason")
	timeout := flag.Duration("timeout", 5*time.Minute, "per-request timeout")
	retry := flag.Duration("retry", -1, "send requests that failed in transport or with 502/503/504 once more after this pause (negative: no retry)")
	flag.StringVar(&userAgent, "ua", userAgent, "User-Agent header (api.cbioportal.org refuses browser user agents)")
	flag.Parse()

	f, err := os.Open(*corpus)
	if err != nil {
		fatal(err)
	}
	reqs, err := parseCorpus(f)
	f.Close()
	if err != nil {
		fatal(err)
	}
	var filter *regexp.Regexp
	if *run != "" {
		if filter, err = regexp.Compile(*run); err != nil {
			fatal(err)
		}
	}

	client := &http.Client{Timeout: *timeout}
	fa := &fetcher{origin: strings.TrimRight(*a, "/"), client: client}
	fb := &fetcher{origin: strings.TrimRight(*b, "/"), client: client, cacheDir: *cache, refresh: *refresh}
	if *rate > 0 {
		fb.interval = time.Duration(float64(time.Second) / *rate)
	}
	opts := defaultOptions()
	opts.MaxDiffs = *maxDiffs

	compare := func(rq request) result {
		start := time.Now()
		ra, errA := fa.get(rq)
		msA := time.Since(start).Milliseconds()
		rb, errB := fb.get(rq)
		var res result
		switch {
		case errA != nil || errB != nil:
			res = result{Name: rq.Name, Outcome: failed, Diffs: []string{fmt.Sprintf("a: %v", errA), fmt.Sprintf("b: %v", errB)}}
		default:
			res = compareResponses(rq, ra, rb, opts)
		}
		res.Request = rq.Method + " " + rq.Path
		res.Body = rq.Body
		if len(res.Body) > 200 {
			res.Body = res.Body[:200] + "…"
		}
		res.MillisA = msA
		if *dump != "" {
			pathA, pathB := filepath.Join(*dump, rq.Name+".a.json"), filepath.Join(*dump, rq.Name+".b.json")
			if res.Outcome == identical || res.Outcome == failed || (*dumpUnexplained && res.Outcome == known) {
				// A retry that settles the request leaves no dump behind.
				_ = os.Remove(pathA)
				_ = os.Remove(pathB)
			} else {
				if err := os.MkdirAll(*dump, 0o755); err != nil {
					fatal(err)
				}
				_ = os.WriteFile(pathA, []byte(cut(ra.Body, *dumpMax)), 0o644)
				_ = os.WriteFile(pathB, []byte(cut(rb.Body, *dumpMax)), 0o644)
			}
		}
		return res
	}

	var results []result
	var selected []request
	for _, rq := range reqs {
		if filter != nil && !filter.MatchString(rq.Name) {
			continue
		}
		res := compare(rq)
		selected = append(selected, rq)
		results = append(results, res)
		printResult(os.Stdout, res, *quiet)
	}
	if *retry >= 0 {
		var again []int
		for i, r := range results {
			if transient(r) {
				again = append(again, i)
			}
		}
		if len(again) > 0 {
			fmt.Printf("\nretrying %d requests in %s\n", len(again), *retry)
			time.Sleep(*retry)
			for _, i := range again {
				res := compare(selected[i])
				res.Retried = true
				results[i] = res
				printResult(os.Stdout, res, *quiet)
			}
		}
	}
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Outcome]++
	}
	fmt.Printf("\n%d requests: %d identical, %d known differences, %d differ, %d status mismatches, %d errors (%d fetched from B)\n",
		len(results), counts[identical], counts[known], counts[differs], counts[status], counts[failed], fb.fetched)
	if *report != "" {
		out, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*report, out, 0o644); err != nil {
			fatal(err)
		}
	}
	if counts[differs]+counts[status]+counts[failed] > 0 {
		os.Exit(1)
	}
}

func printResult(w io.Writer, r result, quiet bool) {
	if quiet && r.Outcome == identical {
		return
	}
	var sb bytes.Buffer
	switch r.Outcome {
	case identical:
		fmt.Fprintf(&sb, "ok      %s\n", r.Name)
	case known:
		fmt.Fprintf(&sb, "known   %s (%d differences): %s\n", r.Name, r.Count, r.Known)
	case differs:
		fmt.Fprintf(&sb, "DIFF    %s (%d differences)\n", r.Name, r.Count)
	case status:
		fmt.Fprintf(&sb, "STATUS  %s a=%d b=%d\n", r.Name, r.StatusA, r.StatusB)
	case failed:
		fmt.Fprintf(&sb, "ERROR   %s\n", r.Name)
	}
	if r.Outcome != known {
		for _, d := range r.Diffs {
			fmt.Fprintf(&sb, "          %s\n", d)
		}
	}
	w.Write(sb.Bytes())
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "paritydiff:", err)
	os.Exit(2)
}
