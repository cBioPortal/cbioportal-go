package cli

import (
	"slices"
	"testing"
)

func TestSuggest(t *testing.T) {
	names := []string{"db", "raw-db", "ref-db", "port", "clickhouse-url", "config", "check"}
	for in, want := range map[string]string{
		"prot": "port", "dbb": "db", "confg": "config", "chek": "check", "clickhouse": "clickhouse-url",
		"RAW-DB": "raw-db", "xyz": "", "quick_search.enabled": "", "zz": "", "x": "",
	} {
		if got := Suggest(in, names); got != want {
			t.Errorf("Suggest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSONFlag(t *testing.T) {
	on, rest := JSONFlag([]string{"--db=x", "--json", "a", "--", "--json"})
	if !on || !slices.Equal(rest, []string{"--db=x", "a", "--", "--json"}) {
		t.Errorf("on=%v rest=%q", on, rest)
	}
	if on, _ := JSONFlag([]string{"--json", "--json=false"}); on {
		t.Error("--json=false did not turn it off")
	}
}
