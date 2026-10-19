// Package datahub checks out studies of cBioPortal/datahub: a shallow,
// sparse git checkout of just the studies asked for, with their large files
// downloaded from datahub's own Git LFS store.
package datahub

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/cli"
)

// Defaults for the datahub repository.
const (
	DefaultGit = "https://github.com/cBioPortal/datahub.git"
	// DefaultLFSURL is datahub's LFS store (its .lfsconfig). It also holds
	// the files of pull requests from forks.
	DefaultLFSURL = "https://nsssw8k94d.execute-api.us-east-1.amazonaws.com/"
)

// Paths of the shared files every checkout includes.
const (
	GenePanelsDir = "reference_data/gene_panels"
	SeedDir       = "seedDB"
	seedPattern   = "seed-cbioportal_hg19_hg38_*.sql.gz"
)

// GitURL is DATAHUB_GIT, else DefaultGit.
func GitURL() string { return cli.EnvOr("DATAHUB_GIT", DefaultGit) }

// LFSURL is DATAHUB_LFS_URL, else DefaultLFSURL.
func LFSURL() string { return cli.EnvOr("DATAHUB_LFS_URL", DefaultLFSURL) }

// Checkout is a sparse checkout of datahub in Dir.
type Checkout struct {
	Dir    string // the repository directory; Fetch recreates it
	Git    string // remote URL (GitURL())
	LFSURL string // LFS store (LFSURL())
	// FallbackLFSURL, when set, is tried for objects the LFS store does not
	// have, e.g. the GitHub LFS of a pull request's head repository.
	FallbackLFSURL string
	Logf           func(format string, args ...any)

	fetched string
}

func (c *Checkout) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// SparsePatterns are the sparse-checkout patterns for the study paths
// (public/<study>, ...): the studies, the shared gene panels and the seed
// dumps.
func SparsePatterns(paths []string) []string {
	var out []string
	for _, p := range paths {
		p = strings.Trim(p, "/")
		if p != "" {
			out = append(out, "/"+p+"/")
		}
	}
	return append(out, "/"+GenePanelsDir+"/", "/"+SeedDir+"/"+seedPattern)
}

// Fetch fetches ref (a branch, a commit or pull/<N>/head) into a new
// repository in Dir, shallow and without blobs outside the study paths,
// and returns the fetched commit. CheckoutFetched then checks it out.
func (c *Checkout) Fetch(ctx context.Context, ref string, paths []string) (string, error) {
	if err := os.RemoveAll(c.Dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return "", err
	}
	gitURL := c.Git
	if gitURL == "" {
		gitURL = GitURL()
	}
	steps := [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", gitURL},
	}
	for _, s := range steps {
		if _, err := c.git(ctx, nil, s...); err != nil {
			return "", err
		}
	}
	patterns := strings.Join(SparsePatterns(paths), "\n") + "\n"
	if _, err := c.git(ctx, strings.NewReader(patterns), "sparse-checkout", "set", "--no-cone", "--stdin"); err != nil {
		return "", err
	}
	if _, err := c.git(ctx, nil, "fetch", "-q", "--depth=1", "--filter=blob:none", "origin", ref); err != nil {
		return "", err
	}
	out, err := c.git(ctx, nil, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", err
	}
	c.fetched = strings.TrimSpace(out)
	return c.fetched, nil
}

// CheckoutFetched checks out the commit of the last Fetch. Large files stay
// LFS pointer files until PullLFS replaces them.
func (c *Checkout) CheckoutFetched(ctx context.Context) error {
	_, err := c.git(ctx, nil, "-c", "advice.ignoredHook=false", "checkout", "-q", "FETCH_HEAD")
	return err
}

// git runs git in Dir. LFS smudging is off: PullLFS downloads the files.
func (c *Checkout) git(ctx context.Context, stdin *strings.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", c.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_LFS_SKIP_SMUDGE=1", "GIT_TERMINAL_PROMPT=0")
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// StudyDirs returns the study directories (public/<study>,
// crdc/gdc/<study>) that file paths of the repository fall in, sorted and
// without duplicates.
func StudyDirs(files []string) []string {
	var out []string
	for _, f := range files {
		parts := strings.Split(f, "/")
		switch {
		case len(parts) >= 3 && parts[0] == "public" && parts[1] != "":
			out = append(out, "public/"+parts[1])
		case len(parts) >= 4 && parts[0] == "crdc" && parts[1] == "gdc" && parts[2] != "":
			out = append(out, "crdc/gdc/"+parts[2])
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TouchesGenePanels reports whether any of the files is a shared gene panel.
func TouchesGenePanels(files []string) bool {
	return slices.ContainsFunc(files, func(f string) bool { return strings.HasPrefix(f, GenePanelsDir+"/") })
}

// NewestSeed returns the newest seed dump of the checkout ("" when none),
// comparing the version numbers in the names.
func NewestSeed(repo string) string {
	files, _ := filepath.Glob(filepath.Join(repo, SeedDir, seedPattern))
	if len(files) == 0 {
		return ""
	}
	slices.SortFunc(files, compareVersions)
	return files[len(files)-1]
}

// compareVersions orders strings as `sort -V` does for names that differ in
// their numbers: runs of digits compare as numbers.
func compareVersions(a, b string) int {
	for a != "" && b != "" {
		da, db := digitPrefix(a), digitPrefix(b)
		if da != "" && db != "" {
			na, _ := strconv.ParseUint(da, 10, 64)
			nb, _ := strconv.ParseUint(db, 10, 64)
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			if a[0] < b[0] {
				return -1
			}
			return 1
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func digitPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
