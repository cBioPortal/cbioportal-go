package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPortalProperties(t *testing.T) {
	file := filepath.Join(t.TempDir(), "portal.properties")
	os.WriteFile(file, []byte("# comment\nskin.title=From file\nskin.blurb=Hello\n"), 0o644)

	props, err := LoadPortalProperties(file, []string{"--skin.title=From args", "--skin.footer=", "--priority_studies=A#x,y;B#z"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"skin.title": "From args", "skin.blurb": "Hello", "skin.footer": "", "priority_studies": "A#x,y;B#z"}
	for k, v := range want {
		if props[k] != v {
			t.Errorf("%s = %q, want %q", k, props[k], v)
		}
	}
	if _, err := LoadPortalProperties("", []string{"skin.title=x"}); err == nil {
		t.Error("accepted an argument without --")
	}
	if _, err := LoadPortalProperties("", []string{"--skin.title"}); err == nil {
		t.Error("accepted an argument without a value")
	}
}

func TestFrontendConfigOverrides(t *testing.T) {
	cfg := FrontendConfig("/", map[string]string{"skin.title": "Go preview"})
	if cfg["skin_title"] != "Go preview" {
		t.Errorf("skin_title = %v", cfg["skin_title"])
	}
}

func TestClassifyProperties(t *testing.T) {
	props := map[string]string{
		"skin.title":                 "x",
		"priority_studies":           "y",
		"spring.datasource.password": "secret",
		"db.user":                    "cbio",
	}
	used, ignored := ClassifyProperties(props)
	if strings.Join(used, ",") != "priority_studies,skin.title" {
		t.Errorf("used = %v", used)
	}
	if strings.Join(ignored, ",") != "db.user,spring.datasource.password" {
		t.Errorf("ignored = %v", ignored)
	}

	summary := PropertiesSummary(props, 1)
	want := "portal properties: 2 used (priority_studies, … 1 more), 2 ignored (not used by this server: db.user, … 1 more)"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
	if strings.Contains(PropertiesSummary(props, 10), "secret") {
		t.Error("summary leaks a property value")
	}
	if got := PropertiesSummary(nil, 10); got != "portal properties: none set (built-in defaults)" {
		t.Errorf("empty summary = %q", got)
	}
}
