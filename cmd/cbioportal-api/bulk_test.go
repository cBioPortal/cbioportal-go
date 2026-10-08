package main

import (
	"testing"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

func TestBulkLimits(t *testing.T) {
	cfg, err := parseArgs(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	l, err := bulkLimits(cfg.values)
	if err != nil {
		t.Fatal(err)
	}
	if l != core.DefaultBulkLimits {
		t.Errorf("default limits = %+v, want core.DefaultBulkLimits %+v", l, core.DefaultBulkLimits)
	}
	e := env(map[string]string{"CLICKHOUSE_BULK_MAX_MEMORY": "512MiB", "CLICKHOUSE_BULK_MAX_TIME": "90", "CLICKHOUSE_BULK_SORT_SPILL": "0"})
	cfg, _ = parseArgs([]string{"-clickhouse-bulk-max-time=2m"}, e)
	l, err = bulkLimits(cfg.values)
	if err != nil {
		t.Fatal(err)
	}
	if want := (core.BulkLimits{MaxMemory: 512 << 20, MaxTime: 2 * time.Minute}); l != want {
		t.Errorf("limits = %+v, want %+v", l, want)
	}
	for _, bad := range []string{"-clickhouse-bulk-max-memory=1.5GiB", "-clickhouse-bulk-max-memory=10XB",
		"-clickhouse-bulk-max-time=soon", "-clickhouse-bulk-sort-spill=-1"} {
		cfg, _ := parseArgs([]string{bad}, env(nil))
		if _, err := bulkLimits(cfg.values); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "1024": 1024, "1KiB": 1 << 10, "10GiB": 10 << 30, "10G": 10 << 30,
		"512 MiB": 512 << 20, "2tb": 2 << 40} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}
