// Command cbioportal-rewrite-test holds the tools that check go against the
// Java cBioPortal it replaces. They are needed only while that migration
// runs; each migration is one command group, whose code lives in its own
// folder under rewrite/.
//
//	cbioportal-rewrite-test java-v7-to-go mirror    rebuild a copy of production
//	cbioportal-rewrite-test java-v7-to-go parity    the nightly API comparison
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/cbioportal/cbioportal-go/internal/buildinfo"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/rewrite/java-v7-to-go/mirror"
	"github.com/cbioportal/cbioportal-go/rewrite/java-v7-to-go/parity"
)

const name = "cbioportal-rewrite-test"

// command is one subcommand of a group: it takes the arguments after its
// name and returns the exit status.
type command struct {
	name, short string
	run         func(args []string, stdout, stderr io.Writer) int
}

type group struct {
	name, short string
	commands    []command
}

var groups = []group{
	{
		name:  "java-v7-to-go",
		short: "compare go with Java cBioPortal v7 (www.cbioportal.org)",
		commands: []command{
			{"mirror", "rebuild go's databases from a clone of production's", mirror.Main},
			{"parity", "replay production traffic against go and Java and report", parity.Main},
		},
	},
}

func main() {
	ch.SetProcessSettings(ch.ReplicaConsistentSettings)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return 0
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, buildinfo.String(name))
		return 0
	}
	for _, g := range groups {
		if g.name != args[0] {
			continue
		}
		if len(args) < 2 || args[1] == "help" || args[1] == "-h" || args[1] == "--help" {
			groupUsage(stdout, g)
			if len(args) < 2 {
				return 2
			}
			return 0
		}
		for _, c := range g.commands {
			if c.name == args[1] {
				return c.run(args[2:], stdout, stderr)
			}
		}
		fmt.Fprintf(stderr, "%s: %s: unknown command %q\n\n", name, g.name, args[1])
		groupUsage(stderr, g)
		return 2
	}
	fmt.Fprintf(stderr, "%s: unknown group %q\n\n", name, args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "%s checks go against the Java cBioPortal it replaces.\n\nUsage:\n  %s <group> <command> [flags]\n\nGroups:\n", name, name)
	for _, g := range groups {
		fmt.Fprintf(w, "  %-16s %s\n", g.name, g.short)
	}
	fmt.Fprintf(w, "\nRun '%s <group> help' for a group's commands, '%s <group> <command> -h' for a command's flags.\n", name, name)
}

func groupUsage(w io.Writer, g group) {
	fmt.Fprintf(w, "%s %s: %s\n\nCommands:\n", name, g.name, g.short)
	for _, c := range g.commands {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.short)
	}
}
