package api

import "testing"

func TestPagedEndpoint(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/studies", true},
		{"GET", "/api/studies/acc_tcga/samples", true},
		{"GET", "/api/gene-panels/", true},
		{"GET", "/api/molecular-profiles/x_cna/discrete-copy-number", false},
		{"POST", "/api/molecular-profiles/x_mutations/mutations/fetch", true},
		{"GET", "/api/molecular-profiles/x_mutations/mutations/fetch", false},
		{"POST", "/api/genes/fetch", false},
	} {
		if got := pagedEndpoint(c.method, c.path); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
