// Package api serves the cBioPortal REST contract from the ClickHouse transform
// views, with JSON field shapes matching the Java backend so the existing
// frontend works unchanged.
package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

type State struct {
	CH           *ch.Client
	sessions     sessionStore
	sessionsOnce sync.Once
	meta         metaCache
	// frontend serves every non-API path — a built frontend (FRONTEND_DIR) or a
	// proxied dev server (FRONTEND_URL) — so the portal UI and the API share one
	// origin as in a cBioPortal deployment.
	frontend http.Handler
	// mux is the route table, consulted by the catch-all to tell an unknown
	// path from a known one requested with an unsupported method.
	mux *http.ServeMux
	// Props are the portal properties (skin.*, priority_studies, …) the
	// frontend configuration is built from. PropsReplaceDefaults is set when
	// they come from a properties file, which replaces the stock defaults as
	// a mounted application.properties does for the Java portal.
	Props                map[string]string
	PropsReplaceDefaults bool
	// login is the OAuth2 login (authenticate=oauth2 or optional_oauth2) and
	// access its study authorization; nil login serves every study
	// anonymously.
	login  *oauth2Login
	access accessPolicy
	// dat serves and verifies data access tokens (dat.method=oauth2).
	dat *datProvider
}

// ConfigureAuth sets up the login and study authorization the portal
// properties ask for.
func (s *State) ConfigureAuth() error {
	props := config.EffectiveProperties(s.Props, s.PropsReplaceDefaults)
	cfg, err := loadOAuth2Config(props)
	if err != nil {
		return err
	}
	datCfg, err := loadDATConfig(props)
	if err != nil {
		return err
	}
	if datCfg != nil {
		if cfg == nil || cfg.optional() {
			return errors.New("dat.method=oauth2 needs authenticate=oauth2")
		}
		if loadAuthConfig().Enabled {
			return errors.New("dat.method=oauth2 verifies bearer tokens itself; unset AUTH_ENABLED")
		}
	}
	if cfg == nil {
		return nil
	}
	store, err := loadLoginStore(props)
	if err != nil {
		return err
	}
	s.login = newOAuth2Login(cfg, store)
	s.access = newAccessPolicy(props)
	s.access.anonymous = cfg.optional()
	for _, reg := range cfg.Registrations {
		issuer := reg.IssuerURI
		if issuer == "" {
			issuer = reg.AuthorizationURI
		}
		log.Printf("authenticate=%s: login with %s through %s (client %s)", cfg.Method, reg.ID, issuer, reg.ClientID)
	}
	if !cfg.MethodAuthorization {
		log.Printf("authenticate=%s: every study is open to everyone (security.method_authorization_enabled is off)", cfg.Method)
	}
	log.Printf("login sessions: %s", store.describe())
	if datCfg != nil {
		s.dat = newDATProvider(datCfg)
		s.login.dat = s.dat
		log.Printf("dat.method=oauth2: data access tokens from %s (client %s)", datCfg.Issuer, datCfg.ClientID)
	}
	return nil
}

// NewState returns the API state over ClickHouse client c; ConfigureAuth and
// ConfigureSessions apply the portal properties.
func NewState(c *ch.Client) *State {
	s := &State{CH: c}
	if dir := os.Getenv("FRONTEND_DIR"); dir != "" {
		s.frontend = spaHandler(dir)
	} else if u, err := url.Parse(os.Getenv("FRONTEND_URL")); err == nil && u.Host != "" {
		s.frontend = httputil.NewSingleHostReverseProxy(u)
	}
	return s
}

// NewRouter builds the API handler with CORS, the OAuth2 login, data access
// tokens and study authorization (authenticate=oauth2), optional HS256
// bearer-token auth (disabled by default), and a body-draining catch-all.
func NewRouter(s *State) http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	authCfg := loadAuthConfig()
	if s.login != nil {
		authCfg.DefaultUser = "" // users log in
	}
	h := auth(authCfg, s.studyAvailability(s.extraEndpoints(s.javaContract(pageList(metaProjection(bodyErrors(mux)))))))
	if s.login != nil {
		if s.login.cfg.MethodAuthorization {
			h = s.studyAccess(s.access, h)
		}
		h = s.login.middleware(h)
	}
	return s.observe(cors(gzipResponse(gunzipBody(h))))
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

func fail(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// sqlLit and inList write values into SQL text as escaped string literals,
// for values chosen by code (database and table names, constant lists, column
// headers of loaded files). Request values go to ClickHouse as query
// parameters (sqlArgs, ch.QueryP, cachedQueryP).
func sqlLit(s string) string { return ch.SQLLit(s) }

func inList(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = "'" + sqlLit(v) + "'"
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
	files := newStaticFiles(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			files.serve(w, r, p, st)
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
	page = withSiteBanner(page, os.Getenv("SITE_BANNER"), os.Getenv("SITE_BANNER_LINK"))
	// An element after #reactRoot keeps it from being the body's last child:
	// otherwise every node the frontend appends to the body to measure text
	// flips :last-child matching and restyles the whole app.
	if i := bytes.Index(page, []byte("</body>")); i >= 0 {
		page = append(append(append([]byte{}, page[:i]...), "<script></script>"...), page[i:]...)
	}
	return page
}

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
