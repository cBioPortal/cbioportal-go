// Command cbioportal-rewrite-test holds the tools that check go against the
// Java cBioPortal it replaces. They are needed only while that migration
// runs; each migration is one command group, whose code lives in its own
// folder under rewrite/.
//
//	cbioportal-rewrite-test java-v7-to-go mirror    rebuild a copy of production
//	cbioportal-rewrite-test java-v7-to-go parity    the nightly API comparison
package main

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/rewrite/java-v7-to-go/mirror"
	"github.com/cbioportal/cbioportal-go/rewrite/java-v7-to-go/parity"
)

const name = "cbioportal-rewrite-test"

func main() {
	ch.SetProcessSettings(ch.ReplicaConsistentSettings)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command line and returns its exit status (cli.Exit*).
func run(args []string, stdout, stderr io.Writer) int {
	return cli.Execute(newRoot(), args, stdout, stderr, os.Getenv)
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   name,
		Short: "check go against the Java cBioPortal it replaces",
		Long: `cbioportal-rewrite-test checks go against the Java cBioPortal it replaces.
Each migration is one command group.`,
	}
	javaV7 := &cobra.Command{Use: "java-v7-to-go", Short: "compare go with Java cBioPortal v7 (www.cbioportal.org)"}
	javaV7.AddCommand(mirror.Command(), parity.Command())
	root.AddCommand(javaV7)
	cli.AddVersion(root, name)
	return root
}
