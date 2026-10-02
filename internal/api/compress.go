package api

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

var gzipWriters = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return zw
}}

// gzipResponse compresses response bodies for clients that accept gzip. The
// decision is made on the first write, once the handler has set its headers:
// already-encoded bodies, bodiless statuses and binary media pass through.
func gzipResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Upgrade") != "" || r.Header.Get("Range") != "" ||
			!strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	zw      *gzip.Writer
	decided bool
	status  int
}

func (g *gzipResponseWriter) decide() {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	status := g.status
	if status == 0 {
		status = http.StatusOK
	}
	ct := h.Get("Content-Type")
	if h.Get("Content-Encoding") == "" && status != http.StatusNoContent && status != http.StatusNotModified &&
		!strings.HasPrefix(ct, "image/") && !strings.HasPrefix(ct, "font/") && !strings.HasPrefix(ct, "video/") {
		h.Set("Content-Encoding", "gzip")
		h.Del("Content-Length")
		g.zw = gzipWriters.Get().(*gzip.Writer)
		g.zw.Reset(g.ResponseWriter)
	}
	if g.status != 0 {
		g.ResponseWriter.WriteHeader(g.status)
	}
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.decided {
		return
	}
	g.status = code
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified {
		g.decide()
	}
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.decided && g.Header().Get("Content-Type") == "" {
		g.Header().Set("Content-Type", http.DetectContentType(p))
	}
	g.decide()
	if g.zw == nil {
		return g.ResponseWriter.Write(p)
	}
	return g.zw.Write(p)
}

func (g *gzipResponseWriter) Flush() {
	g.decide()
	if g.zw != nil {
		g.zw.Flush()
	}
	http.NewResponseController(g.ResponseWriter).Flush()
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponseWriter) close() {
	if !g.decided && g.status != 0 {
		g.decide()
	}
	if g.zw != nil {
		g.zw.Close()
		gzipWriters.Put(g.zw)
	}
}
