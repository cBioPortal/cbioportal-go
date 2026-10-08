package core

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// frontendState is a State serving the frontend build in dir.
func frontendState(dir string) *State {
	return &State{frontendDir: dir, frontendFiles: newStaticFiles(dir)}
}

func TestStaticFiles(t *testing.T) {
	dir := t.TempDir()
	js := strings.Repeat("console.log('cbioportal');\n", 500)
	for name, body := range map[string]string{
		"reactapp/main.app.js":                              js,
		"reactapp/pathway-mapper.bc002c372d94c03c.chunk.js": js,
		"images/logo.png":                                   "\x89PNG",
		"index.html":                                        "<html><head></head><body></body></html>",
	} {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	h := http.HandlerFunc(frontendState(dir).CatchAll)
	get := func(path string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := get("/reactapp/main.app.js", map[string]string{"Accept-Encoding": "gzip, br"})
	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Cache-Control") != "no-cache" ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("headers %v", w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != js {
		t.Fatal("decompressed body differs")
	}
	etag := w.Header().Get("ETag")
	if w := get("/reactapp/main.app.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag}); w.Code != http.StatusNotModified {
		t.Errorf("revalidation: %d, want 304", w.Code)
	}

	w = get("/reactapp/pathway-mapper.bc002c372d94c03c.chunk.js", map[string]string{"Accept-Encoding": "gzip"})
	if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("hashed chunk Cache-Control %q", w.Header().Get("Cache-Control"))
	}

	w = get("/reactapp/main.app.js", nil)
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != js {
		t.Errorf("without Accept-Encoding: encoding %q, %d bytes", w.Header().Get("Content-Encoding"), w.Body.Len())
	}
	if w := get("/images/logo.png", map[string]string{"Accept-Encoding": "gzip"}); w.Header().Get("Content-Encoding") != "" {
		t.Error("image compressed")
	}
	if w := get("/results/oncoprint", nil); !strings.Contains(w.Body.String(), "frontendConfig") {
		t.Error("client route did not serve index.html")
	}
}
