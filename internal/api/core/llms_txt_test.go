package core

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func getLlmsTxt(s *State) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeLLMsTxt(rec, httptest.NewRequest(http.MethodGet, "/llms.txt", nil))
	return rec
}

// Unset llms_txt.location answers 404 with no body.
func TestLlmsTxtUnset(t *testing.T) {
	rec := getLlmsTxt(&State{Props: map[string]string{}})
	if rec.Code != http.StatusNotFound || rec.Body.Len() != 0 {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
}

// A missing file answers 404.
func TestLlmsTxtMissingFile(t *testing.T) {
	rec := getLlmsTxt(&State{Props: map[string]string{LLMsTxtProperty: "file:" + filepath.Join(t.TempDir(), "none.txt")}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}

// The file is served as text/markdown and re-read only once the TTL has
// passed; a failed re-read keeps the last good copy.
func TestLlmsTxtServedAndCached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llms.txt")
	os.WriteFile(path, []byte("# Portal\nv1\n"), 0o644)
	clock := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	s := &State{Props: map[string]string{LLMsTxtProperty: " file:" + path + " "}}
	s.llmsTxt.now = func() time.Time { return clock }

	rec := getLlmsTxt(s)
	if rec.Code != http.StatusOK || rec.Body.String() != "# Portal\nv1\n" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/markdown;charset=UTF-8" {
		t.Errorf("content type %q", ct)
	}

	os.WriteFile(path, []byte("v2"), 0o644)
	clock = clock.Add(llmsTxtTTL - time.Second)
	if got := getLlmsTxt(s).Body.String(); got != "# Portal\nv1\n" {
		t.Errorf("within TTL: %q", got)
	}
	clock = clock.Add(time.Second)
	if got := getLlmsTxt(s).Body.String(); got != "v2" {
		t.Errorf("after TTL: %q", got)
	}

	os.Remove(path)
	clock = clock.Add(llmsTxtTTL)
	if rec := getLlmsTxt(s); rec.Code != http.StatusOK || rec.Body.String() != "v2" {
		t.Errorf("unreadable file: status %d body %q", rec.Code, rec.Body)
	}
}
