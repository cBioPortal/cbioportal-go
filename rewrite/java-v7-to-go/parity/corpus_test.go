package parity

import (
	"strings"
	"testing"
	"time"
)

// Each night samples the UTC day before it, with the night's date as seed.
func TestCorpusArgsWindow(t *testing.T) {
	night := time.Date(2026, 10, 10, 15, 17, 0, 0, time.UTC)
	got := strings.Join(corpusArgs(night), " ")
	for _, want := range []string{"-seed 20261010", "-since 2026-10-09 00:00:00", "-until 2026-10-10 00:00:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
}
