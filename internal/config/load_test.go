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

func TestPropertyPlaceholders(t *testing.T) {
	file := filepath.Join(t.TempDir(), "portal.properties")
	os.WriteFile(file, []byte("redis.host=from-file\nhost=${redis.host}\n"+
		"spring.data.redis.port=${REDIS_SESSION_PORT:6379}\n"), 0o644)
	env := map[string]string{
		"GOOGLE_PLUS_CONSUMER_KEY":    "id-123",
		"GOOGLE_PLUS_CONSUMER_SECRET": "s3cret",
		"SKIN_TITLE":                  "From env (relaxed name)",
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	reg := "spring.security.oauth2.client.registration.google."
	props, err := loadPortalProperties(file, []string{
		"--" + reg + "clientId=${GOOGLE_PLUS_CONSUMER_KEY}",
		"--" + reg + "clientSecret=${GOOGLE_PLUS_CONSUMER_SECRET}",
		"--title=[${skin.title}]",
		"--empty=${UNSET_VAR:}",
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		reg + "clientId":         "id-123",
		reg + "clientSecret":     "s3cret",
		"title":                  "[From env (relaxed name)]",
		"empty":                  "",
		"host":                   "from-file",
		"spring.data.redis.port": "6379",
	}
	for k, v := range want {
		if props[k] != v {
			t.Errorf("%s = %q, want %q", k, props[k], v)
		}
	}
	// Arguments take precedence over the environment and the file.
	props, _ = loadPortalProperties(file, []string{"--redis.host=from-args", "--REDIS_SESSION_PORT=1"}, lookup)
	if props["host"] != "from-args" || props["spring.data.redis.port"] != "1" {
		t.Errorf("host %q port %q, want the arguments' values", props["host"], props["spring.data.redis.port"])
	}

	_, err = loadPortalProperties("", []string{"--" + reg + "clientSecret=${MISSING_SECRET}"}, lookup)
	if err == nil || !strings.Contains(err.Error(), "MISSING_SECRET") || !strings.Contains(err.Error(), "clientSecret") {
		t.Errorf("unset placeholder: %v, want an error naming the property and the placeholder", err)
	}
	if _, err := loadPortalProperties("", []string{"--a=${B"}, lookup); err == nil {
		t.Error("accepted an unterminated placeholder")
	}
}

func TestFrontendConfigOverrides(t *testing.T) {
	cfg := FrontendConfig("/", map[string]string{"skin.title": "Go preview"}, false)
	if cfg["skin_title"] != "Go preview" {
		t.Errorf("skin_title = %v", cfg["skin_title"])
	}
	// Without a properties file the stock defaults apply.
	if cfg["skin_show_about_tab"] != true {
		t.Errorf("skin_show_about_tab = %v, want the stock default true", cfg["skin_show_about_tab"])
	}
}

func TestPropertiesFileReplacesDefaults(t *testing.T) {
	cfg := FrontendConfig("/", map[string]string{"skin.title": "From file"}, true)
	if cfg["skin_title"] != "From file" {
		t.Errorf("skin_title = %v", cfg["skin_title"])
	}
	if cfg["skin_show_about_tab"] == true {
		t.Errorf("skin_show_about_tab = true: stock defaults leaked past a properties file")
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

func TestPropertyEscapes(t *testing.T) {
	props := ParseApplicationProperties(strings.NewReader(`a=one\ntwo
b=tab\there
c=back\\slash
d=café
key\=x=y
e=plain
`))
	want := map[string]string{"a": "one\ntwo", "b": "tab\there", "c": `back\slash`, "d": "café", "key=x": "y", "e": "plain"}
	for k, v := range want {
		if props[k] != v {
			t.Errorf("%q = %q, want %q", k, props[k], v)
		}
	}
}

func TestStudyAvailabilityEnabled(t *testing.T) {
	if v := FrontendConfig("/", nil, false)["study_availability_enabled"]; v != false {
		t.Errorf("default study_availability_enabled = %#v, want false", v)
	}
	cfg := FrontendConfig("/", map[string]string{"study_availability.enabled": "true"}, true)
	if v := cfg["study_availability_enabled"]; v != true {
		t.Errorf("study_availability_enabled = %#v, want true", v)
	}
}

func TestFilePathsRelativeToPropertiesFile(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"frontend.json", "llms.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(`{"a":1}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	props := filepath.Join(dir, "portal.properties")
	if err := os.WriteFile(props, []byte("frontend.config=frontend.json\nllms_txt.location=llms.txt\nquerypage.setsofgenes.location=missing.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadPortalProperties(props, nil, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "frontend.json"); got["frontend.config"] != want {
		t.Errorf("frontend.config = %q, want %q", got["frontend.config"], want)
	}
	if want := filepath.Join(dir, "llms.txt"); got["llms_txt.location"] != want {
		t.Errorf("llms_txt.location = %q, want %q", got["llms_txt.location"], want)
	}
	if got["querypage.setsofgenes.location"] != "missing.json" {
		t.Errorf("a path missing next to the file is left as given, got %q", got["querypage.setsofgenes.location"])
	}
}
