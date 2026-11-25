package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptOldFolder(t *testing.T) {
	parent := t.TempDir()
	old := filepath.Join(parent, oldName)
	if err := os.MkdirAll(filepath.Join(old, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(old, oldName+".log"), []byte("before\n"), 0o644)

	dir := filepath.Join(parent, name)
	if got := adoptOldFolder(dir); got != old {
		t.Fatalf("adoptOldFolder = %q, want %q", got, old)
	}
	if _, err := os.Stat(filepath.Join(dir, "db")); err != nil {
		t.Errorf("the old folder's contents did not move: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, name+".log")); err != nil || string(b) != "before\n" {
		t.Errorf("log under the new name = %q, %v", b, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the old folder is still there")
	}

	// Once the new folder exists, an old one (made again) stays put.
	os.MkdirAll(old, 0o755)
	if got := adoptOldFolder(dir); got != "" {
		t.Errorf("moved %q over an existing folder", got)
	}
	// A folder of another name (--data) is left alone.
	if got := adoptOldFolder(filepath.Join(parent, "mine")); got != "" {
		t.Errorf("moved %q for a folder given by hand", got)
	}
}
