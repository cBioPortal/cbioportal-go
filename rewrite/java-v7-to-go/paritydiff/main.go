// Command paritydiff sends a corpus of API requests to two cBioPortal origins
// and compares the responses, to measure and track API parity between this
// Go API and the Java API (e.g. www.cbioportal.org, which serves the same data
// as the go.cbioportal.org mirror).
//
//	go run ./rewrite/java-v7-to-go/paritydiff -a http://localhost:18091 -b https://www.cbioportal.org \
//	    -corpus test/parity/corpus.txt
//
// JSON responses are compared after normalization: key order is irrelevant,
// numbers match within a relative tolerance, and per-request options in the
// corpus sort arrays whose order Java leaves unspecified, drop keys, or
// treat null as absent (see parseCorpus). Responses from -b are cached on
// disk (-cache) and fetched at most -rate requests per second, so reruns do
// not hit the remote origin again. With -retry, requests that failed in
// transport, with a gateway status (502, 503, 504, as while a deployment
// rolls out, or Cloudflare's 520 to 524 when the origin is unreachable or
// times out) or with a 500 from -b only are sent once more after the given
// pause.
//
// With -self-check every request is sent to -b a second time (B'), right
// after the first, and B is compared with B' the same way. A request whose
// two answers differ is nondeterministic in the reference: a difference
// between A and B is then not attributed to A (outcome "nondeterministic"),
// and the result records the B/B' differences either way. Both B requests
// count toward -rate.
//
// Memory does not grow with response size: requests are compared one at a
// time, bodies over -spool-above are kept in temporary files (-spool),
// top-level arrays larger than -stream-above are compared element by element
// with an external sort of their element records (compareStream, within
// -sort-budget), and other bodies larger than -decode-above are compared
// byte for byte. A body over -max-body (2 GiB) is not read, which bounds
// the disk and time one request takes: the request is a known difference
// when B's body is the one over it, and an error otherwise. Without GOMEMLIMIT the
// collector's soft limit is 1 GiB.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

// userAgent is sent with every request: a browser's by default, as www
// routes non-browser clients elsewhere; api.cbioportal.org refuses browsers
// (-ua).
var userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// response is a status and body. Bodies up to spoolAbove bytes are held in
// Body; larger ones are spooled to a file, so memory does not grow with them.
type response struct {
	Status int
	Body   []byte
	spool  *os.File // the body, when spooled
	off    int64    // where the body starts in spool
	size   int64    // the spooled body's size
	temp   bool     // spool is a temporary file, removed by release
}

// spoolAbove is the body size in bytes above which a body is kept in a file.
var spoolAbove = 1 << 20

// Size is the body's size in bytes.
func (r response) Size() int64 {
	if r.spool != nil {
		return r.size
	}
	return int64(len(r.Body))
}

// reader reads the body.
func (r response) reader() *io.SectionReader {
	if r.spool != nil {
		return io.NewSectionReader(r.spool, r.off, r.size)
	}
	return io.NewSectionReader(bytes.NewReader(r.Body), 0, int64(len(r.Body)))
}

// head is the start of the body, at most n bytes.
func (r response) head(n int) []byte {
	if r.spool == nil {
		return cut(r.Body, n)
	}
	b := make([]byte, min(int64(n), r.size))
	k, _ := r.spool.ReadAt(b, r.off)
	return b[:k]
}

// bytes is the whole body in memory.
func (r response) bytes() ([]byte, error) {
	if r.spool == nil {
		return r.Body, nil
	}
	b := make([]byte, r.size)
	_, err := r.spool.ReadAt(b, r.off)
	return b, err
}

// writeBody writes at most max bytes of the body (0: whole) to w.
func (r response) writeBody(w io.Writer, max int64) error {
	var src io.Reader = r.reader()
	if max > 0 {
		src = io.LimitReader(src, max)
	}
	_, err := io.Copy(w, src)
	return err
}

// release closes a spooled body, removing a temporary one.
func (r response) release() {
	if r.spool == nil {
		return
	}
	r.spool.Close()
	if r.temp {
		os.Remove(r.spool.Name())
	}
}

// readBody reads a body, spooling it to a temporary file past spoolAbove.
func readBody(status int, src io.Reader) (response, error) {
	var buf bytes.Buffer
	n, err := io.CopyN(&buf, src, int64(spoolAbove)+1)
	if err != nil && err != io.EOF {
		return response{}, err
	}
	if n <= int64(spoolAbove) {
		return response{Status: status, Body: buf.Bytes()}, nil
	}
	f, err := os.CreateTemp(spoolDir, "paritydiff-body-*")
	if err != nil {
		return response{}, err
	}
	r := response{Status: status, spool: f, temp: true}
	k, err := io.Copy(f, io.LimitReader(io.MultiReader(&buf, src), maxBody+1))
	if err == nil && k > maxBody {
		err = fmt.Errorf("%w: larger than %d bytes (-max-body)", errTooLarge, maxBody)
	}
	if err != nil {
		r.release()
		return response{}, err
	}
	r.size = k
	return r, nil
}

// errTooLarge fails a request whose body is over -max-body.
var errTooLarge = errors.New("body too large")

// maxBody is the largest body read, in bytes; a larger one fails the
// request, bounding the disk and time one request takes.
var maxBody int64 = 2 << 30

// cacheHeader is the first line of a cache file; the response body follows
// it as sent. (Files of one JSON line also holding the body as a string are
// read too.)
type cacheHeader struct {
	Method  string `json:"Method,omitempty"`
	Path    string `json:"Path,omitempty"`
	Request string `json:"Request,omitempty"`
	Status  int    `json:"status"`
	Body    string `json:"body,omitempty"`
}

// readCache reads a cache file; a body past spoolAbove is read from the
// file as needed.
func readCache(path string) (response, bool) {
	f, err := os.Open(path)
	if err != nil {
		return response{}, false
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return response{}, false
	}
	br := bufio.NewReader(f)
	line, err := br.ReadBytes('\n')
	split := err == nil
	if err != nil && err != io.EOF {
		f.Close()
		return response{}, false
	}
	var h cacheHeader
	if json.Unmarshal(bytes.TrimSuffix(line, []byte("\n")), &h) != nil {
		f.Close()
		return response{}, false
	}
	if !split {
		f.Close()
		return response{Status: h.Status, Body: []byte(h.Body)}, true
	}
	offset := int64(len(line))
	if size := st.Size() - offset; size > int64(spoolAbove) {
		// The body stays in the cache file, read where it starts.
		return response{Status: h.Status, spool: f, size: size, off: offset}, true
	}
	defer f.Close()
	body, err := io.ReadAll(br)
	if err != nil {
		return response{}, false
	}
	return response{Status: h.Status, Body: body}, true
}

// writeCache writes a cache file: the header line, then the body.
func writeCache(path string, rq request, r response) {
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	line, err := json.Marshal(cacheHeader{Method: rq.Method, Path: rq.Path, Request: rq.Body, Status: r.Status})
	if err != nil {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	_, err = f.Write(append(line, '\n'))
	if err == nil {
		err = r.writeBody(f, 0)
	}
	if f.Close() != nil || err != nil {
		_ = os.Remove(path)
	}
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
			if r, ok := readCache(path); ok {
				return r, nil
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
	r, err := readBody(resp.StatusCode, resp.Body)
	if err != nil {
		return response{}, err
	}
	f.fetched++
	// Server errors and throttling are not cached, so a rerun retries them.
	if path != "" && r.Status < 500 && r.Status != 429 {
		writeCache(path, rq, r)
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
	// nondeterministic: A and B differ, and so do B and B' (-self-check).
	nondeterministic = "nondeterministic"
	// javaFailed: A answered and B failed with a 5xx, after any retry. A
	// valid request B cannot serve is B's failure; A's answer is unchecked.
	javaFailed = "java-failed"
)

// Self-check results (result.Reference).
const (
	refSame       = "same"       // B and B' agree
	refDiffers    = "differs"    // B and B' differ
	refUnverified = "unverified" // B' failed in transport or with a gateway status
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
	// Reference is B compared with B' under -self-check (refSame, …), and
	// ReferenceDiffs their first differences.
	Reference      string   `json:"reference,omitempty"`
	ReferenceDiffs []string `json:"referenceDiffs,omitempty"`
}

// selfCheck records how B compares with B' in res, and makes a difference
// between A and B "nondeterministic" when B and B' differ too.
func selfCheck(res result, rq request, rb, rb2 response, errB2 error, opts compareOptions) result {
	if errB2 != nil {
		res.Reference, res.ReferenceDiffs = refUnverified, []string{fmt.Sprintf("b': %v", errB2)}
		return res
	}
	self := compareResponses(rq, rb, rb2, opts)
	switch {
	case self.Outcome == identical:
		res.Reference = refSame
		return res
	case gateway(rb.Status) || gateway(rb2.Status):
		res.Reference = refUnverified
	default:
		res.Reference = refDiffers
		if res.Outcome == differs || res.Outcome == status {
			res.Outcome = nondeterministic
		}
	}
	res.ReferenceDiffs = self.Diffs
	return res
}

// transient reports whether a result may be a passing failure of either
// origin rather than a difference: a transport error, a gateway status, or
// a 500 from B where A answered. A 500 from A is never retried, so a go
// failure always shows.
func transient(r result) bool {
	return r.Outcome == failed || (r.Outcome == status && (gateway(r.StatusA) || gateway(r.StatusB) ||
		(r.StatusA/100 == 2 && r.StatusB == 500)))
}

// settleJavaFailed marks a status mismatch where A answered and B failed
// with a 5xx as B's failure.
func settleJavaFailed(r result) result {
	if r.Outcome == status && r.StatusA/100 == 2 && r.StatusB/100 == 5 {
		r.Outcome = javaFailed
	}
	return r
}

// fetchFailed is the result of a request A or B did not answer. An answer
// from B over -max-body is a known difference rather than an error, when A
// answered or was over -max-body too: it is a request too large to replay.
func fetchFailed(name string, statusA int, errA, errB error) result {
	res := result{Name: name, Outcome: failed, Diffs: []string{fmt.Sprintf("a: %v", errA), fmt.Sprintf("b: %v", errB)}}
	if errors.Is(errB, errTooLarge) && (errA == nil || errors.Is(errA, errTooLarge)) {
		res.Outcome, res.Known, res.StatusA = known, "B's answer is larger than -max-body, so it is not compared", statusA
	}
	return res
}

// gateway reports whether a status is a proxy's while the origin is down,
// rolling out or too slow: 502 to 504, and Cloudflare's 520 to 524.
func gateway(code int) bool {
	return code == 502 || code == 503 || code == 504 || (code >= 520 && code <= 524)
}

func compareResponses(rq request, ra, rb response, opts compareOptions) result {
	res := result{Name: rq.Name, StatusA: ra.Status, StatusB: rb.Status}
	if ra.Status != rb.Status {
		res.Outcome = status
		switch {
		case rq.Opts.Known != "":
			res.Outcome, res.Known = known, rq.Opts.Known
		case rq.Opts.KnownIfBFails != "" && ra.Status/100 == 2 && rb.Status >= 400 && rb.Status < 600 && !gateway(rb.Status):
			res.Outcome, res.Known = known, rq.Opts.KnownIfBFails
		case rq.Opts.KnownIfBEmpty != "" && rb.Status == 200 && emptyBody(rb):
			res.Outcome, res.Known = known, rq.Opts.KnownIfBEmpty
		}
		res.Diffs = []string{fmt.Sprintf("a: %.200s", oneLine(head(ra))), fmt.Sprintf("b: %.200s", oneLine(head(rb)))}
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
	opts.KeyRelTol = rq.Opts.KeyRelTol
	d, streamed := diffResult{}, false
	total := ra.Size() + rb.Size()
	if total > int64(streamAbove) {
		d, streamed = compareStream(ra.reader(), rb.reader(), opts)
	}
	switch {
	case streamed:
	case total > int64(decodeAbove):
		// Too large to decode whole: only byte-identical bodies are equal.
		if same, err := sameBytes(ra.reader(), rb.reader()); err != nil {
			d.add(&opts, "", "comparison failed: %v", err)
		} else if !same {
			d.add(&opts, "", "bodies of %d and %d bytes differ; not arrays, and too large to compare as JSON (-decode-above): a=%.80q b=%.80q",
				ra.Size(), rb.Size(), head(ra), head(rb))
		}
	default:
		ba, errA := ra.bytes()
		bb, errB := rb.bytes()
		if errA != nil || errB != nil {
			d.add(&opts, "", "reading the bodies failed: %v %v", errA, errB)
			break
		}
		ja, errA := decodeJSON(ba)
		jb, errB := decodeJSON(bb)
		switch {
		case errA == nil && errB == nil:
			d = compareJSON(ja, jb, opts)
		case !bytes.Equal(bytes.TrimSpace(ba), bytes.TrimSpace(bb)):
			d.add(&opts, "", "non-JSON bodies differ: a=%.80q b=%.80q", head(ra), head(rb))
		}
	}
	res.Count, res.Diffs = d.Count, d.Diffs
	switch {
	case d.Count == 0:
		res.Outcome = identical
	case rq.Opts.Known != "":
		res.Outcome, res.Known = known, rq.Opts.Known
	case rq.Opts.KnownIfEmpty != "" && emptyArray(ra):
		res.Outcome, res.Known = known, rq.Opts.KnownIfEmpty
	case rq.Opts.KnownIfBEmpty != "" && emptyBody(rb):
		res.Outcome, res.Known = known, rq.Opts.KnownIfBEmpty
	case rq.Opts.KnownIfASubset != "" && d.aInB():
		res.Outcome, res.Known = known, rq.Opts.KnownIfASubset
	case rq.Opts.KnownIfBSubset != "" && d.bInA():
		res.Outcome, res.Known = known, rq.Opts.KnownIfBSubset
	default:
		res.Outcome = differs
	}
	return res
}

// emptyArray reports whether a body is an empty JSON array.
func emptyArray(r response) bool {
	if r.Size() > 64 {
		return false
	}
	b := bytes.TrimSpace(r.head(64))
	return len(b) >= 2 && b[0] == '[' && len(bytes.TrimSpace(b[1:len(b)-1])) == 0 && b[len(b)-1] == ']'
}

// emptyBody reports whether a body is empty or only white space.
func emptyBody(r response) bool {
	return r.Size() <= 64 && len(bytes.TrimSpace(r.head(64))) == 0
}

func setOf(keys []string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// streamAbove is the combined size of two bodies above which top-level
// arrays are compared element by element (compareStream).
var streamAbove = 8 << 20

// decodeAbove is the combined size of two bodies above which bodies that are
// not arrays are only compared byte for byte, as decoding them whole would
// take about ten times their size.
var decodeAbove = 32 << 20

// head is the start of a body, for messages.
func head(r response) string { return string(r.head(4096)) }

// sameBytes reports whether two bodies are byte for byte the same.
func sameBytes(a, b *io.SectionReader) (bool, error) {
	if a.Size() != b.Size() {
		return false, nil
	}
	ba, bb := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		n, errA := io.ReadFull(a, ba)
		m, errB := io.ReadFull(b, bb)
		if !bytes.Equal(ba[:n], bb[:m]) {
			return false, nil
		}
		if errA == io.EOF || errA == io.ErrUnexpectedEOF {
			return errB == io.EOF || errB == io.ErrUnexpectedEOF, nil
		}
		if errA != nil {
			return false, errA
		}
		if errB != nil {
			return false, errB
		}
	}
}

// cut returns s cut to max bytes (0: whole).
func cut(s []byte, max int) []byte {
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
	selfCheckB := flag.Bool("self-check", false, "send every request to B twice and compare the two answers; A/B differences where they differ are \"nondeterministic\"")
	retry := flag.Duration("retry", -1, "send requests that failed in transport, with 502/503/504/520-524, or with 500 from B only once more after this pause (negative: no retry)")
	flag.StringVar(&userAgent, "ua", userAgent, "User-Agent header (api.cbioportal.org refuses browser user agents)")
	flag.IntVar(&streamAbove, "stream-above", streamAbove, "compare top-level arrays element by element when the two bodies together exceed this many bytes, to bound memory")
	flag.IntVar(&decodeAbove, "decode-above", decodeAbove, "compare bodies that are not arrays only byte for byte when the two together exceed this many bytes")
	flag.IntVar(&spoolAbove, "spool-above", spoolAbove, "keep response bodies larger than this many bytes in temporary files instead of memory")
	flag.IntVar(&sortBudget, "sort-budget", sortBudget, "memory in bytes one sort of a large array's element records may use before it spills to disk")
	flag.Int64Var(&maxBody, "max-body", maxBody, "fail requests whose body is larger than this many bytes")
	flag.StringVar(&spoolDir, "spool", "", "directory of spooled bodies and sort runs (default: the system's temporary directory)")
	flag.Parse()
	if os.Getenv("GOMEMLIMIT") == "" {
		// A soft limit: the collector works harder near it, so the garbage
		// of comparing large responses is returned early.
		debug.SetMemoryLimit(1 << 30)
	}

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
		if errA != nil || errB != nil {
			res = fetchFailed(rq.Name, ra.Status, errA, errB)
		} else {
			res = compareResponses(rq, ra, rb, opts)
		}
		// One request's bodies are held at a time, and A's no longer than
		// needed: they are dumped (and removed again below if the request
		// settles) before B is asked a second time.
		var pathA, pathB string
		if *dump != "" {
			pathA, pathB = filepath.Join(*dump, rq.Name+".a.json"), filepath.Join(*dump, rq.Name+".b.json")
			if res.Outcome != identical && errA == nil && errB == nil {
				if err := os.MkdirAll(*dump, 0o755); err != nil {
					fatal(err)
				}
				dumpBody(pathA, ra, int64(*dumpMax))
				dumpBody(pathB, rb, int64(*dumpMax))
			}
		}
		ra.release()
		if errA == nil && errB == nil && *selfCheckB {
			rb2, errB2 := fb.get(rq)
			res = selfCheck(res, rq, rb, rb2, errB2, opts)
			rb2.release()
		}
		rb.release()
		res.Request = rq.Method + " " + rq.Path
		res.Body = rq.Body
		if len(res.Body) > 200 {
			res.Body = res.Body[:200] + "…"
		}
		res.MillisA = msA
		if *dump != "" && (res.Outcome == identical || res.Outcome == failed ||
			(*dumpUnexplained && (res.Outcome == known || res.Outcome == nondeterministic))) {
			// A retry that settles the request leaves no dump behind.
			_ = os.Remove(pathA)
			_ = os.Remove(pathB)
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
	for i, r := range results {
		results[i] = settleJavaFailed(r)
		counts[results[i].Outcome]++
	}
	fmt.Printf("\n%d requests: %d identical, %d known differences, %d differ, %d status mismatches, %d errors, %d B failures (%d fetched from B)\n",
		len(results), counts[identical], counts[known], counts[differs], counts[status], counts[failed], counts[javaFailed], fb.fetched)
	if *selfCheckB {
		refs := map[string]int{}
		for _, r := range results {
			refs[r.Reference]++
		}
		fmt.Printf("self-check: B answered %d requests differently the second time (%d of them also differ from A), %d unverified\n",
			refs[refDiffers], counts[nondeterministic], refs[refUnverified])
	}
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

// dumpBody writes at most max bytes of a body (0: whole) to path.
func dumpBody(path string, r response, max int64) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	r.writeBody(f, max)
	f.Close()
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
	case nondeterministic:
		fmt.Fprintf(&sb, "NONDET  %s (B differs from B')\n", r.Name)
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
