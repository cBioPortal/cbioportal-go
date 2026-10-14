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
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/github"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/hub"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/job"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/preview"
)

const name = "cbioportal-ops"

func main() {
	ch.SetProcessSettings(ch.ReplicaConsistentSettings)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run executes one command line and returns its exit status (cli.Exit*).
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return cli.Execute(newRoot(ctx), args, stdout, stderr, os.Getenv)
}

func newRoot(ctx context.Context) *cobra.Command {
	root := &cobra.Command{
		Use:   name,
		Short: "run the cBioPortal team's jobs for cbioportal.org",
		Long: `cbioportal-ops runs the cBioPortal team's jobs for cbioportal.org: the
previews of cbioportal-go pull requests and the imports of datahub pull
requests into the hub.

Settings come from the environment the cluster's Job manifests set; see
ops/cbioportal.org/README.md.
  ClickHouse:  CLICKHOUSE_URL, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD
  GitHub App:  GH_CLIENT_ID, GH_INSTALLATION_ID, GH_APP_KEY (/etc/gh-app/key.pem)

Exit status: 0 success, 1 failure, 2 invalid command line or settings.`,
	}
	previewCmd := &cobra.Command{Use: "preview", Short: "build and remove the previews of cbioportal-go pull requests"}
	previewCmd.AddCommand(previewSetup(ctx), previewTeardown(ctx), previewGC(ctx))
	hubCmd := &cobra.Command{Use: "hub", Short: "import datahub pull requests into hub.cbioportal.org"}
	hubCmd.AddCommand(hubImportPR(ctx))
	root.AddCommand(previewCmd, hubCmd)
	cli.AddVersion(root, name)
	return root
}

func previewSetup(ctx context.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "build the preview of cbioportal-go pull request $PR_NUMBER",
		Args:  cli.ArgsBetween(0, 0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := preview.SettingsFromEnv(0)
			if err != nil {
				return cli.Usage(err)
			}
			return finish(cmd, preview.Setup(ctx, st, github.FromEnv()))
		},
	}
}

func previewTeardown(ctx context.Context) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "teardown <N>",
		Short: "remove the preview of pull request N",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return cli.Usagef("preview teardown takes a pull request number")
			}
			if pr, err := strconv.Atoi(args[0]); err != nil || pr <= 0 {
				return cli.Usagef("not a pull request number: %q", args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, _ := strconv.Atoi(args[0])
			st, err := preview.SettingsFromEnv(pr)
			if err != nil {
				return cli.Usage(err)
			}
			st.DryRun = dryRun
			return finish(cmd, preview.Teardown(ctx, st, github.FromEnv(), pr))
		},
	}
	dryRunFlag(cmd, &dryRun)
	return cmd
}

func previewGC(ctx context.Context) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "remove the previews of closed or unlabelled pull requests",
		Args:  cli.ArgsBetween(0, 0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := preview.SettingsFromEnv(0)
			if err != nil {
				return cli.Usage(err)
			}
			st.DryRun = dryRun
			return finish(cmd, preview.GC(ctx, st, github.FromEnv()))
		},
	}
	dryRunFlag(cmd, &dryRun)
	return cmd
}

func hubImportPR(ctx context.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "import-pr",
		Short: "import the studies of datahub pull request $PR_NUMBER into the hub",
		Args:  cli.ArgsBetween(0, 0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := hub.SettingsFromEnv()
			if err != nil {
				return cli.Usage(err)
			}
			return finish(cmd, hub.ImportPR(ctx, st, github.FromEnv()))
		},
	}
}

// dryRunFlag registers --dry-run, which logs what would be removed and
// removes nothing.
func dryRunFlag(cmd *cobra.Command, dryRun *bool) {
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "log what would be removed, and remove nothing")
}

// finish logs a job's error in the job log's format. A job that failed
// with preview.ErrFailed or hub.ErrFailed has reported why already.
func finish(cmd *cobra.Command, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, preview.ErrFailed), errors.Is(err, hub.ErrFailed):
		return cli.ErrReported
	}
	job.Logf("%s %s: %v", cmd.Parent().Name(), cmd.Name(), err)
	return cli.ErrReported
}
