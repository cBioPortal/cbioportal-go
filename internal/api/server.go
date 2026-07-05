// Package api serves the cBioPortal REST contract from the ClickHouse transform
// views, with JSON field shapes matching the Java backend so the existing
// frontend works unchanged.
package api

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

type State struct {
	CH       *ch.Client
	sessions sync.Map // id -> json.RawMessage
	counter  atomic.Uint64
}

func NewState(c *ch.Client) *State { return &State{CH: c} }

// NewRouter builds the API handler with CORS, optional bearer-token auth
// (disabled by default), and a body-draining catch-all.
func NewRouter(s *State) http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return cors(gunzipBody(auth(loadAuthConfig(), mux)))
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
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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

// drainReadJSON decodes the request body into v, ensuring the body is fully
// consumed (an unread large body makes Go close the keep-alive connection,
// which the browser reports as a failed/cross-domain request).
func readBody(r *http.Request, v any) {
	if v != nil {
		json.NewDecoder(r.Body).Decode(v)
	}
	io.Copy(io.Discard, r.Body)
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
