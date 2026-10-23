// Package core is the request machinery the API's handler packages share:
// the State they serve from, the Java error and parameter contract, paging,
// JSON streaming, the scoped query layer over ClickHouse and its cache.
package core

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/cbioportal/cbioportal-go/internal/api/sessionstore"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// State is what the handlers serve from: the ClickHouse client, the portal
// properties and the shared caches.
type State struct {
	// Opts are the server settings cbioportal-api starts the API with.
	Opts Options
	CH   *ch.Client
	// BulkCH is CH with the per-query limits of the bulk endpoints (whole
	// profiles of data, enrichments, the study export): their queries spill
	// large sorts to disk and stop at a memory and a time limit.
	BulkCH *ch.Client
	// Sessions is the session store: ConfigureSessions sets it, and a nil
	// one is the built-in store (Opts.SessionStoreFile) on first use.
	Sessions     sessionstore.Store
	sessionsOnce sync.Once
	meta         metaCache
	// The portal UI shares the API's origin as in a cBioPortal deployment:
	// frontendDir is a built frontend (Opts.FrontendDir), whose index.html
	// the portal pages serve and whose files frontendFiles serves under
	// /reactapp/ and /images/; frontendProxy is a frontend dev server
	// (Opts.FrontendURL) that gets the pages and every unknown path instead.
	frontendDir   string
	frontendFiles *staticFiles
	// staticFiles serves the portal's own files (Opts.StaticDir) at their
	// path, before the built-in resources and the frontend build.
	staticFiles   *staticFiles
	frontendProxy http.Handler
	// PageConfig is the frontend configuration (the /config_service body) a
	// portal page embeds as window.rawServerConfig; nil embeds none.
	PageConfig func(*http.Request) map[string]any
	// mux is the route table, consulted by the catch-all to tell an unknown
	// path from a known one requested with an unsupported method.
	mux *http.ServeMux
	// Props are the portal properties (skin.*, priority_studies, …) the
	// frontend configuration is built from. PropsReplaceDefaults is set when
	// they come from a properties file, which replaces the stock defaults as
	// a mounted application.properties does for the Java portal.
	Props                map[string]string
	PropsReplaceDefaults bool
	// AuthMethod is the login's authenticate= method, "" without a login.
	AuthMethod string
	// llmsTxt caches the llms_txt.location file /llms.txt serves.
	llmsTxt llmsTxtCache
}

// SetMux records the route table the catch-all, the trailing-slash matching
// and the access log consult.
func (s *State) SetMux(mux *http.ServeMux) { s.mux = mux }

// Options are the server settings of cbioportal-api that the API itself
// uses. The zero value serves the API alone, with in-memory sessions.
type Options struct {
	// FrontendDir is a frontend build served for every non-/api/ path;
	// FrontendURL, used when FrontendDir is empty, is a frontend dev server
	// those paths are proxied to instead.
	FrontendDir string
	FrontendURL string
	// StaticDir holds the portal's own files (a logo, custom tab scripts),
	// served at their path before the built-in ones.
	StaticDir string
	// OncoKBURL is the OncoKB API behind /proxy/; empty means the public one.
	OncoKBURL string
	// SiteBanner is a notice bar at the top of every portal page, linking
	// to SiteBannerLink when set.
	SiteBanner     string
	SiteBannerLink string
	// SessionStoreFile persists the built-in session store; empty keeps
	// sessions in memory.
	SessionStoreFile string
	// AccessLog writes one JSON line per request to stdout.
	AccessLog bool
	// BearerAuth requires HS256 bearer tokens (signed with $AUTH_JWT_SECRET);
	// BearerDefaultUser is the user of requests without one, for a
	// single-user development login.
	BearerAuth        bool
	BearerDefaultUser string
}

// NewState returns the API state over ClickHouse client c with the server
// settings o; ConfigureAuth and ConfigureSessions apply the portal
// properties.
func NewState(c *ch.Client, o Options) *State {
	s := &State{Opts: o, CH: c, BulkCH: c.WithSettings(DefaultBulkLimits.Settings())}
	if o.StaticDir != "" {
		s.staticFiles = newStaticFiles(o.StaticDir)
	}
	if o.FrontendDir != "" {
		s.frontendDir, s.frontendFiles = o.FrontendDir, newStaticFiles(o.FrontendDir)
	} else if u, err := url.Parse(o.FrontendURL); err == nil && u.Host != "" {
		s.frontendProxy = httputil.NewSingleHostReverseProxy(u)
	}
	return s
}

// GunzipBody transparently decompresses gzip-encoded request bodies. The
// frontend sends large POST bodies (e.g. patients/fetch with hundreds of
// unique keys) gzip-compressed when enable_request_body_gzip_compression is on;
// Spring decompresses these automatically, so we must too or the JSON decode
// silently yields an empty filter.
func GunzipBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
			zr, err := gzip.NewReader(r.Body)
			if err == nil {
				r.Body = gzipBody{zr, r.Body}
				r.Header.Del("Content-Encoding")
				r.ContentLength = -1
			}
		}
		next.ServeHTTP(w, r)
	})
}

// gzipBody couples the gzip reader to the underlying body so both close.
type gzipBody struct {
	*gzip.Reader
	orig io.ReadCloser
}

func (g gzipBody) Close() error {
	g.Reader.Close()
	return g.orig.Close()
}

// CORS adds permissive CORS and answers preflight requests.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "*")
		h.Set("Access-Control-Expose-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ----

// WriteJSON answers v as JSON.
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// jsonArray writes a JSON array element by element, byte for byte as
// WriteJSON writes the whole slice. Nothing is sent before the first element,
// so an error until then still answers as Fail does.
type jsonArray struct {
	w   http.ResponseWriter
	r   *http.Request
	bw  *bufio.Writer
	buf bytes.Buffer
	enc *json.Encoder
}

// NewJSONArray returns a jsonArray answering r through w.
func NewJSONArray(w http.ResponseWriter, r *http.Request) *jsonArray {
	a := &jsonArray{w: w, r: r}
	a.enc = json.NewEncoder(&a.buf)
	return a
}

// Add writes v as the array's next element.
func (a *jsonArray) Add(v any) error {
	a.buf.Reset()
	if err := a.enc.Encode(v); err != nil {
		return err
	}
	if a.bw == nil {
		a.w.Header().Set("Content-Type", "application/json")
		a.bw = bufio.NewWriterSize(a.w, 64<<10)
		a.bw.WriteByte('[')
	} else {
		a.bw.WriteByte(',')
	}
	_, err := a.bw.Write(a.buf.Bytes()[:a.buf.Len()-1]) // without Encode's newline
	return err
}

// Finish ends the array, or reports err: as Fail before the first element,
// by aborting the response after it.
func (a *jsonArray) Finish(err error) {
	switch {
	case err != nil && a.bw == nil:
		Fail(a.w, a.r, err)
	case err != nil:
		LogServerError(a.r, "streamed response aborted", err)
		panic(http.ErrAbortHandler)
	case a.bw == nil:
		WriteJSON(a.w, []struct{}{})
	default:
		a.bw.WriteString("]\n")
		a.bw.Flush()
	}
}

// Fail answers an unexpected internal error as Java does, with Spring
// Boot's generic 500: the error itself (SQL, database names, driver text)
// goes to the server log only.
//
// A query stopped at its memory or time limit (BulkLimits) is answered
// 400 with a message that says how to ask for less.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	if limit := ch.LimitExceeded(err); limit != "" {
		slog.Warn("query limit", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
		WriteError(w, http.StatusBadRequest, MsgQueryLimit(limit))
		return
	}
	LogServerError(r, "internal server error", err)
	SpringError(w, r, http.StatusInternalServerError)
}

// LogServerError logs an internal error of the request r.
func LogServerError(r *http.Request, msg string, err error) {
	slog.Error(msg, slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
}

// SQLLit and InList write values into SQL text as escaped string literals,
// for values chosen by code (database and table names, constant lists, column
// headers of loaded files). Request values go to ClickHouse as query
// parameters (SQLArgs, ch.QueryP, CachedQueryP).
func SQLLit(s string) string { return ch.SQLLit(s) }

// InList writes values as a comma-separated list of SQL string literals.
func InList(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = "'" + SQLLit(v) + "'"
	}
	return strings.Join(q, ", ")
}

// MetaProjection implements projection=META for every list endpoint: the
// handler's JSON array is replaced by a total-count header with its length.
// Handlers that set total-count themselves are passed through.
func MetaProjection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.URL.Query().Get("projection"), "META") {
			next.ServeHTTP(w, r)
			return
		}
		rec := &metaCounter{header: http.Header{}, status: http.StatusOK}
		defer rec.close()
		next.ServeHTTP(rec, r)
		n, samples, ok := rec.count()
		if rec.err != nil {
			Fail(w, r, rec.err)
			return
		}
		maps.Copy(w.Header(), rec.header)
		if !ok {
			w.WriteHeader(rec.status)
			w.Write(rec.raw.Bytes())
			return
		}
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")
		w.Header().Set("total-count", fmt.Sprint(n))
		// Mutation META also reports the distinct samples (MutationMeta);
		// no other endpoint sets sample-count.
		if strings.Contains(r.URL.Path, "/mutations") {
			w.Header().Set("sample-count", fmt.Sprint(samples))
		}
		w.WriteHeader(http.StatusOK)
	})
}

// WriteMetaCount answers projection=META with the record count sql reads (a
// row with n) as the total-count header, as MetaProjection would count the
// records. p holds sql's placeholder values.
func WriteMetaCount(w http.ResponseWriter, r *http.Request, c *ch.Client, sql string, p ch.Params) {
	rows, err := ch.QueryP[struct {
		N uint64 `json:"n"`
	}](c, sql, p)
	if err != nil {
		Fail(w, r, err)
		return
	}
	var n uint64
	if len(rows) > 0 {
		n = rows[0].N
	}
	w.Header().Set("total-count", fmt.Sprint(n))
	w.WriteHeader(http.StatusOK)
}

// metaCounter receives a handler's response for MetaProjection. A successful
// JSON array of objects without a total-count header is counted as it is
// written (its elements and their distinct uniqueSampleKeys) and not kept, so
// counting a large list costs no more memory than one element; any other
// response is kept in raw to be passed on as it is.
type metaCounter struct {
	header   http.Header
	status   int
	raw      bytes.Buffer
	counting bool
	err      error // counting failed
	pw       *io.PipeWriter
	done     chan metaCount
}

type metaCount struct {
	n, samples int
	err        error
}

func (m *metaCounter) Header() http.Header { return m.header }

func (m *metaCounter) WriteHeader(code int) {
	if m.raw.Len() == 0 && !m.counting {
		m.status = code
	}
}

func (m *metaCounter) Write(p []byte) (int, error) {
	if m.counting {
		return m.pw.Write(p)
	}
	m.raw.Write(p)
	if m.status == http.StatusOK && m.header.Get("total-count") == "" && objectArrayStart(m.raw.Bytes()) {
		m.counting = true
		pr, pw := io.Pipe()
		m.pw, m.done = pw, make(chan metaCount, 1)
		go countArray(pr, m.done)
		_, err := pw.Write(m.raw.Bytes())
		m.raw.Reset()
		if err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// count reports the array's length and distinct sample keys; ok is false
// when raw holds the response to pass on instead: a response that is no
// array, or a counted array that failed to decode (no longer held, it
// answers 500).
func (m *metaCounter) count() (n, samples int, ok bool) {
	if !m.counting {
		if m.status != http.StatusOK || m.header.Get("total-count") != "" {
			return 0, 0, false
		}
		var items []struct {
			UniqueSampleKey string `json:"uniqueSampleKey"`
		}
		if json.Unmarshal(m.raw.Bytes(), &items) != nil {
			return 0, 0, false
		}
		samples := map[string]bool{}
		for _, it := range items {
			if it.UniqueSampleKey != "" {
				samples[it.UniqueSampleKey] = true
			}
		}
		return len(items), len(samples), true
	}
	m.pw.Close()
	c := <-m.done
	if c.err != nil {
		m.err = fmt.Errorf("projection=META: counting the response: %w", c.err)
		return 0, 0, false
	}
	return c.n, c.samples, true
}

// close ends the counting of a handler that stopped writing early (an
// aborted response).
func (m *metaCounter) close() {
	if m.pw != nil {
		m.pw.Close()
	}
}

// objectArrayStart reports whether b begins a JSON array whose first token
// after '[' is an object or the closing bracket.
func objectArrayStart(b []byte) bool {
	b = bytes.TrimLeft(b, " \t\r\n")
	if len(b) == 0 || b[0] != '[' {
		return false
	}
	b = bytes.TrimLeft(b[1:], " \t\r\n")
	return len(b) > 0 && (b[0] == '{' || b[0] == ']')
}

// countArray decodes a JSON array of objects from r element by element,
// then drains r.
func countArray(r *io.PipeReader, done chan<- metaCount) {
	var c metaCount
	defer func() {
		io.Copy(io.Discard, r)
		done <- c
	}()
	dec := json.NewDecoder(r)
	if _, c.err = dec.Token(); c.err != nil {
		return
	}
	samples := map[string]bool{}
	for dec.More() {
		var it struct {
			UniqueSampleKey string `json:"uniqueSampleKey"`
		}
		if c.err = dec.Decode(&it); c.err != nil {
			return
		}
		c.n++
		if it.UniqueSampleKey != "" {
			samples[it.UniqueSampleKey] = true
		}
	}
	if _, c.err = dec.Token(); c.err != nil {
		return
	}
	c.samples = len(samples)
}

// NewBufferedResponse returns an empty bufferedResponse with status 200.
func NewBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: http.Header{}, Status: http.StatusOK}
}

type bufferedResponse struct {
	header http.Header
	Body   bytes.Buffer
	Status int
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.Body.Write(p) }
func (b *bufferedResponse) WriteHeader(code int)        { b.Status = code }

// indexPage serves the built frontend's index.html as the portal page: with
// window.frontendConfig pointing the app at this origin and the frontend
// configuration as window.rawServerConfig, as the Java portal's index
// template writes them.
func (s *State) indexPage(w http.ResponseWriter, r *http.Request) {
	page, err := os.ReadFile(filepath.Join(s.frontendDir, "index.html"))
	if err != nil {
		http.Error(w, "frontend index.html missing", http.StatusInternalServerError)
		return
	}
	var raw []byte
	if s.PageConfig != nil {
		raw, _ = json.Marshal(s.PageConfig(r))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(withSiteBanner(withFrontendConfig(page, r.Host, raw), s.Opts.SiteBanner, s.Opts.SiteBannerLink))
}

// withFrontendConfig sets window.frontendConfig in the frontend's index page
// as the portal's index template does, pointing the app at this origin, and
// window.rawServerConfig to rawConfig (JSON) when given, so the app starts
// without requesting /config_service.
func withFrontendConfig(page []byte, host string, rawConfig []byte) []byte {
	cfg, _ := json.Marshal(map[string]string{
		"configurationServiceUrl": "//" + host + "/config_service",
		"apiRoot":                 "//" + host + "/",
		"baseUrl":                 host,
		"basePath":                "",
		"frontendUrl":             "//" + host + "/",
	})
	js := "window.frontendConfig = Object.assign(window.frontendConfig || {}, " + string(cfg) + ");"
	if len(rawConfig) > 0 {
		// json.Marshal escapes <, > and &, so the configuration cannot end the script element.
		js += "window.rawServerConfig = " + string(rawConfig) + ";"
	}
	script := []byte("<script>" + js + "</script>" + devModeScript + fontAwesome4)
	if i := bytes.Index(page, []byte("</head>")); i >= 0 {
		page = append(append(append([]byte{}, page[:i]...), script...), page[i:]...)
	} else {
		page = append(script, page...)
	}
	// An element after #reactRoot keeps it from being the body's last child:
	// otherwise every node the frontend appends to the body to measure text
	// flips :last-child matching and restyles the whole app.
	if i := bytes.Index(page, []byte("</body>")); i >= 0 {
		page = append(append(append([]byte{}, page[:i]...), "<script></script>"...), page[i:]...)
	}
	return withNetlifyBundle(page)
}

var (
	bundleScript = regexp.MustCompile(`<script defer src="/reactapp/([^"<>]+)"></script>`)
	bundleStyles = regexp.MustCompile(`<link href="/reactapp/([^"<>]+\.css)" rel="stylesheet">`)
)

// netlifyBase is the script expression for where the frontend bundle is
// loaded from: a cbioportal-frontend deploy preview when localStorage.netlify
// names one (as on the Java portal page), else this origin. localdev and
// localdist keep this origin's bundle, which the e2e runs serve.
const netlifyBase = `(localStorage.netlify ? "//" + localStorage.netlify + ".netlify.app/" : "/")`

// withNetlifyBundle loads the build's scripts and stylesheet from netlifyBase.
func withNetlifyBundle(page []byte) []byte {
	page = bundleScript.ReplaceAll(page, []byte(`<script>document.write('<script defer src="' + `+netlifyBase+` + 'reactapp/$1"></scr' + 'ipt>');</script>`))
	return bundleStyles.ReplaceAll(page, []byte(`<script>document.write('<link href="' + `+netlifyBase+` + 'reactapp/$1" rel="stylesheet">');</script>`))
}

// fontAwesome4 is the Font Awesome 4.7 stylesheet the Java portal's index
// template links last in the head. The frontend brings Font Awesome 6, and
// this later link gives its fa-* icons the 4.7 glyphs www.cbioportal.org
// shows.
const fontAwesome4 = `<link href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/4.7.0/css/font-awesome.css" rel="stylesheet" />`

// withSiteBanner puts a notice bar (e.g. "experimental preview") at the top
// of the page body, above the app, when text is set; link, if set, is
// appended as a link. It is part of the document flow, so it never covers the
// portal header.
func withSiteBanner(page []byte, text, link string) []byte {
	if text == "" {
		return page
	}
	i := bytes.Index(page, []byte("<body"))
	if i < 0 {
		return page
	}
	j := bytes.IndexByte(page[i:], '>')
	if j < 0 {
		return page
	}
	at := i + j + 1
	banner := `<div id="site-banner" style="background:#fcf8e3;border-bottom:1px solid #faebcc;color:#8a6d3b;` +
		`padding:6px 12px;text-align:center;font-size:13px;line-height:1.4">` + html.EscapeString(text)
	if link != "" {
		banner += ` <a href="` + html.EscapeString(link) + `" style="color:#66512c;text-decoration:underline">` +
			html.EscapeString(link) + `</a>`
	}
	banner += `</div>`
	return append(append(append([]byte{}, page[:at]...), banner...), page[at:]...)
}

// devModeScript mirrors the Java portal page (index.html + load-frontend.js):
// ?localdev=true / ?localdist=true are remembered in localStorage and, as in
// dev mode there, a dismissible "dev mode" banner is added to the body. The
// bundle comes from this origin, or from the netlify deploy preview that
// localStorage.netlify names (withNetlifyBundle).
const devModeScript = `<script>
if (/localdev=true/.test(window.location.href)) { localStorage.setItem("localdev", "true"); }
if (/localdist=true/.test(window.location.href)) { localStorage.setItem("localdist", "true"); }
window.localdev = localStorage.localdev === "true";
window.localdist = localStorage.localdist === "true";
window.netlify = localStorage.netlify;
if (window.netlify) {
  window.frontendConfig.frontendUrl = "//" + window.netlify + ".netlify.app/";
}
if (window.localdev || window.localdist || window.netlify) {
  localStorage.setItem("e2etest", "true");
  window.clearDevState = function () {
    localStorage.removeItem("localdev");
    localStorage.removeItem("localdist");
    localStorage.removeItem("netlify");
    window.location.reload();
  };
  document.addEventListener("DOMContentLoaded", function () {
    var d = document.createElement("div");
    d.setAttribute("class", "local-dev-banner");
    d.setAttribute("style", "position: fixed; z-index:100; top: 0; left: 0; width: 100%");
    document.body.appendChild(d);
    d.innerHTML = '<div style=""><div class="alert alert-warning">' +
      '<button type="button" class="close" data-dismiss="alert">&times;</button>' +
      'cbioportal-frontend dev mode, using ' + window.frontendConfig.frontendUrl +
      '&nbsp;<a href="" onclick="javascript:clearDevState();window.location.reload()">clear</a></div></div>';
    d.onclick = function () { document.body.removeChild(this); };
  });
}
</script>`

// BaseURL is the portal's origin as the browser sees it: behind a
// TLS-terminating proxy or ingress, from its X-Forwarded-Proto and
// X-Forwarded-Host (or the Host it passes on).
func BaseURL(r *http.Request) string {
	first := func(h string) string {
		v, _, _ := strings.Cut(r.Header.Get(h), ",")
		return strings.TrimSpace(v)
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(first("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if h := first("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}
