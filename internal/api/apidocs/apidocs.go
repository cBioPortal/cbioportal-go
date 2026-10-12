package apidocs

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// The API descriptions and Swagger UI, as the Java portal serves them:
//
//   - /api/v2/api-docs: the legacy Swagger 2.0 description API clients are
//     generated from (e.g. bravado's SwaggerClient.from_url), the Java
//     portal's own hand-maintained cbioportal src/main/resources/
//     legacy-api.json, with its host set to the requesting host.
//   - /api/v3/api-docs[/{public,internal}] and their .yaml forms: the
//     OpenAPI 3.1 descriptions springdoc generates for the Java portal,
//     with the server URL set to the requesting origin.
//   - /api/v3/api-docs/swagger-config and /api/swagger-ui/: Swagger UI.
//
// The v3 descriptions and Swagger UI are snapshots of api.cbioportal.org's
// API (scripts/refresh-api-docs.sh refreshes them). Every description is
// served with the paging parameters go adds laid over it (overlayAPIDoc).
//
//go:embed *.json *.yaml swagger-ui
var apiDocsFS embed.FS

// snapshotOrigin is the server URL the v3 snapshots were taken with.
const snapshotOrigin = "https://api.cbioportal.org"

// Routes registers the API description and Swagger UI endpoints.
func (s Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v2/api-docs", s.legacyAPIDocs)
	for route, file := range map[string]string{
		"/api/v3/api-docs":               "v3.json",
		"/api/v3/api-docs/public":        "v3-public.json",
		"/api/v3/api-docs/internal":      "v3-internal.json",
		"/api/v3/api-docs.yaml":          "v3.yaml",
		"/api/v3/api-docs.yaml/public":   "v3-public.yaml",
		"/api/v3/api-docs.yaml/internal": "v3-internal.yaml",
	} {
		mux.HandleFunc("GET "+route, openAPIDoc(file))
	}
	mux.HandleFunc("GET /api/v3/api-docs/swagger-config", swaggerConfig)
	// /api and /api/ lead to the API docs, as springdoc's redirect does on
	// the Java portal for /api.
	toDocs := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, core.BaseURL(r)+"/api/swagger-ui", http.StatusFound)
	}
	mux.HandleFunc("GET /api", toDocs)
	mux.HandleFunc("GET /api/{$}", toDocs)
	mux.HandleFunc("GET /api/swagger-ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, core.BaseURL(r)+"/api/swagger-ui/index.html", http.StatusFound)
	})
	ui, _ := fs.Sub(apiDocsFS, "swagger-ui")
	mux.HandleFunc("GET /api/swagger-ui/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		b, err := fs.ReadFile(ui, name)
		if err != nil {
			core.WriteError(w, http.StatusNotFound, "No static resource api/swagger-ui/"+name+".")
			return
		}
		ct := mime.TypeByExtension(path.Ext(name))
		if path.Ext(name) == ".js" {
			ct = "text/javascript"
		}
		w.Header().Set("Content-Type", ct)
		w.Write(b)
	})
}

// openAPIDoc serves a v3 description with its server URL set to the
// requesting origin, as springdoc's "Generated server url".
func openAPIDoc(file string) http.HandlerFunc {
	b, err := apiDocsFS.ReadFile(file)
	if err == nil {
		b, err = overlayAPIDoc(file, b)
	}
	var before, after []byte
	if err == nil {
		yaml := strings.HasSuffix(file, ".yaml")
		marker := []byte(`"servers":[{"url":"` + snapshotOrigin + `"`)
		if yaml {
			marker = []byte("- url: " + snapshotOrigin + "\n")
		}
		if i := bytes.Index(b, marker); i >= 0 {
			start := i + bytes.Index(marker, []byte(snapshotOrigin))
			before, after = b[:start], b[start+len(snapshotOrigin):]
		} else {
			before = b
		}
	}
	ct := "application/json"
	if strings.HasSuffix(file, ".yaml") {
		ct = "application/vnd.oai.openapi"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !accepts(r, ct) {
			core.SpringError(w, r, http.StatusNotAcceptable)
			return
		}
		if err != nil {
			core.WriteError(w, http.StatusInternalServerError, "API description unavailable")
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Write(before)
		if after != nil {
			w.Write([]byte(core.BaseURL(r)))
			w.Write(after)
		}
	}
}

// accepts reports whether the Accept header allows the media type ct (no
// header allows any). Spring answers 406 Not Acceptable otherwise, e.g.
// for application/json on the YAML descriptions.
func accepts(r *http.Request, ct string) bool {
	typ, _, _ := strings.Cut(ct, "/")
	named := false
	for _, v := range r.Header.Values("Accept") {
		for _, part := range strings.Split(v, ",") {
			mt, _, _ := strings.Cut(part, ";")
			mt = strings.ToLower(strings.TrimSpace(mt))
			if mt == "" {
				continue
			}
			named = true
			if mt == "*/*" || mt == typ+"/*" || mt == ct {
				return true
			}
		}
	}
	return !named
}

// swaggerConfig is springdoc's Swagger UI configuration for the portal's two
// API groups.
func swaggerConfig(w http.ResponseWriter, r *http.Request) {
	redirect, _ := json.Marshal(core.BaseURL(r) + "/api/swagger-ui/oauth2-redirect.html")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"configUrl":"/api/v3/api-docs/swagger-config","oauth2RedirectUrl":` + string(redirect) +
		`,"urls":[{"url":"/api/v3/api-docs/internal","name":"internal"},{"url":"/api/v3/api-docs/public","name":"public"}],` +
		`"urls.primaryName":"public","validatorUrl":""}`))
}

// legacyDocs is the legacy description compacted, split around the value of
// its top-level "host" key.
var legacyDocs struct {
	once          sync.Once
	before, after []byte
	err           error
}

// legacyAPIDocs serves the legacy API description with its host set to the
// host the client asked for.
func (s Server) legacyAPIDocs(w http.ResponseWriter, r *http.Request) {
	legacyDocs.once.Do(func() {
		doc, err := apiDocsFS.ReadFile("legacy-api.json")
		if err == nil {
			doc, err = overlayAPIDoc("legacy-api.json", doc)
		}
		if err != nil {
			legacyDocs.err = err
			return
		}
		legacyDocs.before, legacyDocs.after, legacyDocs.err = splitTopLevelValue(doc, "host")
	})
	if legacyDocs.err != nil {
		core.WriteError(w, http.StatusInternalServerError, "legacy API description unavailable")
		return
	}
	host, _ := json.Marshal(r.Host)
	w.Header().Set("Content-Type", "application/json")
	w.Write(legacyDocs.before)
	w.Write(host)
	w.Write(legacyDocs.after)
}

// splitTopLevelValue compacts the JSON object doc and returns the bytes
// before and after the value of its top-level key (the value itself left
// out). Without that key, the key is appended as the object's last member.
func splitTopLevelValue(doc []byte, key string) (before, after []byte, err error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc); err != nil {
		return nil, nil, err
	}
	b := compact.Bytes()
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil { // {
		return nil, nil, err
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		start := dec.InputOffset() + 1 // past the ':'
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		if t == key {
			end := dec.InputOffset()
			return b[:start:start], b[end:], nil
		}
	}
	k, _ := json.Marshal(key)
	before = append(append(append([]byte{}, b[:len(b)-1]...), ','), k...)
	return append(before, ':'), []byte("}"), nil
}
