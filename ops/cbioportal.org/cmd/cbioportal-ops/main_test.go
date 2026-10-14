package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"version", "--json"}, 0, `"name": "cbioportal-ops"`},
		{[]string{"preview", "teardwon"}, 2, `unknown command "teardwon" (did you mean teardown?)`},
		{[]string{"prevew"}, 2, "did you mean preview?"},
		{[]string{"preview"}, 2, "teardown"},
		{[]string{"-h"}, 0, "GH_INSTALLATION_ID"},
		{[]string{"preview", "teardown", "0"}, 2, "not a pull request number"},
		{[]string{"preview", "gc", "extra"}, 2, `unexpected argument "extra"`},
		{[]string{"hub", "import-pr", "--dryrun"}, 2, "unknown flag --dryrun"},
		{[]string{"preview", "teardown", "x"}, 2, "not a pull request number"},
		{[]string{"preview", "teardown", "--dry-run"}, 2, "takes a pull request number"},
		// A dry run changes nothing, so it needs no ClickHouse or GitHub.
		{[]string{"preview", "teardown", "12", "--dry-run"}, 0, ""},
		{[]string{"preview", "teardown", "-dry-run", "12"}, 0, ""},
	} {
		var out bytes.Buffer
		code := run(context.Background(), tc.args, &out, &out)
		if code != tc.code || !strings.Contains(out.String(), tc.out) {
			t.Errorf("%q: exit %d, want %d; output %q", tc.args, code, tc.code, out.String())
		}
	}
}
