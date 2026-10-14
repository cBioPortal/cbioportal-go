package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		out  string
	}{
		{nil, 2, "java-v7-to-go"},
		{[]string{"java-v7-to-go"}, 2, "parity"},
		{[]string{"java-v7-to-go", "paritee"}, 2, "did you mean parity?"},
		{[]string{"java-v7"}, 2, "did you mean java-v7-to-go?"},
		{[]string{"java-v7-to-go", "mirror", "-h"}, 0, "TARGET_DB"},
		{[]string{"java-v7-to-go", "parity", "nightly", "-h"}, 0, "--home"},
		{[]string{"version", "--json"}, 0, `"name": "cbioportal-rewrite-test"`},
	} {
		var out bytes.Buffer
		code := run(tc.args, &out, &out)
		if code != tc.code || !strings.Contains(out.String(), tc.out) {
			t.Errorf("%q: exit %d, want %d; output:\n%s", tc.args, code, tc.code, out.String())
		}
	}
}
