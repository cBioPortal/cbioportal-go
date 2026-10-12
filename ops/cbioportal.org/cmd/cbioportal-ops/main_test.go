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
		{[]string{"preview", "teardwon"}, 2, `unknown command "preview teardwon" (did you mean preview teardown?)`},
		{[]string{"preview", "teardown", "x"}, 2, "not a pull request number"},
		{[]string{"preview", "teardown", "--dry-run"}, 2, "takes a pull request number"},
		// A dry run changes nothing, so it needs no ClickHouse or GitHub.
		{[]string{"preview", "teardown", "12", "--dry-run"}, 0, ""},
	} {
		var out bytes.Buffer
		code := run(context.Background(), tc.args, &out, &out)
		if code != tc.code || !strings.Contains(out.String(), tc.out) {
			t.Errorf("%q: exit %d, want %d; output %q", tc.args, code, tc.code, out.String())
		}
	}
}
