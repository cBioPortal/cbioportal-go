package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"2G": 2 << 30, "512M": 512 << 20, "1024": 1024, "3k": 3 << 10, "1T": 1 << 40} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "G", "-1G", "0", "2X", "1.5G"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q): no error", in)
		}
	}
}

// fakeTools puts scripts named after tools first on PATH, and nothing else.
func fakeTools(t *testing.T, scripts map[string]string) {
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"sh", "cat", "sed"} {
		if p, err := lookPathIn(tool, "/usr/bin:/bin"); err == nil {
			os.Symlink(p, filepath.Join(dir, tool))
		}
	}
	t.Setenv("PATH", dir)
}

func lookPathIn(name, path string) (string, error) {
	for _, d := range filepath.SplitList(path) {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}

func TestMemoryCapFallbacks(t *testing.T) {
	// A scope whose limit is in force.
	fakeTools(t, map[string]string{"systemd-run": `echo 2147483648`, "prlimit": `exit 0`})
	prefix, how, err := memoryCap("2G", "")
	if err != nil || prefix[0] != "systemd-run" || !strings.Contains(strings.Join(prefix, " "), "MemoryMax=2147483648") {
		t.Errorf("enforced scope: %q %q %v", prefix, how, err)
	}
	// No user manager reachable (cron): the address-space limit.
	fakeTools(t, map[string]string{"systemd-run": `echo "Failed to connect to bus: No medium found" >&2; exit 1`, "prlimit": `exit 0`})
	prefix, how, err = memoryCap("2G", "")
	if err != nil || strings.Join(prefix, " ") != "prlimit --as=4294967296 --" || !strings.Contains(how, "No medium found") {
		t.Errorf("no bus: %q %q %v", prefix, how, err)
	}
	// A scope that does not enforce the limit does not count.
	fakeTools(t, map[string]string{"systemd-run": `echo max`, "prlimit": `exit 0`})
	if prefix, _, err = memoryCap("2G", "3G"); err != nil || strings.Join(prefix, " ") != "prlimit --as=3221225472 --" {
		t.Errorf("unenforced scope: %q %v", prefix, err)
	}
	// Neither: refused.
	fakeTools(t, map[string]string{"systemd-run": `exit 1`, "prlimit": `exit 1`})
	if prefix, _, err = memoryCap("2G", ""); err == nil {
		t.Errorf("no cap available: %q, want an error", prefix)
	}
	if _, _, err = memoryCap("lots", ""); err == nil {
		t.Error("bad size: no error")
	}
}

// TestMemoryCapHere finds a hard cap on this machine with cron's
// environment: no XDG_RUNTIME_DIR, no session bus.
func TestMemoryCapHere(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("PATH", "/usr/bin:/bin")
	prefix, how, err := memoryCap("1G", "")
	if err != nil {
		t.Fatalf("no hard memory cap: %v", err)
	}
	t.Logf("%s: %q", how, prefix)
}
