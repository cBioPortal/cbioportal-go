package main

import (
	"bytes"
	"errors"
	"flag"
	"slices"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseArgsSplitsPortalProperties(t *testing.T) {
	args := []string{
		"--skin.title=My portal", "-db", "cbioportal_e2e", "--quick_search.enabled=true",
		"--port=18113", "-auth-enabled", "--priority_studies=A#x,y", "--frontend-dir", "dist",
	}
	cfg, err := parseArgs(args, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--skin.title=My portal", "--quick_search.enabled=true", "--priority_studies=A#x,y"}
	if !slices.Equal(cfg.portalArgs, want) {
		t.Errorf("portal args = %q, want %q", cfg.portalArgs, want)
	}
	for k, v := range map[string]string{"db": "cbioportal_e2e", "port": "18113", "auth-enabled": "true", "frontend-dir": "dist"} {
		if cfg.values[k] != v {
			t.Errorf("%s = %q, want %q", k, cfg.values[k], v)
		}
	}
}

func TestParseArgsPrecedence(t *testing.T) {
	e := env(map[string]string{"CBIOPORTAL_DB": "from_env", "PORT": "9000", "AUTH_ENABLED": "TRUE"})
	cfg, err := parseArgs([]string{"--db=from_flag"}, e)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"db":             "from_flag",              // flag > env
		"port":           "9000",                   // env > default
		"auth-enabled":   "true",                   // env, case-insensitive
		"clickhouse-url": "http://localhost:18123", // default
		"raw-db":         "cbioportal_raw",
	} {
		if cfg.values[k] != v {
			t.Errorf("%s = %q, want %q", k, cfg.values[k], v)
		}
	}
	cfg, _ = parseArgs([]string{"-auth-enabled=false"}, e)
	if cfg.values["auth-enabled"] != "false" {
		t.Error("-auth-enabled=false did not override AUTH_ENABLED")
	}
}

func TestParseArgsBoolDoesNotSwallow(t *testing.T) {
	cfg, err := parseArgs([]string{"-auth-enabled", "--skin.title=x"}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.values["auth-enabled"] != "true" || !slices.Equal(cfg.portalArgs, []string{"--skin.title=x"}) {
		t.Errorf("auth-enabled=%s portal=%q", cfg.values["auth-enabled"], cfg.portalArgs)
	}
}

func TestParseArgsErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--skin.title"},  // a portal property needs =value
		{"-skin.title=x"}, // portal properties take two dashes
		{"stray"},         // no positional arguments
		{"--port"},        // value missing
		{"--frontend_dir=x", "extra"},
	} {
		if _, err := parseArgs(args, env(nil)); err == nil || errors.Is(err, flag.ErrHelp) {
			t.Errorf("%q: err = %v, want a usage error", args, err)
		}
	}
	for _, args := range [][]string{{"-h"}, {"--help"}, {"--skin.title=x", "-help"}} {
		if _, err := parseArgs(args, env(nil)); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%q: err = %v, want flag.ErrHelp", args, err)
		}
	}
	cfg, err := parseArgs([]string{"-version"}, env(nil))
	if err != nil || !cfg.showVersion {
		t.Errorf("-version: %v", err)
	}
}

func TestUsageListsEveryOption(t *testing.T) {
	var b bytes.Buffer
	printUsage(&b)
	out := b.String()
	for _, o := range allOptions() {
		if !strings.Contains(out, "-"+o.name) || !strings.Contains(out, "$"+o.env) {
			t.Errorf("usage lacks -%s / $%s", o.name, o.env)
		}
	}
	for _, s := range append([]string{"--<key>=<value>", "-version", "Examples:"}, "CLICKHOUSE_PASSWORD", "ONCOKB_TOKEN", "AUTH_JWT_SECRET", "PUBLISHER_API_KEY") {
		if !strings.Contains(out, s) {
			t.Errorf("usage lacks %s", s)
		}
	}
}
