// Package api serves the cBioPortal REST contract from the ClickHouse transform
// views, with JSON field shapes matching the Java backend so the existing
// frontend works unchanged.
package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

type State struct {
	CH           *ch.Client
	sessions     *sessionStore
	sessionsOnce sync.Once
	meta         metaCache
	// frontend serves every non-API path — a built frontend (FRONTEND_DIR) or a
	// proxied dev server (FRONTEND_URL) — so the portal UI and the API share one
	// origin as in a cBioPortal deployment.
	frontend http.Handler
}

// NewState loads the session store (SESSION_STORE_FILE, if set) at startup.
func NewState(c *ch.Client) *State {
	s := &State{CH: c, sessions: newSessionStoreFromEnv()}
	if dir := os.Getenv("FRONTEND_DIR"); dir != "" {
		s.frontend = spaHandler(dir)
	} else if u, err := url.Parse(os.Getenv("FRONTEND_URL")); err == nil && u.Host != "" {
		s.frontend = httputil.NewSingleHostReverseProxy(u)
	}
	return s
}

// NewRouter builds the API handler with CORS, optional bearer-token auth
// (disabled by default), and a body-draining catch-all.
func NewRouter(s *State) http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return cors(gzipResponse(gunzipBody(auth(loadAuthConfig(), s.extraEndpoints(s.javaContract(pageList(metaProjection(bodyErrors(mux)))))))))
}

// gunzipBody transparently decompresses gzip-encoded request bodies. The
// frontend sends large POST bodies (e.g. patients/fetch with hundreds of
// unique keys) gzip-compressed when enable_request_body_gzip_compression is on;
// Spring decompresses these automatically, so we must too or the JSON decode
// silently yields an empty filter.
func gunzipBody(next http.Handler) http.Handler {
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

// cors adds permissive CORS and answers preflight requests.
func cors(next http.Handler) http.Handler {
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// emptyArray drains the body and returns [] — for not-yet-implemented endpoints.
func emptyArray(w http.ResponseWriter, r *http.Request) {
	readBody(r, nil)
	writeJSON(w, []any{})
}

func fail(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func sqlLit(s string) string { return ch.SQLLit(s) }

func inList(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = "'" + sqlLit(v) + "'"
	}
	return strings.Join(q, ", ")
}

func tupleIn(pairs [][2]string) string {
	q := make([]string, len(pairs))
	for i, p := range pairs {
		q[i] = fmt.Sprintf("('%s', '%s')", sqlLit(p[0]), sqlLit(p[1]))
	}
	return strings.Join(q, ", ")
}

// metaProjection implements projection=META for every list endpoint: the
// handler's JSON array is replaced by a total-count header with its length.
// Handlers that set total-count themselves are passed through.
func metaProjection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.URL.Query().Get("projection"), "META") {
			next.ServeHTTP(w, r)
			return
		}
		rec := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		maps.Copy(w.Header(), rec.header)
		if rec.header.Get("total-count") == "" && rec.status == http.StatusOK {
			var items []struct {
				UniqueSampleKey string `json:"uniqueSampleKey"`
			}
			if json.Unmarshal(rec.body.Bytes(), &items) == nil {
				w.Header().Del("Content-Type")
				w.Header().Del("Content-Length")
				w.Header().Set("total-count", fmt.Sprint(len(items)))
				// Mutation META also reports the distinct samples (MutationMeta);
				// no other endpoint sets sample-count.
				if strings.Contains(r.URL.Path, "/mutations") {
					samples := map[string]bool{}
					for _, it := range items {
						if it.UniqueSampleKey != "" {
							samples[it.UniqueSampleKey] = true
						}
					}
					w.Header().Set("sample-count", fmt.Sprint(len(samples)))
				}
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		w.WriteHeader(rec.status)
		w.Write(rec.body.Bytes())
	})
}

type bufferedResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedResponse) WriteHeader(code int)        { b.status = code }

// spaHandler serves a built frontend: existing files as-is, every other path
// (client-side routes such as /results or /patient) as index.html.
func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		page, err := os.ReadFile(filepath.Join(dir, "index.html"))
		if err != nil {
			http.Error(w, "frontend index.html missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(withFrontendConfig(page, r.Host))
	})
}

// withFrontendConfig sets window.frontendConfig in the frontend's index page
// as the portal's index template does, pointing the app at this origin.
func withFrontendConfig(page []byte, host string) []byte {
	cfg, _ := json.Marshal(map[string]string{
		"configurationServiceUrl": "//" + host + "/config_service",
		"apiRoot":                 "//" + host + "/",
		"baseUrl":                 host,
		"basePath":                "",
		"frontendUrl":             "//" + host + "/",
	})
	script := []byte("<script>window.frontendConfig = Object.assign(window.frontendConfig || {}, " + string(cfg) + ");</script>" + devModeScript)
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
	return page
}

// devModeScript mirrors the Java portal page (index.html + load-frontend.js):
// ?localdev=true / ?localdist=true are remembered in localStorage and, as in
// dev mode there, a dismissible "dev mode" banner is added to the body. The
// bundle is always served from this origin.
const devModeScript = `<script>
if (/localdev=true/.test(window.location.href)) { localStorage.setItem("localdev", "true"); }
if (/localdist=true/.test(window.location.href)) { localStorage.setItem("localdist", "true"); }
window.localdev = localStorage.localdev === "true";
window.localdist = localStorage.localdist === "true";
window.netlify = localStorage.netlify;
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
