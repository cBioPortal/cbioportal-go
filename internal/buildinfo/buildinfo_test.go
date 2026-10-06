package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

func info(version string, settings ...string) *debug.BuildInfo {
	bi := &debug.BuildInfo{Main: debug.Module{Path: "github.com/cbioportal/cbioportal-go", Version: version}}
	for i := 0; i+1 < len(settings); i += 2 {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
	}
	return bi
}

func TestFormat(t *testing.T) {
	const rev = "6b38ed6abcdef0123456789abcdef0123456789a"
	const when = "2026-10-02T09:15:00Z"
	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"git checkout", info("(devel)", "vcs.revision", rev, "vcs.time", when, "vcs.modified", "false"), "6b38ed6 (2026-10-02T09:15:00Z)"},
		{"dirty checkout", info("(devel)", "vcs.revision", rev, "vcs.time", when, "vcs.modified", "true"), "6b38ed6+dirty (2026-10-02T09:15:00Z)"},
		{"pseudo-version", info("v0.0.0-20261002091500-6b38ed6abcde+dirty", "vcs.revision", rev, "vcs.time", when, "vcs.modified", "true"), "6b38ed6+dirty (2026-10-02T09:15:00Z)"},
		{"tagged release", info("v1.2.0", "vcs.revision", rev, "vcs.time", when, "vcs.modified", "false"), "v1.2.0 (6b38ed6, 2026-10-02T09:15:00Z)"},
		{"go install module@version", info("v1.2.0"), "v1.2.0"},
		{"no stamp", info("(devel)"), "devel"},
		{"empty", info(""), "devel"},
	}
	for _, tt := range tests {
		if got := format(tt.info); got != tt.want {
			t.Errorf("%s: format = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestString(t *testing.T) {
	if got := String("cbioportal-api"); !strings.HasPrefix(got, "cbioportal-api ") || len(got) <= len("cbioportal-api ") {
		t.Errorf("String = %q", got)
	}
}

func TestCommitStamp(t *testing.T) {
	old := commit
	defer func() { commit = old }()
	commit = "6b38ed6abcdef0123456789abcdef0123456789a"
	if rev, modified := Commit(); rev != commit || modified {
		t.Fatalf("Commit() = %q, %v, want the stamped commit", rev, modified)
	}
}

func TestStampOverrides(t *testing.T) {
	old := stamp
	defer func() { stamp = old }()
	stamp = "6b38ed6 (2026-10-02T09:15:00Z)"
	if got := Version(); got != stamp {
		t.Fatalf("Version() = %q, want %q", got, stamp)
	}
}
