// Command cbioportal-ops runs the cBioPortal team's own jobs for
// cbioportal.org: the per-pull-request previews of cbioportal-go
// (pr-<N>.gopreview.cbioportal.org) and the imports of datahub pull
// requests into hub.cbioportal.org. Settings come from the environment the
// cluster's Job manifests set; see ops/cbioportal.org/README.md.
//
//	cbioportal-ops preview setup         build PR_NUMBER's preview databases
//	cbioportal-ops preview teardown <N>  remove pull request N's preview
//	cbioportal-ops preview gc            remove previews of closed or unlabelled PRs
//	cbioportal-ops hub import-pr         import PR_NUMBER's datahub studies into the hub
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/cbioportal/cbioportal-go/internal/buildinfo"
	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/github"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/hub"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/job"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/preview"
)

const name = "cbioportal-ops"

const usage = `cbioportal-ops runs the cBioPortal team's jobs for cbioportal.org.

Usage:
  cbioportal-ops preview setup                     build the preview of cbioportal-go PR $PR_NUMBER
  cbioportal-ops preview teardown <N> [--dry-run]  remove the preview of PR N
  cbioportal-ops preview gc [--dry-run]            remove the previews of closed or unlabelled PRs
  cbioportal-ops hub import-pr                     import the studies of datahub PR $PR_NUMBER into the hub
  cbioportal-ops version [--json]

--dry-run logs what would be removed and removes nothing.
Exit status: 0 success, 1 failure, 2 invalid command line or settings.
Settings come from the environment; see ops/cbioportal.org/README.md.
ClickHouse: CLICKHOUSE_URL, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD.
GitHub App: GH_CLIENT_ID, GH_INSTALLATION_ID, GH_APP_KEY (/etc/gh-app/key.pem).
`

func main() {
	ch.SetProcessSettings(ch.ReplicaConsistentSettings)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cmd := strings.Join(args[:min(len(args), 2)], " ")
	switch {
	case len(args) == 0:
		fmt.Fprint(stderr, usage)
		return 2
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case args[0] == "version" || args[0] == "--version":
		if asJSON, _ := cli.JSONFlag(args[1:]); asJSON {
			cli.WriteJSON(stdout, buildinfo.Get(name))
		} else {
			fmt.Fprintln(stdout, buildinfo.String(name))
		}
		return 0
	}
	rest := args[min(len(args), 2):]
	dryRun := false
	if cmd == "preview teardown" || cmd == "preview gc" {
		var kept []string
		for _, a := range rest {
			if a == "--dry-run" || a == "-dry-run" {
				dryRun = true
				continue
			}
			kept = append(kept, a)
		}
		rest = kept
	}
	gh := github.FromEnv()
	var err error
	switch cmd {
	case "preview setup", "preview teardown", "preview gc":
		pr := 0
		if cmd == "preview teardown" {
			if len(rest) != 1 {
				return usageError(stderr, "preview teardown takes a pull request number")
			}
			if pr, err = strconv.Atoi(rest[0]); err != nil || pr <= 0 {
				return usageError(stderr, fmt.Sprintf("not a pull request number: %q", rest[0]))
			}
		} else if len(rest) > 0 {
			return usageError(stderr, fmt.Sprintf("unexpected argument %q", rest[0]))
		}
		var st *preview.Settings
		if st, err = preview.SettingsFromEnv(pr); err != nil {
			return usageError(stderr, err.Error())
		}
		st.DryRun = dryRun
		switch cmd {
		case "preview setup":
			err = preview.Setup(ctx, st, gh)
		case "preview teardown":
			err = preview.Teardown(ctx, st, gh, pr)
		default:
			err = preview.GC(ctx, st, gh)
		}
	case "hub import-pr":
		if len(rest) > 0 {
			return usageError(stderr, fmt.Sprintf("unexpected argument %q", rest[0]))
		}
		var st *hub.Settings
		if st, err = hub.SettingsFromEnv(); err != nil {
			return usageError(stderr, err.Error())
		}
		err = hub.ImportPR(ctx, st, gh)
	default:
		return usageError(stderr, fmt.Sprintf("unknown command %q%s", cmd,
			cli.DidYouMean(cli.Suggest(cmd, []string{"preview setup", "preview teardown", "preview gc", "hub import-pr", "version"}))))
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, preview.ErrFailed), errors.Is(err, hub.ErrFailed):
		return 1 // reported already
	}
	job.Logf("%s: %v", cmd, err)
	return 1
}

func usageError(w io.Writer, msg string) int {
	fmt.Fprintf(w, "%s: %s\n\n%s", name, msg, usage)
	return 2
}
