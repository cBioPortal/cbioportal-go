package config

import (
	"os"
	"path/filepath"
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
