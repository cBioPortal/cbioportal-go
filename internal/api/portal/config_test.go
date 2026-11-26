package portal

import (
	"regexp"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/buildinfo"
)

func TestGoRewriteVersionOf(t *testing.T) {
	for release, want := range map[string]string{
		"v0.1.0":             "v0.1.0",
		"v0.1.0-12-g6b38ed6": "v0.1.0-12-g6b38ed6",
		"":                   "dev",
	} {
		if got := goRewriteVersionOf(release); got != want {
			t.Errorf("%q: %s, want %s", release, got, want)
		}
	}
}

func TestSetGitCommit(t *testing.T) {
	info := Info{GitBranch: gitNotSet, GitCommitMessageFull: gitNotSet, GitCommitMessageShort: gitNotSet,
		GitCommitMessageUserName: gitNotSet, GitCommitMessageUserEmail: gitNotSet}
	setGitCommit(&info, buildinfo.GitCommit{Branch: "main", Message: "Fix it\n\nBody", UserName: "A", UserEmail: "a@b"})
	if info.GitBranch != "main" || info.GitCommitMessageFull != "Fix it\n\nBody" || info.GitCommitMessageShort != "Fix it" ||
		info.GitCommitMessageUserName != "A" || info.GitCommitMessageUserEmail != "a@b" {
		t.Errorf("stamped: %+v", info)
	}

	unstamped := Info{GitBranch: gitNotSet, GitCommitMessageShort: gitNotSet}
	setGitCommit(&unstamped, buildinfo.GitCommit{})
	if unstamped.GitBranch != gitNotSet || unstamped.GitCommitMessageShort != gitNotSet {
		t.Errorf("unstamped: %+v", unstamped)
	}
}

// The upstream sync edits these by hand every time it goes through a range of
// cBioPortal commits, so check their shape: portalVersion is a cBioPortal
// release tag, and the parity commit is a full sha, not a short one.
func TestJavaParityConstants(t *testing.T) {
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(javaPortalVersion) {
		t.Errorf("javaPortalVersion %q, want a release tag such as v7.2.8", javaPortalVersion)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(javaParityCommit) {
		t.Errorf("javaParityCommit %q, want a full 40-character sha", javaParityCommit)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(dbSchemaVersion) {
		t.Errorf("dbSchemaVersion %q, want the release's pom.xml db.version, such as 3.1.0", dbSchemaVersion)
	}
}
