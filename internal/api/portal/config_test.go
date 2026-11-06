package portal

import "testing"

func TestPortalVersionOf(t *testing.T) {
	for release, want := range map[string]string{
		"v0.1.0":             "go-0.1.0",
		"v0.1.0-12-g6b38ed6": "go-0.1.0-12-g6b38ed6",
		"":                   "go-dev",
	} {
		if got := portalVersionOf(release); got != want {
			t.Errorf("%q: %s, want %s", release, got, want)
		}
	}
}
