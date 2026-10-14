package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/cbioportal/cbioportal-go/internal/buildinfo"
)

// The programs' command lines are cobra commands run by Execute, which adds
// the conventions of docs/cli.md that cobra does not have:
//
//   - Exit status 2 for any mistake in the command line or the settings
//     (an unknown command or flag, a missing argument, a bad value), 1 when
//     the command fails while it runs, 0 otherwise.
//   - Long flags may be written with one dash: -db x is --db x.
//   - A flag may fall back to an environment variable (Env); the value's
//     source is kept for `config`-style output (Source).
//   - A mistyped command or flag gets "did you mean" with the nearest name.
//   - A command with subcommands and no action of its own prints its help
//     when run alone and rejects an unknown subcommand.

// usageError marks a mistake in the command line or the settings.
type usageError struct {
	err    error
	silent bool // already reported (the help was printed)
}

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// Usagef returns an error that makes the program exit with ExitUsage.
func Usagef(format string, a ...any) error {
	return usageError{err: fmt.Errorf(format, a...)}
}

// Usage marks err as a mistake in the command line or the settings.
func Usage(err error) error {
	if err == nil {
		return nil
	}
	return usageError{err: err}
}

// ErrReported is returned by a command that failed and has already said
// why: the program exits with ExitFailure and prints nothing more.
var ErrReported = errors.New("failed")

const (
	envAnnotation    = "cbioportal_env"
	groupAnnotation  = "cbioportal_group"
	sourceAnnotation = "cbioportal_source"
	groupOnly        = "cbioportal_group_only"
)

// Env makes flag name fall back to the environment variable env when it is
// not given on the command line, and names the variable in the help.
func Env(fs *pflag.FlagSet, name, env string) {
	f := fs.Lookup(name)
	if f == nil {
		panic("cli.Env: no flag " + name)
	}
	fs.SetAnnotation(name, envAnnotation, []string{env})
	f.Usage += " ($" + env + ")"
}

// Group lists flags under a heading in the help instead of under "Flags".
func Group(fs *pflag.FlagSet, title string, names ...string) {
	for _, n := range names {
		if fs.Lookup(n) == nil {
			panic("cli.Group: no flag " + n)
		}
		fs.SetAnnotation(n, groupAnnotation, []string{title})
	}
}

// Source says where a parsed flag's value came from: "flag", "env $NAME",
// "default", or "" for an unset flag without a default.
func Source(f *pflag.Flag) string {
	if s := f.Annotations[sourceAnnotation]; len(s) == 1 {
		return s[0]
	}
	return ""
}

// EnvName is the environment variable flag f falls back to, or "".
func EnvName(f *pflag.Flag) string {
	if e := f.Annotations[envAnnotation]; len(e) == 1 {
		return e[0]
	}
	return ""
}

// applyEnv sets every flag that was not given from its environment
// variable, and records each flag's source.
func applyEnv(fs *pflag.FlagSet, getenv func(string) string) error {
	var err error
	fs.VisitAll(func(f *pflag.Flag) {
		env := EnvName(f)
		source := ""
		switch {
		case f.Changed:
			source = "flag"
		case env != "" && getenv(env) != "":
			if e := f.Value.Set(getenv(env)); e != nil && err == nil {
				err = Usagef("$%s: %v", env, e)
			}
			source = "env $" + env
		case f.DefValue != "" && f.Value.Type() != "bool" || f.DefValue == "true":
			source = "default"
		}
		if source != "" {
			fs.SetAnnotation(f.Name, sourceAnnotation, []string{source})
		}
	})
	return err
}

// ArgsBetween accepts min to max positional arguments (max < 0: no limit).
// The usage line names them: the part of cmd.Use after the command name.
func ArgsBetween(min, max int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) < min:
			_, names, _ := strings.Cut(cmd.Use, " ")
			return Usagef("missing %s", strings.TrimSuffix(names, " [flags]"))
		case max >= 0 && len(args) > max:
			return Usagef("unexpected argument %q", args[max])
		}
		return nil
	}
}

// AddVersion gives root a version command (with --json) and a --version
// flag, both printing the build's version.
func AddVersion(root *cobra.Command, name string) {
	root.Version = buildinfo.String(name)
	root.SetVersionTemplate("{{.Version}}\n")
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "print the version",
		Args:  ArgsBetween(0, 0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON {
				return WriteJSON(cmd.OutOrStdout(), buildinfo.Get(name))
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), buildinfo.String(name))
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the version as JSON")
	root.AddCommand(cmd)
}

// Execute runs the command line args against root and returns the exit
// status. Errors are printed on stderr, prefixed with the program's name.
func Execute(root *cobra.Command, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	ran := false
	prepare(root, getenv, &ran)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(normalize(root, args))
	cmd, err := root.ExecuteC()
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, ErrReported) {
		return ExitFailure
	}
	var ue usageError
	usage := errors.As(err, &ue) || !ran
	if ue.silent {
		return ExitUsage
	}
	where := root.Name()
	if cmd != nil && cmd != root {
		where += ": " + strings.TrimPrefix(cmd.CommandPath(), root.Name()+" ")
	}
	fmt.Fprintf(stderr, "%s: %v\n", where, err)
	if usage {
		help := root.Name() + " -h"
		if cmd != nil && cmd != root {
			help = "help " + strings.TrimPrefix(cmd.CommandPath(), root.Name()+" ")
			help = root.Name() + " " + help
		}
		fmt.Fprintf(stderr, "Run '%s' for usage.\n", help)
		return ExitUsage
	}
	return ExitFailure
}

// prepare adds the conventions above to every command of the tree.
func prepare(root *cobra.Command, getenv func(string) string, ran *bool) {
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.DisableSuggestions = true
	root.SetFlagErrorFunc(flagError)
	root.SetUsageTemplate(usageTemplate)
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		switch c.Name() {
		case "help":
			c.Short = "show the help of a command"
		case "completion":
			c.Short = "print a shell script that completes this program's commands and flags"
		}
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		c.InitDefaultHelpFlag()
		if f := c.Flags().Lookup("help"); f != nil {
			f.Usage = "show this help"
		}
		if c == root && root.Version != "" {
			c.InitDefaultVersionFlag()
			if f := c.Flags().Lookup("version"); f != nil {
				f.Usage = "print the version"
			}
		}
		if c.Runnable() {
			run := c.RunE
			if run == nil {
				r := c.Run
				run = func(cmd *cobra.Command, args []string) error { r(cmd, args); return nil }
				c.Run = nil
			}
			c.RunE = func(cmd *cobra.Command, args []string) error {
				if err := applyEnv(cmd.Flags(), getenv); err != nil {
					return err
				}
				*ran = true
				return run(cmd, args)
			}
			return
		}
		if c.HasSubCommands() {
			c.Args = cobra.ArbitraryArgs
			c.RunE = groupRun
			if c.Annotations == nil {
				c.Annotations = map[string]string{}
			}
			c.Annotations[groupOnly] = "true"
		}
	}
	walk(root)
}

// groupRun runs a command that only groups subcommands: alone it prints its
// help on stderr; with an argument, that argument is not one of them.
func groupRun(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(cmd.ErrOrStderr(), cmd.UsageString())
		return usageError{err: errors.New("no command"), silent: true}
	}
	return UnknownCommand(cmd, args[0])
}

// UnknownCommand is the error for an argument where one of cmd's
// subcommands should be.
func UnknownCommand(cmd *cobra.Command, name string) error {
	var names []string
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() || c.Name() == "help" {
			names = append(names, c.Name())
		}
	}
	return Usagef("unknown command %q%s", name, DidYouMean(Suggest(name, names)))
}

// flagError rewrites pflag's errors in the programs' words, with the
// nearest flag for an unknown one.
func flagError(cmd *cobra.Command, err error) error {
	msg := err.Error()
	if name, ok := strings.CutPrefix(msg, "unknown flag: --"); ok {
		var names []string
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden {
				names = append(names, f.Name)
			}
		})
		s := Suggest(name, names)
		if s != "" {
			s = "--" + s
		}
		return Usagef("unknown flag --%s%s", name, DidYouMean(s))
	}
	return Usage(err)
}

// normalize writes long flags given with one dash (-db) with two (--db),
// as pflag reads one dash as single-letter flags. Values are left alone: a
// word after a flag that takes a value is that value, even if it starts
// with a dash.
func normalize(root *cobra.Command, args []string) []string {
	cmd, _, err := root.Find(args)
	if err != nil || cmd == nil {
		cmd = root
	}
	lookup := func(name string) *pflag.Flag {
		if f := cmd.Flags().Lookup(name); f != nil {
			return f
		}
		return cmd.InheritedFlags().Lookup(name)
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' && (a[1] < '0' || a[1] > '9') && !shorthand(cmd, a[1:]) {
			a = "-" + a
		}
		out = append(out, a)
		name, isFlag := strings.CutPrefix(a, "--")
		if !isFlag || strings.Contains(name, "=") {
			continue
		}
		if f := lookup(name); f != nil && f.NoOptDefVal == "" && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	return out
}

// shorthand reports whether -s is a single-letter flag of cmd, like -h.
func shorthand(cmd *cobra.Command, s string) bool {
	name, _, _ := strings.Cut(s, "=")
	if len(name) != 1 {
		return false
	}
	return cmd.Flags().ShorthandLookup(name) != nil || cmd.InheritedFlags().ShorthandLookup(name) != nil || name == "h"
}

func init() {
	cobra.AddTemplateFunc("flagGroups", flagGroups)
}

// flagGroups is the help's flag sections: the flags of each Group title in
// the order they were defined, then the rest under "Flags".
func flagGroups(cmd *cobra.Command) string {
	var titles []string
	sets := map[string]*pflag.FlagSet{}
	add := func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		title := "Flags"
		if g := f.Annotations[groupAnnotation]; len(g) == 1 {
			title = g[0]
		}
		if sets[title] == nil {
			titles = append(titles, title)
			sets[title] = pflag.NewFlagSet(title, pflag.ContinueOnError)
			sets[title].SortFlags = false
		}
		sets[title].AddFlag(f)
	}
	cmd.LocalFlags().VisitAll(add)
	cmd.InheritedFlags().VisitAll(add)
	var b strings.Builder
	for _, t := range append(without(titles, "Flags"), "Flags") {
		if sets[t] == nil {
			continue
		}
		fmt.Fprintf(&b, "\n%s:\n%s", t, sets[t].FlagUsagesWrapped(100))
	}
	return b.String()
}

func without(s []string, v string) []string {
	var out []string
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// usageTemplate is cobra's, with the flags in their groups.
var usageTemplate = strings.TrimSpace(`
Usage:{{if and .Runnable (not (index .Annotations "cbioportal_group_only"))}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} <command>{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableFlags}}
{{flagGroups . | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableSubCommands}}

Run '{{.CommandPath}} <command> -h' for a command's help.{{end}}
`) + "\n"
