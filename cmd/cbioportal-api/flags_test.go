package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/config"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// effective runs `config --json` with args and returns each setting's
// value and source, and each portal property's source.
func effective(t *testing.T, args []string, e func(string) string) (values, sources, props map[string]string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run(append([]string{"config", "--json"}, args...), &out, &errOut, e); code != 0 {
		t.Fatalf("%q: exit %d: %s", args, code, errOut.String())
	}
	var got struct {
		Settings, PortalProperties []configEntry
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	values, sources, props = map[string]string{}, map[string]string{}, map[string]string{}
	for _, s := range got.Settings {
		values[s.Name], sources[s.Name] = s.Value, s.Source
	}
	for _, p := range got.PortalProperties {
		props[p.Name] = p.Source
	}
	return values, sources, props
}

func TestSplitPortalArgs(t *testing.T) {
	args := []string{
		"--skin.title=My portal", "-db", "cbioportal_e2e", "--quick_search.enabled=true",
		"--port=18113", "-auth-enabled", "--priority_studies=A#x,y", "--frontend-dir", "dist",
		"-skin.footer=f",
	}
	rest, portal := splitPortalArgs(newRootCmd(&settings{}), args)
	if want := []string{"--skin.title=My portal", "--quick_search.enabled=true", "--priority_studies=A#x,y", "--skin.footer=f"}; !slices.Equal(portal, want) {
		t.Errorf("portal args = %q, want %q", portal, want)
	}
	if want := []string{"-db", "cbioportal_e2e", "--port=18113", "-auth-enabled", "--frontend-dir", "dist"}; !slices.Equal(rest, want) {
		t.Errorf("rest = %q, want %q", rest, want)
	}
	values, _, props := effective(t, args, env(nil))
	for k, v := range map[string]string{"db": "cbioportal_e2e", "port": "18113", "auth-enabled": "true", "frontend-dir": "dist"} {
		if values[k] != v {
			t.Errorf("%s = %q, want %q", k, values[k], v)
		}
	}
	if props["skin.title"] != "argument" || props["priority_studies"] != "argument" {
		t.Errorf("portal properties: %v", props)
	}
}

func TestPrecedence(t *testing.T) {
	e := env(map[string]string{"CBIOPORTAL_DB": "from_env", "PORT": "9000", "AUTH_ENABLED": "TRUE"})
	values, sources, _ := effective(t, []string{"--db=from_flag"}, e)
	for k, v := range map[string][2]string{
		"db":             {"from_flag", "flag"},                 // flag > env
		"port":           {"9000", "env $PORT"},                 // env > default
		"auth-enabled":   {"true", "env $AUTH_ENABLED"},         // env, case-insensitive
		"clickhouse-url": {"http://localhost:18123", "default"}, // default
		"raw-db":         {"cbioportal_raw", "default"},         //
		"oncokb-url":     {"https://public.api.oncokb.org/api/v1", "default"},
	} {
		if values[k] != v[0] || sources[k] != v[1] {
			t.Errorf("%s = %q from %q, want %q from %q", k, values[k], sources[k], v[0], v[1])
		}
	}
	values, _, _ = effective(t, []string{"-auth-enabled=false"}, e)
	if values["auth-enabled"] != "false" {
		t.Error("-auth-enabled=false did not override AUTH_ENABLED")
	}
}

func TestBoolDoesNotSwallow(t *testing.T) {
	values, _, props := effective(t, []string{"-auth-enabled", "--skin.title=x"}, env(nil))
	if values["auth-enabled"] != "true" || props["skin.title"] != "argument" {
		t.Errorf("auth-enabled=%s portal=%v", values["auth-enabled"], props)
	}
}

// A portal property that only looks like a flag stays a property.
func TestPropertiesNearFlags(t *testing.T) {
	_, portal := splitPortalArgs(newRootCmd(&settings{}), []string{"--authenticate=false", "--app.name=x", "--dat.method=none"})
	if len(portal) != 3 {
		t.Fatalf("portal args %q", portal)
	}
}

func TestUsageListsEveryOption(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"-h"}, &out, &out, env(nil)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	help := out.String()
	for _, st := range config.AllSettings() {
		want := "$" + st.Env
		if st.Secret {
			want = st.Env // environment only
		}
		if !strings.Contains(help, want) {
			t.Errorf("usage lacks %s", want)
		}
	}
	for _, want := range []string{"--port", "$PORT", "--clickhouse-bulk-max-memory", "$CLICKHOUSE_BULK_MAX_MEMORY",
		"--frontend-dir", "--session-store", "--portal-properties", "--key=value", "Examples:",
		"CLICKHOUSE_PASSWORD", "ONCOKB_TOKEN", "AUTH_JWT_SECRET", "PUBLISHER_API_KEY",
		"CBIOPORTAL_LOGIN_MICROSOFT_CLIENT_SECRET", "CBIOPORTAL_REDIS_PASSWORD",
		"ClickHouse:", "Frontend:", "Sessions and auth:", "Login:"} {
		if !strings.Contains(help, want) {
			t.Errorf("usage lacks %s", want)
		}
	}
}

func TestRunCommands(t *testing.T) {
	e := env(map[string]string{"CBIOPORTAL_LOGIN_GOOGLE_CLIENT_SECRET": "s3cret", "CBIOPORTAL_DB": "envdb"})
	for _, tc := range []struct {
		args       []string
		code       int
		out, notIn string
	}{
		{[]string{"version", "--json"}, 0, `"name": "cbioportal-api"`, ""},
		{[]string{"-version"}, 0, "cbioportal-api ", ""},
		{[]string{"help"}, 0, "Without a\ncommand it serves", ""},
		{[]string{"help", "config"}, 0, "cbioportal-api config [flags]", ""},
		{[]string{"config", "--login=optional", "--login-google-client-id=gid"}, 0, "authenticate", "s3cret"},
		{[]string{"config", "--json", "--spring.data.redis.host=h"}, 0, `"source": "spring.data.redis.host (argument)"`, ""},
		{[]string{"config", "--json"}, 0, `"source": "env $CBIOPORTAL_DB"`, ""},
		{[]string{"--db=x", "config"}, 0, "Settings:", ""},
		{[]string{"confg"}, 2, "did you mean config?", ""},
		{[]string{"--prot=1"}, 2, "did you mean --port?", ""},
		{[]string{"-dbb", "x"}, 2, "did you mean --db?", ""},
		{[]string{"--port"}, 2, "needs an argument", ""},
		{[]string{"stray"}, 2, `unknown command "stray"`, ""},
		{[]string{"config", "--login=optional", "--authenticate=oauth2"}, 2, "set it one way", ""},
		{[]string{"config", "--login=maybe"}, 2, "is not off, optional or required", ""},
		{[]string{"config", "--clickhouse-bulk-max-time=soon"}, 2, "invalid duration", ""},
	} {
		var out bytes.Buffer
		code := run(tc.args, &out, &out, e)
		got := out.String()
		if code != tc.code || !strings.Contains(got, tc.out) || (tc.notIn != "" && strings.Contains(got, tc.notIn)) {
			t.Errorf("%q: exit %d, want %d; output:\n%s", tc.args, code, tc.code, got)
		}
	}
}
