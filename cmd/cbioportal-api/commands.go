package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/auth"
	"github.com/cbioportal/cbioportal-go/internal/cli"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

// configEntry is one effective setting or portal property in `config`.
type configEntry struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// printConfig prints the effective settings, each with where its value
// came from, and the portal properties; credentials are redacted.
func printConfig(w io.Writer, cfg *settings, ld *loaded, jsonOut bool) int {
	var out struct {
		Settings         []configEntry `json:"settings"`
		PortalProperties []configEntry `json:"portalProperties"`
	}
	for _, o := range allOptions() {
		v, src := cfg.values[o.name], cfg.sources[o.name]
		if o.setting && src == "" {
			// Unset under go's name: it may be set by its Java name.
			st, _ := config.LookupSetting(o.name)
			if k := config.JavaName(st, cfg.goSettings()); k != "" {
				if pv, ok := ld.props[k]; ok {
					v, src = pv, k+" ("+ld.propSources[k]+")"
				}
			}
		}
		if src == "" {
			continue
		}
		out.Settings = append(out.Settings, configEntry{o.name, config.Redact(o.name, v), src})
	}
	for _, st := range config.AllSettings() {
		if src := cfg.sources[st.Name]; st.Secret && src != "" {
			out.Settings = append(out.Settings, configEntry{st.Name, config.Redact(st.Name, cfg.values[st.Name]), src})
		}
	}
	for _, k := range slices.Sorted(maps.Keys(ld.props)) {
		out.PortalProperties = append(out.PortalProperties, configEntry{k, config.Redact(k, ld.props[k]), ld.propSources[k]})
	}
	if jsonOut {
		cli.WriteJSON(w, out)
		return cli.ExitOK
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Settings:")
	for _, e := range out.Settings {
		fmt.Fprintf(tw, "  --%s\t%s\t%s\n", e.Name, e.Value, e.Source)
	}
	fmt.Fprintln(tw, "\nPortal properties:")
	if len(out.PortalProperties) == 0 {
		fmt.Fprintln(tw, "  none (built-in defaults)")
	}
	for _, e := range out.PortalProperties {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", e.Name, e.Value, e.Source)
	}
	tw.Flush()
	return cli.ExitOK
}

// runCheck checks the settings and reaches every service they name:
// ClickHouse and its three databases, and the login's providers and
// session store. The exit status is 1 when any check fails.
func runCheck(stdout, stderr io.Writer, cfg *settings, ld *loaded, jsonOut bool) int {
	var checks []auth.CheckResult
	add := func(name string, err error, detail string) {
		r := auth.CheckResult{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			r.Detail = err.Error()
		}
		checks = append(checks, r)
	}
	state, err := newState(cfg, ld)
	add("settings", err, "valid")
	if err == nil {
		v := cfg.values
		c := ch.New(v["clickhouse-url"], v["db"])
		type row struct {
			V string `json:"v"`
		}
		ver, err := ch.Query[row](c, "SELECT version() AS v")
		detail := ""
		if err == nil && len(ver) == 1 {
			detail = fmt.Sprintf("%s, ClickHouse %s", v["clickhouse-url"], ver[0].V)
		}
		add("clickhouse", err, detail)
		if err == nil {
			for _, db := range []string{"db", "raw-db", "ref-db"} {
				type count struct {
					N uint64 `json:"n"`
				}
				n, err := ch.QueryP[count](c, "SELECT count() AS n FROM system.tables WHERE database = {db:String}",
					ch.Params{"db": v[db]})
				if err == nil && (len(n) != 1 || n[0].N == 0) {
					err = fmt.Errorf("database %s has no tables (--%s)", v[db], db)
				}
				detail := ""
				if err == nil {
					detail = fmt.Sprintf("%s: %d tables", v[db], n[0].N)
				}
				add("clickhouse --"+db, err, detail)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		checks = append(checks, state.CheckLogin(ctx)...)
	}
	ok := true
	for _, c := range checks {
		ok = ok && c.OK
	}
	if jsonOut {
		cli.WriteJSON(stdout, struct {
			OK     bool               `json:"ok"`
			Checks []auth.CheckResult `json:"checks"`
		}{ok, checks})
	} else {
		for _, c := range checks {
			mark := "ok  "
			if !c.OK {
				mark = "FAIL"
			}
			fmt.Fprintf(stdout, "%s  %s: %s\n", mark, c.Name, c.Detail)
		}
	}
	if !ok {
		return cli.ExitFailure
	}
	return cli.ExitOK
}
