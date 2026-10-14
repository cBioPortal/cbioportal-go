package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

func TestBulkLimits(t *testing.T) {
	var s settings
	newRootCmd(&s)
	if l := s.bulkLimits(); l != core.DefaultBulkLimits {
		t.Errorf("default limits = %+v, want core.DefaultBulkLimits %+v", l, core.DefaultBulkLimits)
	}
	e := env(map[string]string{"CLICKHOUSE_BULK_MAX_MEMORY": "512MiB", "CLICKHOUSE_BULK_MAX_TIME": "90", "CLICKHOUSE_BULK_SORT_SPILL": "0"})
	values, _, _ := effective(t, []string{"-clickhouse-bulk-max-time=2m"}, e)
	for k, v := range map[string]string{"clickhouse-bulk-max-memory": "512MiB", "clickhouse-bulk-max-time": "2m", "clickhouse-bulk-sort-spill": "0"} {
		if values[k] != v {
			t.Errorf("%s = %q, want %q", k, values[k], v)
		}
	}
	for _, bad := range []string{"-clickhouse-bulk-max-memory=1.5GiB", "-clickhouse-bulk-max-memory=10XB",
		"-clickhouse-bulk-max-time=soon", "-clickhouse-bulk-sort-spill=-1"} {
		var out bytes.Buffer
		if code := run([]string{"config", bad}, &out, &out, env(nil)); code != 2 {
			t.Errorf("%s: exit %d, want 2; output:\n%s", bad, code, out.String())
		}
	}
}

func TestSizeAndDurationValues(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "1024": 1024, "1KiB": 1 << 10, "10GiB": 10 << 30, "10G": 10 << 30,
		"512 MiB": 512 << 20, "2tb": 2 << 40} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for in, want := range map[byteSize]string{0: "0", 1024: "1KiB", 10 << 30: "10GiB", 1000: "1000"} {
		if got := in.String(); got != want {
			t.Errorf("byteSize(%d) = %q, want %q", int64(in), got, want)
		}
	}
	for in, want := range map[time.Duration]string{0: "0", 10 * time.Minute: "10m", 90 * time.Second: "1m30s", 2 * time.Hour: "2h"} {
		d := seconds(in)
		if got := d.String(); got != want {
			t.Errorf("seconds(%v) = %q, want %q", in, got, want)
		}
	}
}
