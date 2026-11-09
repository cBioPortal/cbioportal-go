// Package buildinfo reports the version a binary was built from, as recorded
// by the Go toolchain (module version and VCS stamp) in runtime/debug.
package buildinfo

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// stamp, when set at link time, is the version for builds without VCS
// information, such as container images built from a source tree without
// .git: go build -ldflags "-X github.com/cbioportal/cbioportal-go/internal/buildinfo.stamp=6b38ed6 (2026-10-02T09:15:00Z)".
var stamp string

// commit, when set at link time, is the full git revision of such a build:
// -ldflags "-X github.com/cbioportal/cbioportal-go/internal/buildinfo.commit=6b38ed6…".
var commit string

// release, when set at link time, is the build's place among the release
// tags as git describe --tags gives it: v0.1.0 for a tagged release,
// v0.1.0-12-g6b38ed6 for a later commit.
var release string

// Release is the build's release, "" when it was built without one.
func Release() string { return release }

// gitCommit, when set at link time, describes the commit the build was made
// from, as base64 of a JSON GitCommit, so a multi-line message survives the
// linker flags: -ldflags "-X github.com/cbioportal/cbioportal-go/internal/buildinfo.gitCommit=eyJicmFuY2giOi…".
var gitCommit string

// GitCommit is the branch and commit details a build was stamped with.
type GitCommit struct {
	Branch    string `json:"branch"`
	Message   string `json:"message"`
	UserName  string `json:"userName"`
	UserEmail string `json:"userEmail"`
}

// Git is the build's stamped commit details; its fields are "" when the
// build has none.
func Git() GitCommit { return decodeGitCommit(gitCommit) }

func decodeGitCommit(stamped string) GitCommit {
	var c GitCommit
	raw, err := base64.StdEncoding.DecodeString(stamped)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return GitCommit{}
	}
	return c
}

// Commit is the git revision the binary was built from and whether the
// working tree had changes: the stamped commit, else the toolchain's VCS
// information, else "".
func Commit() (revision string, modified bool) {
	if commit != "" {
		return commit, false
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return revision, modified
}

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

// Info is a program's version, for --json output.
type Info struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Modified bool   `json:"modified"`
	Go       string `json:"go"`
}

// Get returns the version of the program name.
func Get(name string) Info {
	rev, modified := Commit()
	return Info{Name: name, Version: Version(), Commit: rev, Modified: modified, Go: runtime.Version()}
}
