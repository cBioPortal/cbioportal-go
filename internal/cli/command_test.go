package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// testProgram is a small command tree: ingest <dir> with flags, a group
// with one command, and a command that fails.
func testProgram(got *map[string]string) *cobra.Command {
	root := &cobra.Command{Use: "prog"}
	var db string
	var n int
	var asJSON bool
	ingest := &cobra.Command{
		Use:  "ingest <study-dir>",
		Args: ArgsBetween(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			(*got)["dir"], (*got)["db"] = args[0], db
			(*got)["db source"] = Source(cmd.Flags().Lookup("db"))
			(*got)["n source"] = Source(cmd.Flags().Lookup("n"))
			if asJSON {
				(*got)["json"] = "true"
			}
			if n < 0 {
				(*got)["n"] = "negative"
			}
			return nil
		},
	}
	ingest.Flags().StringVar(&db, "db", "cbioportal_raw", "database")
	ingest.Flags().IntVar(&n, "n", 1, "a number")
	ingest.Flags().BoolVar(&asJSON, "json", false, "JSON")
	Env(ingest.Flags(), "db", "CBIOPORTAL_DB")
	Group(ingest.Flags(), "ClickHouse", "db")
	preview := &cobra.Command{Use: "preview", Short: "previews"}
	preview.AddCommand(&cobra.Command{Use: "gc", Short: "remove old previews", Args: ArgsBetween(0, 0),
		RunE: func(*cobra.Command, []string) error { (*got)["gc"] = "ran"; return nil }})
	fail := &cobra.Command{Use: "fail", RunE: func(*cobra.Command, []string) error { return errors.New("boom") }}
	exit3 := &cobra.Command{Use: "exit3", RunE: func(*cobra.Command, []string) error { return Exit(3, errors.New("no data")) }}
	root.AddCommand(ingest, preview, fail, exit3)
	AddVersion(root, "prog")
	return root
}

func TestExecute(t *testing.T) {
	env := map[string]string{"CBIOPORTAL_DB": "envdb"}
	for _, tc := range []struct {
		args []string
		code int
		out  string            // in stdout+stderr
		got  map[string]string // values the command saw
	}{
		{[]string{"ingest", "d", "-db", "x"}, 0, "", map[string]string{"dir": "d", "db": "x", "db source": "flag", "n source": "default"}},
		{[]string{"ingest", "--db=x", "d"}, 0, "", map[string]string{"db": "x"}},
		{[]string{"ingest", "d"}, 0, "", map[string]string{"db": "envdb", "db source": "env $CBIOPORTAL_DB"}},
		{[]string{"ingest", "d", "-json"}, 0, "", map[string]string{"json": "true"}},
		{[]string{"ingest", "d", "-n", "-3"}, 0, "", map[string]string{"n": "negative"}},
		{[]string{"preview", "gc"}, 0, "", map[string]string{"gc": "ran"}},
		{[]string{"ingest"}, 2, "prog: ingest: missing <study-dir>", nil},
		{[]string{"ingest", "a", "b"}, 2, `unexpected argument "b"`, nil},
		{[]string{"ingest", "d", "--dbb=x"}, 2, "unknown flag --dbb (did you mean --db?)", nil},
		{[]string{"ingest", "d", "-dbb", "x"}, 2, "did you mean --db?", nil},
		{[]string{"ingest", "d", "--n=x"}, 2, "invalid argument", nil},
		{[]string{"ingst"}, 2, `unknown command "ingst" (did you mean ingest?)`, nil},
		{[]string{"preview", "g"}, 2, `unknown command "g"`, nil},
		{[]string{"preview"}, 2, "Commands:", nil},
		{[]string{}, 2, "ingest", nil},
		{[]string{"fail"}, 1, "prog: fail: boom", nil},
		{[]string{"version"}, 0, "prog ", nil},
		{[]string{"--version"}, 0, "prog ", nil},
		{[]string{"version", "--json"}, 0, `"name": "prog"`, nil},
		{[]string{"ingest", "-h"}, 0, "ClickHouse:\n      --db string", nil},
		{[]string{"ingest", "-h"}, 0, "($CBIOPORTAL_DB)", nil},
		{[]string{"help", "ingest"}, 0, "prog ingest <study-dir> [flags]", nil},
		{[]string{"help", "ingst"}, 2, `unknown command "ingst" (did you mean ingest?)`, nil},
		{[]string{"help", "preview", "x"}, 2, `unknown command "x"`, nil},
		{[]string{"exit3"}, 3, "prog: exit3: no data", nil},
		{[]string{"-h"}, 0, "ingest      \n  preview", nil},
	} {
		got := map[string]string{}
		var out bytes.Buffer
		code := Execute(testProgram(&got), tc.args, &out, &out, func(k string) string { return env[k] })
		if code != tc.code || !strings.Contains(out.String(), tc.out) {
			t.Errorf("%q: exit %d, want %d; output:\n%s", tc.args, code, tc.code, out.String())
		}
		for k, v := range tc.got {
			if got[k] != v {
				t.Errorf("%q: %s = %q, want %q", tc.args, k, got[k], v)
			}
		}
	}
}

func TestExecuteBadEnvironment(t *testing.T) {
	root := &cobra.Command{Use: "prog", Args: ArgsBetween(0, 0), RunE: func(*cobra.Command, []string) error { return nil }}
	root.Flags().Int("port", 8080, "port")
	Env(root.Flags(), "port", "PORT")
	var out bytes.Buffer
	code := Execute(root, nil, &out, &out, func(k string) string { return map[string]string{"PORT": "eighty"}[k] })
	if code != ExitUsage || !strings.Contains(out.String(), "$PORT") {
		t.Errorf("exit %d, output:\n%s", code, out.String())
	}
}
