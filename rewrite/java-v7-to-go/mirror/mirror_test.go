package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

func TestDecide(t *testing.T) {
	cfg := Config{Prefix: "go"}
	idle := Source{Status: "complete", Snapshot: "green 2026-10-08 14:00:00"}
	tests := []struct {
		name      string
		active    string
		target    string
		src       Source
		builtFrom string
		build     bool
		db        string
	}{
		{"new import", "go_blue", "", idle, "blue 2026-10-07 14:00:00", true, "go_green"},
		{"new import, green served", "go_green", "", idle, "", true, "go_blue"},
		{"already built", "go_blue", "", idle, idle.Snapshot, false, "go_blue"},
		{"import running", "go_green", "", Source{Status: "running", Snapshot: "x"}, "", false, "go_green"},
		{"bootstrap", "", "", Source{Status: "running"}, "", true, "go_blue"},
		{"other database served", "something_else", "", idle, idle.Snapshot, true, "go_blue"},
		{"fixed target", "go_blue", "pr42", Source{Status: "running"}, "", true, "pr42"},
	}
	for _, tt := range tests {
		c := cfg
		c.ActiveDB, c.TargetDB = tt.active, tt.target
		p := Decide(c, tt.src, tt.builtFrom)
		if p.Build != tt.build || p.DB != tt.db || p.RawDB != tt.db+"_raw" || p.RefDB != tt.db+"_ref" {
			t.Errorf("%s: %+v, want build=%v into %s", tt.name, p, tt.build, tt.db)
		}
	}
}

func TestCloned(t *testing.T) {
	for table, want := range map[string]bool{
		"mutation": true, "sample_derived": true, "cancer_study": true,
		"users": false, "authorities": false, "data_access_tokens": false,
		"mutation_backup": false, "OAuth_clients": false, "user_sessions": false, "login_audit": false,
		"password_reset": false, "credentials": false, "secret_stuff": false, "api_token_x": false,
	} {
		if got := Cloned(table); got != want {
			t.Errorf("%s: cloned=%v, want %v", table, got, want)
		}
	}
}

// TestLiveMirror clones a scratch "production" database into scratch
// blue/green databases: the source is recorded, an unchanged import is not
// rebuilt, a running import is waited for, and credential tables stay out.
func TestLiveMirror(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	p := fmt.Sprintf("rwtest_mirror_%d", os.Getpid())
	c := ch.New(base, "default")
	exec := func(q string) string {
		t.Helper()
		out, err := c.ExecuteServerwide(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return strings.TrimSpace(out)
	}
	dbs := []string{p + "_mgmt", p + "_prod_blue", p + "_prod_green"}
	for _, color := range []string{"blue", "green"} {
		for _, s := range []string{"", "_raw", "_ref"} {
			dbs = append(dbs, p+"_go_"+color+s)
		}
	}
	drop := func() {
		for _, d := range dbs {
			c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + d + "` SYNC")
		}
	}
	drop()
	t.Cleanup(drop)
	for _, d := range dbs {
		exec("CREATE DATABASE `" + d + "`")
	}
	exec("CREATE TABLE `" + p + "_mgmt`.update_status (portal_database String, current_database_in_production String, update_process_status String, time_of_last_update_process_completion DateTime) ENGINE = MergeTree ORDER BY tuple()")
	exec("INSERT INTO `" + p + "_mgmt`.update_status VALUES ('public', 'green', 'complete', '2026-10-08 14:00:00')")
	for _, tbl := range []string{"sample", "users", "mutation_backup"} {
		exec("CREATE TABLE `" + p + "_prod_green`." + tbl + " (id UInt32) ENGINE = MergeTree ORDER BY id")
		exec("INSERT INTO `" + p + "_prod_green`." + tbl + " VALUES (1), (2)")
	}
	exec("CREATE VIEW `" + p + "_prod_green`.sample_view AS SELECT * FROM `" + p + "_prod_green`.sample")
	// Leftovers of an earlier build in the target color.
	exec("CREATE TABLE `" + p + "_go_green_ref`.old (x UInt8) ENGINE = Memory")
	exec("CREATE VIEW `" + p + "_go_green`.old_view AS SELECT 1")

	builds := 0
	workdir := t.TempDir()
	run := func(active string) Plan {
		t.Helper()
		r := &Runner{
			Cfg: Config{URL: base, Prefix: p + "_go", ActiveDB: active, Workdir: workdir,
				MgmtDB: p + "_mgmt", SrcPrefix: p + "_prod_", PortalDatabase: "public"},
			C: c,
			Build: func(db, rawDB, refDB string) error {
				builds++
				_, err := c.ExecuteServerwide(fmt.Sprintf("CREATE TABLE `%s`.sample_derived ENGINE = MergeTree ORDER BY tuple() AS SELECT * FROM `%s`.sample", db, refDB))
				return err
			},
			Logf: t.Logf,
		}
		plan, err := r.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	state := func() string {
		b, _ := os.ReadFile(filepath.Join(workdir, "state.env"))
		return string(b)
	}

	plan := run(p + "_go_blue")
	if !plan.Build || plan.DB != p+"_go_green" || builds != 1 {
		t.Fatalf("first run: %+v, %d builds", plan, builds)
	}
	tables := exec("SELECT arrayStringConcat(groupArray(name), ',') FROM (SELECT name FROM system.tables WHERE database = '" + p + "_go_green_ref' ORDER BY name)")
	if tables != "_go_mirror_source,sample" {
		t.Errorf("cloned tables: %s", tables)
	}
	if n := exec("SELECT count() FROM system.tables WHERE database = '" + p + "_go_green'"); n != "1" {
		t.Errorf("%s objects in the portal database, want only the new sample_derived", n)
	}
	if s := exec("SELECT any(snapshot) FROM `" + p + "_go_green_ref`._go_mirror_source"); s != "green 2026-10-08 14:00:00" {
		t.Errorf("recorded snapshot %q", s)
	}
	if !strings.Contains(state(), "CBIOPORTAL_DB="+p+"_go_green\n") {
		t.Errorf("state.env: %s", state())
	}

	// Green now serves the same import: nothing to do.
	plan = run(p + "_go_green")
	if plan.Build || builds != 1 || !strings.Contains(state(), "CBIOPORTAL_DB="+p+"_go_green\n") {
		t.Errorf("unchanged import: %+v, %d builds, state %s", plan, builds, state())
	}

	// A new import that is still running is waited for.
	exec("ALTER TABLE `" + p + "_mgmt`.update_status UPDATE update_process_status = 'running', time_of_last_update_process_completion = '2026-10-09 14:00:00' WHERE 1 SETTINGS mutations_sync = 2")
	if plan = run(p + "_go_green"); plan.Build || builds != 1 {
		t.Errorf("running import: %+v", plan)
	}
	exec("ALTER TABLE `" + p + "_mgmt`.update_status UPDATE update_process_status = 'complete' WHERE 1 SETTINGS mutations_sync = 2")
	if plan = run(p + "_go_green"); !plan.Build || plan.DB != p+"_go_blue" || builds != 2 {
		t.Errorf("completed import: %+v", plan)
	}
}
