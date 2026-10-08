package core

import (
	"strings"
	"testing"
)

func TestWithSiteBanner(t *testing.T) {
	page := []byte(`<html><head></head><body class="x"><div id="reactRoot"></div></body></html>`)
	if got := withSiteBanner(page, "", ""); string(got) != string(page) {
		t.Fatalf("banner added without text: %s", got)
	}
	got := string(withSiteBanner(page, `Experimental <preview> & co`, "https://www.cbioportal.org"))
	want := `<body class="x"><div id="site-banner"`
	if !strings.Contains(got, want) {
		t.Fatalf("banner not first in body: %s", got)
	}
	if !strings.Contains(got, "Experimental &lt;preview&gt; &amp; co") || strings.Contains(got, "<preview>") {
		t.Errorf("text not escaped: %s", got)
	}
	if !strings.Contains(got, `<a href="https://www.cbioportal.org"`) {
		t.Errorf("link missing: %s", got)
	}
	if strings.Index(got, "site-banner") > strings.Index(got, "reactRoot") {
		t.Errorf("banner after reactRoot: %s", got)
	}
}
