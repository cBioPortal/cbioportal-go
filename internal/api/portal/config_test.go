package portal

import (
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
