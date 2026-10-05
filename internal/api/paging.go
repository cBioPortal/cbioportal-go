package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// selfPagedPaths answer with their own paged envelope (not a bare JSON array),
// so the generic paging below leaves their query params untouched.
var selfPagedPaths = map[string]bool{
	"/api/clinical-data-table/fetch": true,
}

// oneBasedPaths number their pages from 1: ColumnStoreStudyController
// returns items pageSize*(pageNumber-1) onwards, treating pageNumber <= 0 (or
// absent) as the first page.
var oneBasedPaths = map[string]bool{
	"/api/studies": true,
}

// pagedEndpoints are the Java endpoints that take pageSize / pageNumber /
// sortBy / direction (their controller methods declare them); every other
// endpoint ignores those params, as Java does.
var pagedEndpoints = []string{
	"GET /api/cancer-types",
	"GET /api/clinical-attributes",
	"GET /api/gene-panels",
	"POST /api/generic-assay-meta/fetch",
	"GET /api/genes",
	"GET /api/genesets",
	"GET /api/legacy/samples",
	"GET /api/legacy/studies",
	"GET /api/legacy/studies/{}/patients/{}/samples",
	"GET /api/legacy/studies/{}/samples",
	"GET /api/molecular-profiles",
	"GET /api/molecular-profiles/{}/mutations",
	"POST /api/molecular-profiles/{}/mutations/fetch",
	"POST /api/mutations/fetch",
	"POST /api/not-ready-yet/mutations/fetch",
	"GET /api/patients",
	"GET /api/sample-lists",
	"GET /api/samples",
	"GET /api/studies",
	"GET /api/studies/{}/clinical-attributes",
	"GET /api/studies/{}/clinical-data",
	"GET /api/studies/{}/clinical-events",
	"GET /api/studies/{}/molecular-profiles",
	"GET /api/studies/{}/patients",
	"GET /api/studies/{}/patients/{}/clinical-data",
	"GET /api/studies/{}/patients/{}/clinical-events",
	"GET /api/studies/{}/patients/{}/resource-data",
	"GET /api/studies/{}/patients/{}/samples",
	"GET /api/studies/{}/resource-data",
	"GET /api/studies/{}/resource-data-all",
	"GET /api/studies/{}/resource-definitions",
	"GET /api/studies/{}/sample-lists",
	"GET /api/studies/{}/samples",
	"GET /api/studies/{}/samples/{}/clinical-data",
	"GET /api/studies/{}/samples/{}/copy-number-segments",
	"GET /api/studies/{}/samples/{}/resource-data",
	"GET /api/studies/{}/significant-copy-number-regions",
	"GET /api/studies/{}/significantly-mutated-genes",
}

// pagedEndpoint reports whether the request is for one of pagedEndpoints
// ({} matches one path segment).
func pagedEndpoint(method, path string) bool {
	segs := strings.Split(strings.TrimSuffix(path, "/"), "/")
	for _, e := range pagedEndpoints {
		m, tmpl, _ := strings.Cut(e, " ")
		if m != method {
			continue
		}
		t := strings.Split(tmpl, "/")
		if len(t) != len(segs) {
			continue
		}
		match := true
		for i := range t {
			if t[i] != "{}" && t[i] != segs[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// pageList implements the pageSize / pageNumber / sortBy / direction query
// params of the Java list endpoints for every handler returning a JSON array:
// the array is stably sorted by the sortBy field (ASC unless direction=DESC)
// and then sliced to the requested page. pageSize / pageNumber are removed
// from the request before the handler runs so handlers return the full list.
// META requests pass through: their total-count covers all items.
func pageList(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sizeParam, numberParam, sortBy := q.Get("pageSize"), q.Get("pageNumber"), q.Get("sortBy")
		if (sizeParam == "" && numberParam == "" && sortBy == "") || selfPagedPaths[r.URL.Path] ||
			!pagedEndpoint(r.Method, r.URL.Path) ||
			strings.EqualFold(q.Get("projection"), "META") {
			next.ServeHTTP(w, r)
			return
		}
		size, sizeErr := strconv.Atoi(strings.TrimSpace(sizeParam))
		number, _ := strconv.Atoi(strings.TrimSpace(numberParam))
		if oneBasedPaths[r.URL.Path] {
			number = max(number-1, 0)
		}
		q.Del("pageSize")
		q.Del("pageNumber")
		r2 := r.Clone(r.Context())
		r2.URL.RawQuery = q.Encode()

		pg := page{sortBy: sortBy, desc: strings.EqualFold(q.Get("direction"), "DESC"), size: -1}
		if sizeErr == nil && size >= 0 {
			pg.start, pg.size = max(number, 0)*size, size
		}
		if sortBy == "" {
			pw := &pageWindow{w: w, header: http.Header{}, status: http.StatusOK, page: pg}
			defer pw.close()
			next.ServeHTTP(pw, r2)
			pw.finish()
			return
		}
		rec := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r2)
		writePage(w, rec.header, rec.status, rec.body.Bytes(), pg)
	})
}

// page is the requested page of a list: sorted by sortBy (if set), from
// start, size items (all when size < 0).
type page struct {
	sortBy      string
	desc        bool
	start, size int
}

// writePage writes the page of a handler's whole response: an array of a
// successful response is sorted and sliced, anything else passes as it is.
func writePage(w http.ResponseWriter, header http.Header, status int, body []byte, pg page) {
	maps.Copy(w.Header(), header)
	var items []json.RawMessage
	if status != http.StatusOK || json.Unmarshal(body, &items) != nil {
		w.WriteHeader(status)
		w.Write(body)
		return
	}
	if pg.sortBy != "" {
		sortItems(items, pg.sortBy, pg.desc)
	}
	if pg.size >= 0 {
		start := min(pg.start, len(items))
		items = items[start:min(start+pg.size, len(items))]
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(it)
	}
	buf.WriteString("]\n")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

// pageWindow receives an unsorted paged handler's response for pageList. A
// successful JSON array is decoded element by element as the handler writes
// it, and only the page's elements are written on: the list is never held.
// Any other response is kept whole for writePage.
type pageWindow struct {
	w         http.ResponseWriter
	header    http.Header
	status    int
	page      page
	raw       bytes.Buffer
	streaming bool
	pw        *io.PipeWriter
	done      chan error
	closed    bool
	err       error
}

func (p *pageWindow) Header() http.Header { return p.header }

func (p *pageWindow) WriteHeader(code int) {
	if p.raw.Len() == 0 && !p.streaming {
		p.status = code
	}
}

func (p *pageWindow) Write(b []byte) (int, error) {
	if p.streaming {
		return p.pw.Write(b)
	}
	p.raw.Write(b)
	if t := bytes.TrimLeft(p.raw.Bytes(), " \t\r\n"); p.status == http.StatusOK && len(t) > 0 && t[0] == '[' {
		p.streaming = true
		maps.Copy(p.w.Header(), p.header)
		p.w.Header().Set("Content-Type", "application/json")
		p.w.Header().Del("Content-Length")
		p.w.WriteHeader(http.StatusOK)
		pr, pw := io.Pipe()
		p.pw, p.done = pw, make(chan error, 1)
		go p.window(pr)
		_, err := pw.Write(p.raw.Bytes())
		p.raw.Reset()
		if err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// window copies the page's elements of the array read from r to the
// response, then drains r.
func (p *pageWindow) window(r *io.PipeReader) {
	var err error
	defer func() {
		io.Copy(io.Discard, r)
		p.done <- err
	}()
	bw := bufio.NewWriterSize(p.w, 64<<10)
	bw.WriteByte('[')
	dec := json.NewDecoder(r)
	if _, err = dec.Token(); err != nil {
		return
	}
	n, written := 0, 0
	for dec.More() {
		if p.page.size >= 0 && written == p.page.size {
			// The page is complete; the rest of the list is discarded unread.
			break
		}
		var item json.RawMessage
		if err = dec.Decode(&item); err != nil {
			return
		}
		if n >= p.page.start {
			if written > 0 {
				bw.WriteByte(',')
			}
			bw.Write(item)
			written++
		}
		n++
	}
	if p.page.size < 0 || written < p.page.size {
		if _, err = dec.Token(); err != nil {
			return
		}
	}
	bw.WriteString("]\n")
	err = bw.Flush()
}

// finish ends the response: the streamed page, or the response kept whole.
// A streamed array that fails to decode aborts the response.
func (p *pageWindow) finish() {
	if !p.streaming {
		writePage(p.w, p.header, p.status, p.raw.Bytes(), p.page)
		return
	}
	if err := p.close(); err != nil {
		log.Printf("paged response aborted: %v", err)
		panic(http.ErrAbortHandler)
	}
}

// close ends the stream and waits for its page to be written; it also
// releases a handler that ended early (an aborted response).
func (p *pageWindow) close() error {
	if p.pw == nil || p.closed {
		return p.err
	}
	p.closed = true
	p.pw.Close()
	p.err = <-p.done
	return p.err
}

// sortItems stably sorts JSON objects by one top-level field: numbers compare
// numerically, everything else as strings; a missing or null field sorts
// first (MySQL NULL ordering).
func sortItems(items []json.RawMessage, field string, desc bool) {
	type keyed struct {
		raw   json.RawMessage
		null  bool
		num   *float64
		str   string
		index int
	}
	ks := make([]keyed, len(items))
	for i, it := range items {
		k := keyed{raw: it, index: i, null: true}
		var obj map[string]json.RawMessage
		if json.Unmarshal(it, &obj) == nil {
			if v, ok := obj[field]; ok && string(v) != "null" {
				k.null = false
				var f float64
				var s string
				switch {
				case json.Unmarshal(v, &f) == nil:
					k.num = &f
				case json.Unmarshal(v, &s) == nil:
					k.str = s
				default:
					k.str = string(v)
				}
			}
		}
		ks[i] = k
	}
	less := func(a, b keyed) bool {
		if a.null || b.null {
			return a.null && !b.null
		}
		if a.num != nil && b.num != nil {
			return *a.num < *b.num
		}
		return a.str < b.str
	}
	sort.SliceStable(ks, func(i, j int) bool {
		if desc {
			return less(ks[j], ks[i])
		}
		return less(ks[i], ks[j])
	})
	for i, k := range ks {
		items[i] = k.raw
	}
}
