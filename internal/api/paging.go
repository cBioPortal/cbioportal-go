package api

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"slices"
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
// absent) as the first page. Their /api/legacy/ aliases are served by the
// legacy controllers, which number pages from 0.
var oneBasedPaths = map[string]bool{
	"/api/studies": true,
}

// pagedEndpoints are the Java endpoints that take pageSize / pageNumber /
// sortBy / direction (their controller methods declare them); every other
// endpoint ignores those params, as Java does. The /api/legacy/ and
// /api/not-ready-yet/ aliases reach pageList as the /api/ path that serves
// them (extraEndpoints).
var pagedEndpoints = []string{
	"GET /api/cancer-types",
	"GET /api/clinical-attributes",
	"GET /api/gene-panels",
	"POST /api/generic-assay-meta/fetch",
	"GET /api/genes",
	"GET /api/genesets",
	"GET /api/molecular-profiles",
	"GET /api/molecular-profiles/{}/mutations",
	"POST /api/molecular-profiles/{}/mutations/fetch",
	"POST /api/mutations/fetch",
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

// unsortedEndpoints are the paged endpoints whose Java controllers declare
// no sortBy: it is ignored.
var unsortedEndpoints = map[string]bool{
	"POST /api/generic-assay-meta/fetch": true,
	"GET /api/genesets":                  true,
}

// sortIgnored reports whether the request's sortBy is ignored: only on the
// endpoints that declare none. Every other paged endpoint sorts by it, also
// for a keyword search and for projection=ID.
func sortIgnored(endpoint string) bool {
	return unsortedEndpoints[endpoint]
}

// pagedEndpoint reports whether the request is for one of pagedEndpoints
// ({} matches one path segment).
func pagedEndpoint(method, path string) bool {
	return pagedEndpointOf(method, path) != ""
}

// pagedEndpointOf returns the pagedEndpoints entry the request is for, or "".
func pagedEndpointOf(method, path string) string {
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
			return e
		}
	}
	return ""
}

// pageList implements the pageSize / pageNumber / sortBy / direction query
// params of the Java list endpoints for every handler returning a JSON array:
// the array is sorted by the sortBy field (ASC unless direction=DESC; see
// page.before) and then sliced to the requested page. pageSize / pageNumber are
// removed from the request before the handler runs so handlers return the
// full list, which is never held: an unsorted page is cut from the array as
// it is written, and a sorted one is read by the handler in SQL
// (sqlPageOrder) or kept as the best items seen (topPage). META requests
// pass through: their total-count covers all items.
func pageList(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		endpoint := pagedEndpointOf(r.Method, r.URL.Path)
		sortBy := q.Get("sortBy")
		if sortBy != "" && sortIgnored(endpoint) {
			sortBy = ""
			q.Del("sortBy")
		}
		sizeParam, numberParam := q.Get("pageSize"), q.Get("pageNumber")
		if (sizeParam == "" && numberParam == "" && sortBy == "") || selfPagedPaths[r.URL.Path] ||
			endpoint == "" || strings.EqualFold(q.Get("projection"), "META") ||
			(endpoint == "GET /api/genes" && q.Has("keyword")) {
			next.ServeHTTP(w, r)
			return
		}
		size, sizeErr := strconv.Atoi(strings.TrimSpace(sizeParam))
		number, _ := strconv.Atoi(strings.TrimSpace(numberParam))
		if oneBasedPaths[r.URL.Path] && aliasOf(r) == "" {
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
		sp := &sqlPage{page: pg}
		cw := &claimWriter{w: w, sp: sp, header: http.Header{}}
		next.ServeHTTP(cw, r2.WithContext(context.WithValue(r2.Context(), sqlPageKey{}, sp)))
		cw.finish()
	})
}

// sqlPage is the sorted page pageList asks a handler for; claimed when the
// handler reads it in SQL.
type sqlPage struct {
	page
	claimed bool
}

type sqlPageKey struct{}

// sqlPageOrder is the ORDER BY clause of a list query. When pageList asks
// for a sorted page by one of the sortable fields (sortBy value -> SQL
// expression; an empty expression leaves the order as it is), the handler
// claims it: rows are sorted by that expression, NULLs last in either
// direction as ClickHouse orders Java's ORDER BY "<sortBy>", ties in the
// handler's own order def, and only the page is read (LIMIT / OFFSET).
// Otherwise the clause orders by def, and pageList sorts what the handler
// writes.
func sqlPageOrder(r *http.Request, sortable map[string]string, def string) string {
	sp, expr := claimSortedPage(r, sortable)
	if sp == nil {
		return " ORDER BY " + def
	}
	return sp.orderLimit(expr, def)
}

// sqlStablePageQuery is the query base (a SELECT without ORDER BY) ordered
// as sqlPageOrder orders it, for an order def that leaves ties: a sorted
// page is cut from base ordered by def and numbered as read, so ties keep
// the order the unsorted list gives them. sortable's expressions refer to
// base's columns.
func sqlStablePageQuery(r *http.Request, sortable map[string]string, base, def string) string {
	sp, expr := claimSortedPage(r, sortable)
	switch {
	case sp == nil:
		return base + " ORDER BY " + def
	case expr == "":
		return base + sp.orderLimit("", def)
	}
	// The numbering needs the inner sort, which ClickHouse would otherwise
	// drop as redundant under the outer one.
	return "SELECT * EXCEPT (pageRank) FROM (SELECT *, rowNumberInAllBlocks() AS pageRank FROM (" +
		base + " ORDER BY " + def + "))" + sp.orderLimit(expr, "pageRank") +
		" SETTINGS query_plan_remove_redundant_sorting = 0"
}

// claimSortedPage returns the sorted page pageList asks the request for and
// the SQL expression of its sortBy, claiming it, or nil when there is none
// or sortBy is not among sortable.
func claimSortedPage(r *http.Request, sortable map[string]string) (*sqlPage, string) {
	sp, _ := r.Context().Value(sqlPageKey{}).(*sqlPage)
	if sp == nil {
		return nil, ""
	}
	expr, ok := sortable[sp.sortBy]
	if !ok {
		return nil, ""
	}
	sp.claimed = true
	return sp, expr
}

// keyedPage is a sorted page a handler cuts itself, for rows it writes
// without the sortBy field (the ID projection drops it): each row is added
// with an object that has the field, and the rows are sorted as pageList
// sorts them.
type keyedPage struct {
	pg    page
	items []sortItem
}

// claimKeyedPage claims the sorted page pageList asks the request for, when
// the handler has not read it in SQL, or returns nil.
func claimKeyedPage(r *http.Request) *keyedPage {
	sp, _ := r.Context().Value(sqlPageKey{}).(*sqlPage)
	if sp == nil || sp.claimed {
		return nil
	}
	sp.claimed = true
	return &keyedPage{pg: sp.page}
}

// add adds the row out, sorted by key's sortBy field.
func (k *keyedPage) add(key, out any) error {
	kb, err := json.Marshal(key)
	if err != nil {
		return err
	}
	it := newSortItem(kb, k.pg.sortBy, len(k.items))
	if it.raw, err = json.Marshal(out); err != nil {
		return err
	}
	k.items = append(k.items, it)
	return nil
}

// rows returns the rows of the page in order.
func (k *keyedPage) rows() []json.RawMessage {
	slices.SortFunc(k.items, k.pg.compare)
	items := k.items[min(k.pg.start, len(k.items)):]
	if k.pg.size >= 0 {
		items = items[:min(k.pg.size, len(items))]
	}
	out := make([]json.RawMessage, len(items))
	for i, it := range items {
		out[i] = it.raw
	}
	return out
}

// orderLimit is the ORDER BY and LIMIT clause of the page: by expr (unless
// empty) then tie.
func (sp *sqlPage) orderLimit(expr, tie string) string {
	clause := " ORDER BY " + tie
	if expr != "" {
		dir := "ASC"
		if sp.desc {
			dir = "DESC"
		}
		clause = fmt.Sprintf(" ORDER BY %s %s NULLS LAST, %s", expr, dir, tie)
	}
	if sp.size >= 0 {
		clause += fmt.Sprintf(" LIMIT %d OFFSET %d", sp.size, sp.start)
	}
	return clause
}

// claimWriter passes a handler's response on as it is when the handler
// claimed its sorted page (sqlPageOrder), else to a topPage that sorts it.
// The choice is made at the first write.
type claimWriter struct {
	w      http.ResponseWriter
	sp     *sqlPage
	header http.Header
	out    http.ResponseWriter
	top    *topPage
}

func (c *claimWriter) decide() {
	if c.out != nil {
		return
	}
	if c.sp.claimed {
		c.out = c.w
	} else {
		c.top = newTopPage(c.w, c.sp.page)
		c.out = c.top
	}
	maps.Copy(c.out.Header(), c.header)
}

func (c *claimWriter) Header() http.Header {
	if c.out != nil {
		return c.out.Header()
	}
	return c.header
}

func (c *claimWriter) WriteHeader(code int) {
	c.decide()
	c.out.WriteHeader(code)
}

func (c *claimWriter) Write(p []byte) (int, error) {
	c.decide()
	return c.out.Write(p)
}

func (c *claimWriter) finish() {
	c.decide()
	if c.top != nil {
		c.top.finish()
	}
}

// topPage sorts a handler's array as it is written, keeping only the items
// of the page and those before it (all when the page is unbounded). Any
// other response passes as it is.
type topPage struct {
	*elementWriter
	w     http.ResponseWriter
	pg    page
	items []sortItem // a heap of the kept items, worst first, while bounded
	n     int
}

func newTopPage(w http.ResponseWriter, pg page) *topPage {
	t := &topPage{w: w, pg: pg}
	t.elementWriter = newElementWriter(func(http.Header) bool { return true }, t.add)
	return t
}

// worse orders the heap: the item a page would drop first comes first.
func (t *topPage) worse(i, j int) bool { return t.pg.before(t.items[j], t.items[i]) }

func (t *topPage) add(el []byte) error {
	it := newSortItem(el, t.pg.sortBy, t.n)
	t.n++
	if t.pg.size < 0 {
		it.raw = bytes.Clone(el)
		t.items = append(t.items, it)
		return nil
	}
	keep := t.pg.start + t.pg.size
	if keep == 0 {
		return nil
	}
	if len(t.items) == keep {
		// Later items lose ties, so only a strictly better one is kept.
		if !t.pg.before(it, t.items[0]) {
			return nil
		}
		it.raw = bytes.Clone(el)
		t.items[0] = it
		t.down(0)
		return nil
	}
	it.raw = bytes.Clone(el)
	t.items = append(t.items, it)
	t.up(len(t.items) - 1)
	return nil
}

func (t *topPage) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !t.worse(i, p) {
			return
		}
		t.items[i], t.items[p] = t.items[p], t.items[i]
		i = p
	}
}

func (t *topPage) down(i int) {
	for {
		l, m := 2*i+1, i
		if l < len(t.items) && t.worse(l, m) {
			m = l
		}
		if r := l + 1; r < len(t.items) && t.worse(r, m) {
			m = r
		}
		if m == i {
			return
		}
		t.items[i], t.items[m] = t.items[m], t.items[i]
		i = m
	}
}

// finish writes the page of a streamed array, or the response as it is.
func (t *topPage) finish() {
	if !t.elementWriter.finish() {
		writePage(t.w, t.header, t.status, t.raw.Bytes(), t.pg)
		return
	}
	slices.SortFunc(t.items, t.pg.compare)
	items := t.items[min(t.pg.start, len(t.items)):]
	if t.pg.size >= 0 {
		items = items[:min(t.pg.size, len(items))]
	}
	maps.Copy(t.w.Header(), t.header)
	t.w.Header().Set("Content-Type", "application/json")
	t.w.Header().Del("Content-Length")
	t.w.WriteHeader(http.StatusOK)
	bw := bufio.NewWriterSize(t.w, 64<<10)
	bw.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			bw.WriteByte(',')
		}
		bw.Write(it.raw)
	}
	bw.WriteString("]\n")
	bw.Flush()
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

// sortItem is a list item with its key for sortBy: numbers compare
// numerically, everything else as strings (the decoded string, or the raw
// JSON of other values); a missing or null field, or an item that is no
// object, has no key.
type sortItem struct {
	raw   []byte
	null  bool
	num   *float64
	str   string
	index int // position in the list
}

func newSortItem(raw []byte, field string, index int) sortItem {
	it := sortItem{raw: raw, index: index, null: true}
	fields, ok := objectFields(raw, nil)
	if !ok {
		return it
	}
	var v []byte
	for _, f := range fields {
		if string(f.key) == field {
			v = f.val // the last of duplicate keys, as json.Unmarshal reads it
		}
	}
	if v == nil || string(v) == "null" {
		return it
	}
	it.null = false
	var f float64
	var s string
	switch {
	case json.Unmarshal(v, &f) == nil:
		it.num = &f
	case json.Unmarshal(v, &s) == nil:
		it.str = s
	default:
		it.str = string(v)
	}
	return it
}

// before reports whether a precedes b in the sorted list: keyed items in
// the page's direction, then the items without a key (ClickHouse's NULLS
// LAST, either direction), ties in list order.
func (pg page) before(a, b sortItem) bool {
	if a.null != b.null {
		return b.null
	}
	if !a.null {
		c := 0
		if a.num != nil && b.num != nil {
			c = cmp.Compare(*a.num, *b.num)
		} else {
			c = strings.Compare(a.str, b.str)
		}
		if pg.desc {
			c = -c
		}
		if c != 0 {
			return c < 0
		}
	}
	return a.index < b.index
}

// compare is before as a comparison function.
func (pg page) compare(a, b sortItem) int {
	switch {
	case a.index == b.index:
		return 0
	case pg.before(a, b):
		return -1
	}
	return 1
}

// sortItems sorts JSON objects by one top-level field as page.before orders
// them.
func sortItems(items []json.RawMessage, field string, desc bool) {
	pg := page{sortBy: field, desc: desc}
	ks := make([]sortItem, len(items))
	for i, it := range items {
		ks[i] = newSortItem(it, field, i)
	}
	slices.SortFunc(ks, pg.compare)
	for i, k := range ks {
		items[i] = k.raw
	}
}
