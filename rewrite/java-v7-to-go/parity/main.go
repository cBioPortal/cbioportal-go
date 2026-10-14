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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/cbioportal/cbioportal-go/internal/cli"
)

// Command is `cbioportal-rewrite-test java-v7-to-go parity`.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "parity",
		Short: "replay production traffic against go and Java and report",
		Long: `Parity replays a sample of the previous day's production requests against
go (A) and Java (B), reports every difference, and keeps the GitHub issue
that tracks the clean nights up to date. See docs/rewrite/parity.md.`,
	}
	cmd.AddCommand(runCommand(), statusCommand(), corpusCommand(), nightlyCommand())
	return cmd
}

// stateFlags are the flags that locate the run directory and nights.txt.
type stateFlags struct {
	root, dir, state string
}

func (s *stateFlags) register(fs *pflag.FlagSet) {
	fs.StringVar(&s.root, "root", "", "repository `directory` (default: the go.mod above the working directory)")
	fs.StringVar(&s.dir, "dir", "", "`directory` of corpus.txt, runs/<date>/ (kept 14 days) and last-unexplained.txt (default: <root>/.parity/nightly)")
	fs.StringVar(&s.state, "state", "", "`directory` of nights.txt, one \"<date> clean|unclean\" line per night (default: --dir)")
	cli.Env(fs, "dir", "PARITY_DIR")
	cli.Env(fs, "state", "PARITY_STATE")
}

// resolve fills in the defaults that depend on the repository root.
func (s *stateFlags) resolve() (root, dir, state string, err error) {
	if root, err = rootDir(s.root); err != nil {
		return "", "", "", cli.Usage(err)
	}
	dir = s.dir
	if dir == "" {
		dir = filepath.Join(root, ".parity/nightly")
	}
	state = s.state
	if state == "" {
		state = dir
	}
	return root, dir, state, nil
}

// issueFlags name the tracking issue.
type issueFlags struct {
	repo, number string
}

func (i *issueFlags) register(fs *pflag.FlagSet) {
	fs.StringVar(&i.repo, "issue-repo", "cBioPortal/cbioportal-go", "`repository` of the tracking issue")
	fs.StringVar(&i.number, "issue", "", "the tracking issue's `number` (default: looked up by its label)")
	cli.Env(fs, "issue-repo", "ISSUE_REPO")
	cli.Env(fs, "issue", "PARITY_ISSUE")
}

func runCommand() *cobra.Command {
	var st stateFlags
	var is issueFlags
	var day, a, b, ua, memoryMax, memoryAS, goMemLimit string
	var rate float64
	var dry bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "compare a new sample of the previous day's traffic and report",
		Long: `Run samples a new corpus from the previous day's logged traffic, replays
it against A and B, writes runs/<date>/ (results.json, output.txt,
report.md, issue.md, dump/) and updates the tracking issue.

paritydiff runs under a hard memory cap (--memory-max): the MemoryMax of a
systemd user scope (found without a login session too, via /run/user/UID),
checked to be in force; where there is none, an address-space limit
(prlimit --as) of --memory-as (default twice --memory-max, as Go reserves
address space ahead). With neither, run refuses to start paritydiff.

The corpus is sampled with CORPUS_N (5000), CORPUS_MIN (3), CORPUS_SEED (the
night's date), CORPUS_SINCE and CORPUS_UNTIL (the UTC day before the night)
and CORPUS_API_SHARE (0.25), from ClickHouse at CLICKHOUSE_URL/USER/PASSWORD,
else from the file CLOUD_ENV (~/.config/cbioportal-go/cloud.env).

Exit status: 0 when every request is identical or a known difference, 1 on
unexplained differences, status mismatches or errors, 2 when the check
itself could not run.`,
		Args: cli.ArgsBetween(0, 0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, dir, state, err := st.resolve()
			if err != nil {
				return err
			}
			clean, err := nightlyRun(runOptions{
				root: root, dir: dir, state: state,
				a: a, b: b, rate: strconv.FormatFloat(rate, 'f', -1, 64), ua: ua,
				issueRepo: is.repo, issue: is.number,
				memoryMax: memoryMax, memoryAS: memoryAS, goMemLimit: goMemLimit,
				day: day, dry: dry, stdout: cmd.OutOrStdout(), stderr: cmd.ErrOrStderr(),
			})
			if err != nil {
				return cli.Usage(fmt.Errorf("nightly: %w", err))
			}
			if !clean {
				return cli.ErrReported
			}
			return nil
		},
	}
	fs := cmd.Flags()
	st.register(fs)
	fs.StringVar(&day, "day", "", "record the run as this night, `YYYY-MM-DD`, and sample the day before it (default: today, UTC)")
	fs.BoolVar(&dry, "dry-run", false, "print the GitHub issue actions instead of running gh, and record nothing in --state")
	fs.StringVar(&a, "origin-a", "https://go.cbioportal.org", "origin A, the go API")
	fs.StringVar(&b, "origin-b", "https://api.cbioportal.org", "origin B, Java production")
	fs.Float64Var(&rate, "rate", 4, "requests per second to B, both of its runs together")
	fs.StringVar(&ua, "ua", "cbioportal-go-parity", "User-Agent (api.cbioportal.org refuses browsers)")
	is.register(fs)
	fs.StringVar(&memoryMax, "memory-max", "2G", "hard memory cap of paritydiff")
	fs.StringVar(&memoryAS, "memory-as", "", "address-space cap of paritydiff where no systemd scope can be had (default: twice --memory-max)")
	fs.StringVar(&goMemLimit, "gomemlimit", "1500MiB", "paritydiff's GOMEMLIMIT, its collector's soft limit")
	for name, env := range map[string]string{"origin-a": "A", "origin-b": "B", "rate": "RATE", "ua": "UA",
		"memory-max": "PARITY_MEMORY_MAX", "memory-as": "PARITY_MEMORY_AS", "gomemlimit": "GOMEMLIMIT"} {
		cli.Env(fs, name, env)
	}
	return cmd
}

func statusCommand() *cobra.Command {
	var st stateFlags
	var is issueFlags
	var dry bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "rewrite the tracking issue's description from nights.txt",
		Long: `Status rewrites the tracking issue's description from nights.txt as it is
now, after a night's record was changed by hand.`,
		Args: cli.ArgsBetween(0, 0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _, state, err := st.resolve()
			if err != nil {
				return err
			}
			return refreshStatus(runOptions{
				state: state, issueRepo: is.repo, issue: is.number,
				dry: dry, stdout: cmd.OutOrStdout(), stderr: cmd.ErrOrStderr(),
			})
		},
	}
	fs := cmd.Flags()
	st.register(fs)
	is.register(fs)
	fs.BoolVar(&dry, "dry-run", false, "print the description instead of editing the issue")
	return cmd
}

func corpusCommand() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "corpus [OUT]",
		Short: "write the logged-traffic corpus",
		Long: `Corpus samples the previous day's logged traffic into OUT (default
<root>/.parity/nightly/corpus.txt), with the CORPUS_* settings of run.`,
		Args: cli.ArgsBetween(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := rootDir(root)
			if err != nil {
				return cli.Usage(err)
			}
			out := filepath.Join(r, ".parity/nightly/corpus.txt")
			if len(args) > 0 {
				out = args[0]
			}
			return writeCorpus(r, out, time.Now().UTC(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "repository `directory` (default: the go.mod above the working directory)")
	return cmd
}

func nightlyCommand() *cobra.Command {
	home, _ := os.UserHomeDir()
	var dir, repo string
	cmd := &cobra.Command{
		Use:   "nightly [-- RUN-ARGS]",
		Short: "the cron entry: one run from a fresh checkout, logged",
		Long: `Nightly is the cron entry: one run at a time, from a checkout (<home>/src)
of the ref in <home>/state/ref (default origin/main), logged to
<home>/logs. Arguments after -- go to run.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			switch cronNightly(nightlyOptions{home: dir, repo: repo, extra: args}) {
			case 0:
				return nil
			case 1:
				return cli.ErrReported
			}
			return cli.Usagef("the check could not run; see the log in %s", filepath.Join(dir, "logs"))
		},
	}
	cmd.Flags().StringVar(&dir, "home", filepath.Join(home, ".local/share/cbioportal-go-parity"), "logs, state and checkout `directory`")
	cmd.Flags().StringVar(&repo, "repo", filepath.Join(home, "projects/cbioportal-go"), "local clone to fetch unpushed branches from")
	return cmd
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
