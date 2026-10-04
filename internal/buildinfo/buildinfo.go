// Package buildinfo reports the version a binary was built from, as recorded
// by the Go toolchain (module version and VCS stamp) in runtime/debug.
package buildinfo

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// stamp, when set at link time, is the version for builds without VCS
// information, such as container images built from a source tree without
// .git: go build -ldflags "-X github.com/cbioportal/cbioportal-go/internal/buildinfo.stamp=6b38ed6 (2026-10-02T09:15:00Z)".
var stamp string

// Version describes the running binary, e.g. "6b38ed6+dirty (2026-10-02T09:15:00Z)"
// for a build from a git checkout, "v1.2.0 (6b38ed6, 2026-10-02T09:15:00Z)" for
// a tagged release, or "devel" when the toolchain recorded nothing.
func Version() string {
	if stamp != "" {
		return stamp
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	return format(info)
}

// String is the -version line of the named command: "<name> <Version()>".
func String(name string) string {
	return name + " " + Version()
}

// pseudoVersion matches the module versions the toolchain derives from an
// untagged commit (v0.0.0-20261002091500-6b38ed6abcde), which say no more
// than the revision does.
var pseudoVersion = regexp.MustCompile(`-(\d+\.)?\d{14}-[0-9a-f]{12}(\+dirty)?$`)

func format(info *debug.BuildInfo) string {
	var revision, vcsTime string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			vcsTime = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	dirty := ""
	if modified {
		dirty = "+dirty"
	}

	release := strings.TrimSuffix(info.Main.Version, "+dirty")
	if release == "(devel)" || pseudoVersion.MatchString(info.Main.Version) {
		release = ""
	}

	var version string
	var details []string
	switch {
	case release != "":
		version = release + dirty
		if revision != "" {
			details = append(details, revision)
		}
	case revision != "":
		version = revision + dirty
	default:
		version = "devel"
	}
	if vcsTime != "" {
		details = append(details, vcsTime)
	}
	if len(details) > 0 {
		version += " (" + strings.Join(details, ", ") + ")"
	}
	return version
}
