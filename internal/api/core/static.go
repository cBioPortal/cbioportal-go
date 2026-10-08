package core

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// staticFiles serves the frontend build. Text assets are gzip-compressed
// once, at the best compression level, and kept in memory (the bundles are
// several MB and every new browser downloads them). Files with a content
// hash in their name never change and are cached by browsers for a year;
// the others revalidate with an ETag.
type staticFiles struct {
	dir   string
	files http.Handler

	mu sync.Mutex
	gz map[string]*gzAsset
}

type gzAsset struct {
	once sync.Once
	mod  time.Time
	size int64
	data []byte
	etag string
}

// hashedAsset matches webpack's content-hashed file names, e.g.
// pathway-mapper.bc002c372d94c03c.chunk.js.
var hashedAsset = regexp.MustCompile(`\.[0-9a-f]{8,}(\.chunk)?\.(js|css)$`)

func newStaticFiles(dir string) *staticFiles {
	s := &staticFiles{dir: dir, files: http.FileServer(http.Dir(dir)), gz: map[string]*gzAsset{}}
	go s.warm()
	return s
}

func compressible(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".js", ".css", ".html", ".json", ".map", ".svg", ".txt", ".xml", ".wasm":
		return true
	}
	return false
}

// warm compresses the build's text assets in the background at startup.
func (s *staticFiles) warm() {
	filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !compressible(p) {
			return nil
		}
		if st, err := d.Info(); err == nil {
			s.asset(p, st)
		}
		return nil
	})
}

// asset returns the compressed form of file p, compressing it on first use
// and again when the file changes.
func (s *staticFiles) asset(p string, st os.FileInfo) *gzAsset {
	s.mu.Lock()
	a := s.gz[p]
	if a == nil || !a.mod.Equal(st.ModTime()) || a.size != st.Size() {
		a = &gzAsset{mod: st.ModTime(), size: st.Size()}
		s.gz[p] = a
	}
	s.mu.Unlock()
	a.once.Do(func() {
		raw, err := os.ReadFile(p)
		if err != nil {
			return
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		zw.Write(raw)
		zw.Close()
		a.data = buf.Bytes()
		a.etag = fmt.Sprintf(`"%x-%x-gz"`, a.mod.UnixNano(), a.size)
	})
	return a
}

// serve answers a request for the file p (st is its stat).
func (s *staticFiles) serve(w http.ResponseWriter, r *http.Request, p string, st os.FileInfo) {
	h := w.Header()
	if hashedAsset.MatchString(p) {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if !compressible(p) || r.Header.Get("Range") != "" || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		s.files.ServeHTTP(w, r)
		return
	}
	a := s.asset(p, st)
	if a.data == nil {
		s.files.ServeHTTP(w, r)
		return
	}
	ct := mime.TypeByExtension(filepath.Ext(p))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Add("Vary", "Accept-Encoding")
	h.Set("ETag", a.etag)
	if r.Header.Get("If-None-Match") == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", fmt.Sprint(len(a.data)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(a.data)
}
