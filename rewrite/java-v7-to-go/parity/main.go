// Package parity is the nightly API parity check of go.cbioportal.org (A,
// the go API on the production mirror) against api.cbioportal.org (B, Java
// production): it replays a corpus of logged production requests to both,
// writes a short report, and keeps one GitHub issue open while unexplained
// differences, status mismatches or errors remain. Every request goes to B
// twice (paritydiff -self-check): where B's two answers differ, the request
// is nondeterministic in Java, reported apart and never unexplained.
//
// It is read-only: the corpus holds no writing endpoints and B gets at most
// RATE requests per second, both of its runs together. Responses are not
// cached; only the bodies of unexplained requests are kept, cut to 1 MB
// each (runs/<date>/dump).
package parity

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const usage = `Usage:
  parity run [-regen] [-dry-run] [-root DIR]
      one comparison from the repository at DIR (default: the go.mod above
      the working directory). -regen regenerates the corpus first (also done
      when there is none, or it predates the windowed generator); -dry-run
      prints the GitHub issue actions instead of running gh and records
      nothing in the state directory.
  parity corpus [-root DIR] [OUT]
      write the logged-traffic corpus (default DIR/.parity/nightly/corpus.txt)
  parity nightly [-home DIR] [-repo DIR] [-- RUN-ARGS]
      the cron entry: one run at a time, from a checkout (DIR/src) of the
      ref in DIR/state/ref (default origin/main), logged to DIR/logs, with
      -regen on Sundays.

Environment of run:
  PARITY_DIR     corpus.txt, runs/<date>/ (results.json, output.txt,
                 report.md, issue.md, dump/; kept 14 days) and
                 last-unexplained.txt; default .parity/nightly
  PARITY_STATE   nights.txt, one "<date> clean|unclean" line per night, for
                 the clean nights in a row (7 are the go/no-go for
                 api.cbioportal.org); default PARITY_DIR
  A, B           origins (https://go.cbioportal.org, https://api.cbioportal.org)
  RATE           requests per second to B, both runs together (4)
  UA             User-Agent (cbioportal-go-parity; api.cbioportal.org refuses browsers)
  ISSUE_REPO     repository of the alert issue (cBioPortal/cbioportal-go)
  PARITY_ISSUE   the alert issue's number, instead of looking it up
  PARITY_MEMORY_MAX  hard memory cap of paritydiff (2G): MemoryMax of a
                 systemd user scope (found without a login session too, via
                 /run/user/UID), checked to be in force; where there is none,
                 an address-space limit (prlimit --as) of PARITY_MEMORY_AS
                 (twice PARITY_MEMORY_MAX, as Go reserves address space
                 ahead). With neither, run refuses to start paritydiff.
                 GOMEMLIMIT (1500MiB) is its collector's soft limit
Environment of corpus: CORPUS_N (5000), CORPUS_MIN (3), CORPUS_SEED (the ISO
  week), CORPUS_SINCE, CORPUS_UNTIL, CORPUS_API_SHARE (0.25); ClickHouse from
  CLICKHOUSE_URL/USER/PASSWORD, else CLOUD_ENV (~/.config/cbioportal-go/cloud.env).

Exit status of run: 0 when every request is identical or a known
difference, 1 on unexplained differences, status mismatches or errors, 2
when the check itself could not run.
`

// Main runs a parity subcommand and returns the exit status.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("parity "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	switch args[0] {
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "run":
		root := fs.String("root", "", "repository root")
		regen := fs.Bool("regen", false, "regenerate the corpus first")
		dry := fs.Bool("dry-run", false, "print the GitHub issue actions instead of running gh")
		if err := fs.Parse(args[1:]); err != nil {
			return flagStatus(err)
		}
		if fs.NArg() > 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		r, err := rootDir(*root)
		if err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		dir := env("PARITY_DIR", filepath.Join(r, ".parity/nightly"))
		clean, err := nightlyRun(runOptions{
			root: r, dir: dir, state: env("PARITY_STATE", dir),
			a: env("A", "https://go.cbioportal.org"), b: env("B", "https://api.cbioportal.org"),
			rate: env("RATE", "4"), ua: env("UA", "cbioportal-go-parity"),
			issueRepo: env("ISSUE_REPO", "cBioPortal/cbioportal-go"), issue: os.Getenv("PARITY_ISSUE"),
			memoryMax: env("PARITY_MEMORY_MAX", "2G"), memoryAS: os.Getenv("PARITY_MEMORY_AS"), goMemLimit: env("GOMEMLIMIT", "1500MiB"),
			regen: *regen, dry: *dry, stdout: stdout, stderr: stderr,
		})
		if err != nil {
			fmt.Fprintln(stderr, "nightly:", err)
			return 2
		}
		if !clean {
			return 1
		}
		return 0
	case "corpus":
		root := fs.String("root", "", "repository root")
		if err := fs.Parse(args[1:]); err != nil {
			return flagStatus(err)
		}
		r, err := rootDir(*root)
		if err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		out := filepath.Join(r, ".parity/nightly/corpus.txt")
		if fs.NArg() > 0 {
			out = fs.Arg(0)
		}
		if err := writeCorpus(r, out, stderr); err != nil {
			fmt.Fprintln(stderr, "corpus:", err)
			return 1
		}
		return 0
	case "nightly":
		home, _ := os.UserHomeDir()
		dir := fs.String("home", filepath.Join(home, ".local/share/cbioportal-go-parity"), "logs, state and checkout `directory`")
		repo := fs.String("repo", filepath.Join(home, "projects/cbioportal-go"), "local clone to fetch unpushed branches from")
		if err := fs.Parse(args[1:]); err != nil {
			return flagStatus(err)
		}
		return cronNightly(nightlyOptions{home: *dir, repo: *repo, extra: fs.Args()})
	}
	fmt.Fprintf(stderr, "parity: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func flagStatus(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func rootDir(root string) (string, error) {
	if root != "" {
		return filepath.Abs(root)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return repoRoot(wd)
}
