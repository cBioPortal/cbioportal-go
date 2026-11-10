package parity

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestReadBasicAuth(t *testing.T) {
	file := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(file, []byte("s3cret\r\nignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := readBasicAuth("parity", file)
	if err != nil || auth != (basicAuth{"parity", "s3cret"}) {
		t.Fatalf("readBasicAuth: %+v, %v", auth, err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.org/api/info", nil)
	auth.set(req)
	if u, p, ok := req.BasicAuth(); !ok || u != "parity" || p != "s3cret" {
		t.Errorf("request auth %q %q %v", u, p, ok)
	}

	if auth, err := readBasicAuth("", "/nonexistent"); err != nil || auth != (basicAuth{}) {
		t.Errorf("no user: %+v, %v", auth, err)
	}
	req, _ = http.NewRequest(http.MethodGet, "https://example.org/api/info", nil)
	basicAuth{}.set(req)
	if _, _, ok := req.BasicAuth(); ok {
		t.Error("the zero basicAuth sent credentials")
	}
	if _, err := readBasicAuth("parity", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing password file was accepted")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	os.WriteFile(empty, []byte("\n"), 0o600)
	if _, err := readBasicAuth("parity", empty); err == nil {
		t.Error("an empty password was accepted")
	}
}
