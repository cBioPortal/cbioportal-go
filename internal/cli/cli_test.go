package cli

import "testing"

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
