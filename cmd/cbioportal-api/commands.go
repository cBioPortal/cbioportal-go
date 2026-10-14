package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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

func newConfigCmd(s *settings) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "print the effective settings and where each comes from; secrets are shown as (redacted)",
		Args:  cli.ArgsBetween(0, 0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ld, err := load(s)
			if err != nil {
				return err
			}
			return printConfig(cmd.OutOrStdout(), s, ld, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the settings as JSON")
	return cmd
}

func newCheckCmd(s *settings) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "check",
		Short: "check the settings and reach ClickHouse, the sign-in providers and the login session store; " +
			"exit status 1 when a check fails",
		Args: cli.ArgsBetween(0, 0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ld, err := load(s)
			if err != nil {
				return err
			}
			return runCheck(cmd.OutOrStdout(), s, ld, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the checks as JSON")
	return cmd
}

// printConfig prints the effective settings, each with where its value
// came from, and the portal properties; credentials are redacted.
func printConfig(w io.Writer, s *settings, ld *loaded, jsonOut bool) error {
	var out struct {
		Settings         []configEntry `json:"settings"`
		PortalProperties []configEntry `json:"portalProperties"`
	}
	login := s.loginValues()
	s.flags.VisitAll(func(f *pflag.Flag) {
		v, src := f.Value.String(), cli.Source(f)
		if _, isLogin := s.login[f.Name]; isLogin && (src == "" || src == "default") {
			// Unset under go's name: it may be set by its Java name.
			st, _ := config.LookupSetting(f.Name)
			if k := config.JavaName(st, login); k != "" {
				if pv, ok := ld.props[k]; ok {
					v, src = pv, k+" ("+ld.propSources[k]+")"
				}
			}
		}
		if src == "" {
			return
		}
		out.Settings = append(out.Settings, configEntry{f.Name, config.Redact(f.Name, v), src})
	})
	for _, st := range config.AllSettings() {
		if v := s.getenv(st.Env); st.Secret && v != "" {
			out.Settings = append(out.Settings, configEntry{st.Name, config.Redact(st.Name, v), "env $" + st.Env})
		}
	}
	for _, k := range slices.Sorted(maps.Keys(ld.props)) {
		out.PortalProperties = append(out.PortalProperties, configEntry{k, config.Redact(k, ld.props[k]), ld.propSources[k]})
	}
	if jsonOut {
		return cli.WriteJSON(w, out)
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
	return tw.Flush()
}

// runCheck checks the settings and reaches every service they name:
// ClickHouse and its three databases, and the login's providers and
// session store. It fails (exit status 1) when any check fails.
func runCheck(stdout io.Writer, s *settings, ld *loaded, jsonOut bool) error {
	var checks []auth.CheckResult
	add := func(name string, err error, detail string) {
		r := auth.CheckResult{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			r.Detail = err.Error()
		}
		checks = append(checks, r)
	}
	state, err := newState(s, ld)
	add("settings", err, "valid")
	if err == nil {
		c := ch.New(s.clickhouseURL, s.db)
		type row struct {
			V string `json:"v"`
		}
		ver, err := ch.Query[row](c, "SELECT version() AS v")
		detail := ""
		if err == nil && len(ver) == 1 {
			detail = fmt.Sprintf("%s, ClickHouse %s", s.clickhouseURL, ver[0].V)
		}
		add("clickhouse", err, detail)
		if err == nil {
			for _, db := range []struct{ flag, name string }{{"db", s.db}, {"raw-db", s.rawDB}, {"ref-db", s.refDB}} {
				type count struct {
					N uint64 `json:"n"`
				}
				n, err := ch.QueryP[count](c, "SELECT count() AS n FROM system.tables WHERE database = {db:String}",
					ch.Params{"db": db.name})
				if err == nil && (len(n) != 1 || n[0].N == 0) {
					err = fmt.Errorf("database %s has no tables (--%s)", db.name, db.flag)
				}
				detail := ""
				if err == nil {
					detail = fmt.Sprintf("%s: %d tables", db.name, n[0].N)
				}
				add("clickhouse --"+db.flag, err, detail)
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
		return cli.ErrReported
	}
	return nil
}
