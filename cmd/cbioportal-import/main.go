// cbioportal-import — fast ELT loader for cBioPortal datahub studies.
// Streams raw TSV data files of a study directory straight into ClickHouse
// staging tables (schema-on-read); transforms run afterwards as SQL views.
package main

import (
	"log/slog"

	"errors"
	"flag"
	"fmt"
	"github.com/KimMachineGun/automemlimit/memlimit"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/cbioportal/cbioportal-go/internal/buildinfo"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

const name = "cbioportal-import"

func main() {
	// Go's soft memory limit at 80% of the container's, unless GOMEMLIMIT is set.
	memlimit.Set(memlimit.WithRatio(0.8), memlimit.WithLogger(slog.Default()))
	ch.SetProcessSettings(ch.ReplicaConsistentSettings)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command line and returns the exit status: 0 on success,
// 1 when the command fails, 2 for a malformed command line.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printOverview(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		if len(args) < 2 || args[0] != "help" {
			printOverview(stdout)
			return 0
		}
		if args[1] == "version" {
			fmt.Fprintf(stdout, "Usage: %s version\n\nPrint the version of %s.\n", name, name)
			return 0
		}
		cmd := lookup(args[1])
		if cmd == nil {
			fmt.Fprintf(stderr, "%s: help: unknown command %q\n\n", name, args[1])
			printOverview(stderr)
			return 2
		}
		fs, _ := cmd.flagSet()
		printCommandUsage(stdout, cmd, fs)
		return 0
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, buildinfo.String(name))
		return 0
	}

	cmd := lookup(args[0])
	if cmd == nil {
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(stderr, "%s: flag %s given before a command\n\n", name, args[0])
		} else {
			fmt.Fprintf(stderr, "%s: unknown command %q\n\n", name, args[0])
		}
		printOverview(stderr)
		return 2
	}
	fs, exec := cmd.flagSet()
	usageErr := func(err error) int {
		fmt.Fprintf(stderr, "%s: %s: %v\nRun '%s help %s' for usage.\n", name, cmd.name, err, name, cmd.name)
		return 2
	}
	if err := fs.Parse(reorder(args[1:], fs)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printCommandUsage(stdout, cmd, fs)
			return 0
		}
		return usageErr(err)
	}
	if err := cmd.checkArgs(fs.Args()); err != nil {
		return usageErr(err)
	}
	if err := exec(fs.Args(), stdout); err != nil {
		fmt.Fprintf(stderr, "%s: %s: %v\n", name, cmd.name, err)
		return 1
	}
	return 0
}

func lookup(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

// flagSet returns the command's flags and the function that runs it with
// the parsed flags and positional arguments.
func (c *command) flagSet() (*flag.FlagSet, func([]string, io.Writer) error) {
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs, c.setup(fs)
}

func (c *command) checkArgs(args []string) error {
	switch {
	case len(args) < c.minArgs:
		return fmt.Errorf("missing %s", c.args)
	case c.maxArgs >= 0 && len(args) > c.maxArgs:
		return fmt.Errorf("unexpected argument %q", args[c.maxArgs])
	}
	return nil
}

// reorder moves flags (and their values) ahead of positional arguments, so
// `ingest <dir> --db X` works although the flag package stops at the first
// positional. A flag consumes the following argument as its value only if fs
// defines it as non-boolean and it is not written -name=value; everything
// after "--" is positional.
func reorder(args []string, fs *flag.FlagSet) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		body := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		name, _, hasValue := strings.Cut(body, "=")
		f := fs.Lookup(name)
		if f != nil && !hasValue && !isBoolFlag(f) {
			if i+1 == len(args) {
				// A value flag at the very end has no value: leave it last
				// so that fs.Parse reports it.
				return flags
			}
			i++
			flags = append(flags, args[i])
		}
	}
	if len(pos) == 0 {
		return flags
	}
	return append(append(flags, "--"), pos...)
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func printOverview(w io.Writer) {
	fmt.Fprintf(w, `%[1]s loads cBioPortal studies in datahub format into ClickHouse.

Usage:
  %[1]s <command> [arguments] [flags]

Commands:
`, name)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(tw, "  %s\t%s\n", c.name, c.short)
	}
	fmt.Fprintf(tw, "  version\tprint the version\n")
	fmt.Fprintf(tw, "  help\tshow help for a command\n")
	tw.Flush()
	fmt.Fprintf(w, `
A typical import (each step's --db is the next step's input):
  %[1]s load-reference seed-cbioportal_hg19_hg38_v2.14.5.sql.gz --db cbioportal_ref
  %[1]s ingest datahub/public/acbc_mskcc_2015 --db cbioportal_raw
  %[1]s transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref

%[2]s
Flags may come before or after the arguments.
Run '%[1]s help <command>' for a command's flags and examples.
`, name, environmentHelp)
}

const environmentHelp = `Environment:
  CLICKHOUSE_URL       default for --url (http://localhost:18123)
  CLICKHOUSE_USER      ClickHouse user (unset: the default user)
  CLICKHOUSE_PASSWORD  ClickHouse password; credentials are read from the
                       environment only, so they stay out of the process list
`

func printCommandUsage(w io.Writer, c *command, fs *flag.FlagSet) {
	synopsis := c.name
	if c.args != "" {
		synopsis += " " + c.args
	}
	hasFlags := false
	fs.VisitAll(func(*flag.Flag) { hasFlags = true })
	if hasFlags {
		synopsis += " [flags]"
	}
	fmt.Fprintf(w, "Usage: %s %s\n\n%s\n", name, synopsis, c.long)
	if hasFlags {
		fmt.Fprintln(w, "\nFlags:")
		fs.SetOutput(w)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
	}
	if len(c.examples) > 0 {
		fmt.Fprintln(w, "\nExamples:")
		for _, e := range c.examples {
			fmt.Fprintf(w, "  %s %s\n", name, e)
		}
	}
	if fs.Lookup("url") != nil {
		fmt.Fprintf(w, "\n%s", environmentHelp)
	}
}
