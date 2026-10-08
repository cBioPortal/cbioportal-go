package main

import (
	"bytes"
	"flag"
	"slices"
	"strings"
	"testing"
)

func testFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("db", "", "")
	fs.String("url", "", "")
	fs.Bool("verbose", false, "")
	return fs
}

func TestReorder(t *testing.T) {
	tests := []struct {
		in, want []string
	}{
		{[]string{"dir", "--db", "x"}, []string{"--db", "x", "--", "dir"}},
		{[]string{"--db=x", "dir"}, []string{"--db=x", "--", "dir"}},
		// a boolean flag does not swallow the positional after it
		{[]string{"--verbose", "dir", "--db", "x"}, []string{"--verbose", "--db", "x", "--", "dir"}},
		{[]string{"dir1", "-verbose=false", "dir2"}, []string{"-verbose=false", "--", "dir1", "dir2"}},
		// an unknown flag is kept for fs.Parse to report and takes no value
		{[]string{"--bogus", "dir"}, []string{"--bogus", "--", "dir"}},
		// everything after -- is positional, even if it looks like a flag
		{[]string{"--db", "x", "--", "-dir"}, []string{"--db", "x", "--", "-dir"}},
		// a value flag without a value stays last, so fs.Parse reports it
		{[]string{"dir", "--db"}, []string{"--db"}},
		{[]string{"--db", "x"}, []string{"--db", "x"}},
	}
	for _, tt := range tests {
		if got := reorder(tt.in, testFlagSet()); !slices.Equal(got, tt.want) {
			t.Errorf("reorder(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestReorderParses(t *testing.T) {
	fs := testFlagSet()
	if err := fs.Parse(reorder([]string{"dir", "--verbose", "--db", "raw"}, fs)); err != nil {
		t.Fatal(err)
	}
	if fs.Lookup("db").Value.String() != "raw" || fs.Lookup("verbose").Value.String() != "true" || !slices.Equal(fs.Args(), []string{"dir"}) {
		t.Errorf("db=%s verbose=%s args=%q", fs.Lookup("db").Value, fs.Lookup("verbose").Value, fs.Args())
	}
	if err := testFlagSet().Parse(reorder([]string{"dir", "--db"}, testFlagSet())); err == nil {
		t.Error("a trailing --db without a value parsed")
	}
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		code, out, _ := runCLI(args...)
		if code != 0 {
			t.Errorf("%q: exit %d", args, code)
		}
		for _, c := range commands {
			if !strings.Contains(out, "\n  "+c.name+" ") {
				t.Errorf("%q: overview lacks command %s", args, c.name)
			}
		}
	}
	for _, c := range commands {
		for _, args := range [][]string{{"help", c.name}, {c.name, "-h"}} {
			code, out, _ := runCLI(args...)
			if code != 0 || !strings.HasPrefix(out, "Usage: cbioportal-import "+c.name) || !strings.Contains(out, "Examples:") {
				t.Errorf("%q: exit %d, output:\n%s", args, code, out)
			}
			if c.name != "plan" && c.name != "fetch-datahub" && !strings.Contains(out, "-url endpoint") {
				t.Errorf("%q: usage lacks -url", args)
			}
		}
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		args    []string
		code    int
		message string
	}{
		{nil, 2, "Commands:"},
		{[]string{"frobnicate"}, 2, `cbioportal-import: unknown command "frobnicate"`},
		{[]string{"help", "frobnicate"}, 2, `unknown command "frobnicate"`},
		{[]string{"ingest"}, 2, "cbioportal-import: ingest: missing <study-dir>"},
		{[]string{"ingest", "a", "b"}, 2, `cbioportal-import: ingest: unexpected argument "b"`},
		{[]string{"transform", "--bogus"}, 2, "cbioportal-import: transform: flag provided but not defined: -bogus"},
		{[]string{"ingest", "dir", "--db"}, 2, "flag needs an argument: -db"},
		{[]string{"plan", "/nonexistent-study-dir"}, 1, "cbioportal-import: plan: "},
	}
	for _, tt := range tests {
		code, _, errOut := runCLI(tt.args...)
		if code != tt.code || !strings.Contains(errOut, tt.message) {
			t.Errorf("%q: exit %d, stderr %q; want exit %d containing %q", tt.args, code, errOut, tt.code, tt.message)
		}
	}
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "-version", "--version"} {
		code, out, _ := runCLI(arg)
		if code != 0 || !strings.HasPrefix(out, "cbioportal-import ") {
			t.Errorf("%s: exit %d, %q", arg, code, out)
		}
	}
}
