package datahub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runGit runs git in dir for a test.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.org",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.org")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestChangedStudies follows two commits of a small datahub: study a is
// removed, b changed and c added.
func TestChangedStudies(t *testing.T) {
	src := t.TempDir()
	runGit(t, src, "init", "-q", "-b", "master")
	runGit(t, src, "config", "uploadpack.allowFilter", "true")
	runGit(t, src, "config", "uploadpack.allowAnySHA1InWant", "true")
	write := func(path, body string) {
		p := filepath.Join(src, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("public/a/meta_study.txt", "type_of_cancer: brca\ncancer_study_identifier: a_id\n")
	write("public/b/meta_study.txt", "cancer_study_identifier: b_id\n")
	write("public/b/data_clinical_sample.txt", "x\n")
	write("public/notastudy/README", "x\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "one")
	first := runGit(t, src, "rev-parse", "HEAD")
	write("public/b/data_clinical_sample.txt", "y\n")
	runGit(t, src, "rm", "-q", "-r", "public/a")
	write("public/c/meta_study.txt", "cancer_study_identifier: c_id\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "two")
	second := runGit(t, src, "rev-parse", "HEAD")

	ctx := context.Background()
	url := "file://" + src
	head, err := HeadCommit(ctx, url, "master")
	if err != nil || head != second {
		t.Fatalf("HeadCommit = %q, %v; want %s", head, err, second)
	}
	co := &Checkout{Dir: filepath.Join(t.TempDir(), "repo"), Git: url}
	if got, err := co.Fetch(ctx, "master", nil); err != nil || got != second {
		t.Fatalf("Fetch = %q, %v", got, err)
	}
	files, err := co.Files(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if got := StudyDirsWithMeta(files); !slices.Equal(got, []string{"public/b", "public/c"}) {
		t.Errorf("studies at the head = %v", got)
	}
	if err := co.FetchCommit(ctx, first); err != nil {
		t.Fatal(err)
	}
	changed, err := co.ChangedFiles(ctx, first, second)
	if err != nil {
		t.Fatal(err)
	}
	if got := StudyDirs(changed); !slices.Equal(got, []string{"public/a", "public/b", "public/c"}) {
		t.Errorf("changed studies = %v", got)
	}
	meta, err := co.Show(ctx, first, "public/a/meta_study.txt")
	if err != nil || StudyIDFromMeta(meta) != "a_id" {
		t.Errorf("old meta_study.txt of a: %q, %v", meta, err)
	}
	if err := co.SetPaths(ctx, []string{"public/c"}); err != nil {
		t.Fatal(err)
	}
	if err := co.CheckoutFetched(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(co.Dir, "public/c/meta_study.txt")); err != nil {
		t.Errorf("public/c not checked out: %v", err)
	}
	if _, err := os.Stat(filepath.Join(co.Dir, "public/b")); err == nil {
		t.Errorf("public/b checked out, want only public/c")
	}
}
