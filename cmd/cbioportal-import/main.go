// cbioportal-import — fast ELT loader for cBioPortal datahub studies.
// Streams raw TSV data files of a study directory straight into ClickHouse
// staging tables (schema-on-read); transforms run afterwards as SQL views.
package main

import (
	"log/slog"

	"io"
	"os"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/spf13/cobra"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

const name = "cbioportal-import"

func main() {
	// Go's soft memory limit at 80% of the container's, unless GOMEMLIMIT is set.
	memlimit.Set(memlimit.WithRatio(0.8), memlimit.WithLogger(slog.Default()))
	ch.SetProcessSettings(ch.ReplicaConsistentSettings)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

// run executes one command line and returns the exit status: 0 on success,
// 1 when the command fails, 2 for a malformed command line.
func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	return cli.Execute(newRootCmd(), args, stdout, stderr, getenv)
}

const environmentHelp = `Environment:
  CLICKHOUSE_URL       default for --url (http://localhost:18123)
  CLICKHOUSE_USER      ClickHouse user (unset: the default user)
  CLICKHOUSE_PASSWORD  ClickHouse password; credentials are read from the
                       environment only, so they stay out of the process list`

// newRootCmd is the program's command tree: one subcommand per step of an
// import.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   name,
		Short: "load cBioPortal studies in datahub format into ClickHouse",
		Long: name + ` loads cBioPortal studies in datahub format into ClickHouse.

Each step's --db is the next step's input. Flags may come before or after
the arguments.

` + environmentHelp,
		Example: `  ` + name + ` load-reference seed-cbioportal_hg19_hg38_v2.14.5.sql.gz --db cbioportal_ref
  ` + name + ` ingest datahub/public/acbc_mskcc_2015 --db cbioportal_raw
  ` + name + ` transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref`,
	}
	root.AddCommand(
		newPlanCmd(),
		newIngestCmd(),
		newRemoveStudyCmd(),
		newIngestCaseListsCmd(),
		newLoadReferenceCmd(),
		newLoadGenesetsCmd(),
		newLoadGenePanelsCmd(),
		newImportStudyCmd(),
		newFetchDatahubCmd(),
		newTransformCmd(),
	)
	cli.AddVersion(root, name)
	return root
}
