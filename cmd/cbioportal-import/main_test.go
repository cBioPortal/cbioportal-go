package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runCLI(args ...string) (code int, stdout, stderr string) {
	return runEnv(nil, args...)
}

func runEnv(env map[string]string, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut, func(k string) string { return env[k] })
	return code, out.String(), errOut.String()
}

// commands are the program's commands other than help, version and
// completion.
func commands() []*cobra.Command {
	var cmds []*cobra.Command
	for _, c := range newRootCmd().Commands() {
		if c.Name() != "version" && c.Name() != "help" {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		code, out, _ := runCLI(args...)
		if code != 0 || !strings.Contains(out, "CLICKHOUSE_PASSWORD") {
			t.Errorf("%q: exit %d, output:\n%s", args, code, out)
		}
		for _, c := range commands() {
			if !strings.Contains(out, "\n  "+c.Name()+" ") {
				t.Errorf("%q: overview lacks command %s", args, c.Name())
			}
		}
	}
	for _, c := range commands() {
		for _, args := range [][]string{{"help", c.Name()}, {c.Name(), "-h"}} {
			code, out, _ := runCLI(args...)
			if code != 0 || !strings.Contains(out, "\n  cbioportal-import "+c.Name()) || !strings.Contains(out, "Examples:") {
				t.Errorf("%q: exit %d, output:\n%s", args, code, out)
			}
			if c.Name() != "plan" && c.Name() != "fetch-datahub" &&
				(!strings.Contains(out, "--url endpoint") || !strings.Contains(out, "$CLICKHOUSE_URL")) {
				t.Errorf("%q: usage lacks --url", args)
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
		{[]string{"transform", "--bogus"}, 2, "cbioportal-import: transform: unknown flag --bogus"},
		{[]string{"transform", "--rawdb", "x"}, 2, "unknown flag --rawdb (did you mean --raw-db?)"},
		{[]string{"transfrom"}, 2, `unknown command "transfrom" (did you mean transform?)`},
		{[]string{"ingest", "dir", "--db"}, 2, "flag needs an argument: --db"},
		{[]string{"import-study", "dir", "--lock-wait=soon"}, 2, "invalid argument"},
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
	if code, out, _ := runCLI("version", "--json"); code != 0 || !strings.Contains(out, `"name": "cbioportal-import"`) {
		t.Errorf("version --json: exit %d, %q", code, out)
	}
	// load-genesets' own --version is the gene set collection's.
	if code, _, errOut := runCLI("load-genesets", "/nonexistent.gmt", "--version", "v1", "--url", "http://127.0.0.1:1"); code != 1 {
		t.Errorf("load-genesets --version v1: exit %d, %q", code, errOut)
	}
}

// writeStudy writes a minimal study directory and returns its path.
func writeStudy(t *testing.T) string {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "meta_study.txt"), []byte("cancer_study_identifier: s1\ntype_of_cancer: brca\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "meta_clinical_sample.txt"), []byte(
		"cancer_study_identifier: s1\ngenetic_alteration_type: CLINICAL\ndatatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "data_clinical_sample.txt"), []byte("PATIENT_ID\tSAMPLE_ID\nP1\tS1\n"), 0o644)
	return dir
}

func TestPlanJSON(t *testing.T) {
	dir := writeStudy(t)
	// Flags may come before or after the argument, with one or two dashes.
	for _, args := range [][]string{{"plan", dir, "--json"}, {"plan", "-json", dir}, {"plan", "--json=true", dir}} {
		code, out, errOut := runCLI(args...)
		if code != 0 || !strings.Contains(out, `"studyId": "s1"`) || !strings.Contains(out, `"datatype": "SAMPLE_ATTRIBUTES"`) {
			t.Errorf("%q: exit %d, %q %q", args, code, out, errOut)
		}
	}
}

// The ClickHouse endpoint comes from --url, else $CLICKHOUSE_URL; a flag
// written -url x, --url x or --url=x, before or after the argument.
func TestURLFlagAndEnvironment(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.Error(w, "Code: 60. DB::Exception: test server", http.StatusBadRequest)
	}))
	defer srv.Close()
	for _, tc := range []struct {
		env  map[string]string
		args []string
	}{
		{map[string]string{"CLICKHOUSE_URL": srv.URL}, []string{"remove-study", "s1", "--dry-run"}},
		{nil, []string{"remove-study", "s1", "-url", srv.URL, "--dry-run"}},
		{nil, []string{"remove-study", "--url", srv.URL, "s1"}},
		{map[string]string{"CLICKHOUSE_URL": "http://127.0.0.1:9"}, []string{"remove-study", "--url=" + srv.URL, "s1", "--db", "x"}},
	} {
		before := hits
		code, _, errOut := runEnv(tc.env, tc.args...)
		if code != 1 || hits == before {
			t.Errorf("%q (env %v): exit %d, %d requests, stderr %q", tc.args, tc.env, code, hits-before, errOut)
		}
	}
}
