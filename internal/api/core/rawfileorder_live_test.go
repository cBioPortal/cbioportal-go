package core

import (
	"fmt"
	"os"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// TestLiveRawFileOrder runs the raw file-order expression against each shape
// a raw staging source takes, when CLICKHOUSE_TEST_URL (e.g.
// http://localhost:18123) is set: ingested tables with and without the order
// columns, the from-prod views with their stand-ins, and a view without any
// (a mirror built before the stand-ins existed).
func TestLiveRawFileOrder(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	rawDB := fmt.Sprintf("cbioportal_go_test_raw_%d", os.Getpid())
	admin := ch.New(base, "default")
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Execute(sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("CREATE DATABASE " + rawDB)
	t.Cleanup(func() { _, _ = admin.Execute("DROP DATABASE IF EXISTS " + rawDB) })
	exec("CREATE TABLE " + rawDB + ".staged (study_id String, value String, file_num UInt32, row_num UInt64) " +
		"ENGINE = MergeTree ORDER BY study_id")
	exec("INSERT INTO " + rawDB + ".staged VALUES ('s', 'b', 0, 2), ('s', 'a', 0, 1)")
	exec("CREATE TABLE " + rawDB + ".staged_unnumbered (study_id String, value String) ENGINE = MergeTree ORDER BY tuple()")
	exec("INSERT INTO " + rawDB + ".staged_unnumbered VALUES ('s', 'a'), ('s', 'b')")
	exec("CREATE VIEW " + rawDB + ".prod_view AS SELECT 's' AS study_id, 'a' AS value, " +
		"toUInt32(0) AS file_num, toUInt64(0) AS row_num, '' AS _part, toUInt64(0) AS _part_offset")
	exec("CREATE VIEW " + rawDB + ".bare_view AS SELECT 's' AS study_id, 'a' AS value")

	c := ch.New(base, "default")
	c.SetDatabaseAliases(rawDB, "")
	s := &State{CH: c}
	type row struct {
		Value string `json:"value"`
	}
	for table, want := range map[string]string{"staged": "ab", "staged_unnumbered": "ab", "prod_view": "a", "bare_view": "a"} {
		rows, err := ch.Query[row](c, fmt.Sprintf(
			"SELECT value FROM (SELECT *, %s AS pos_ FROM cbioportal_raw.%s) ORDER BY pos_", s.RawFileOrder(table, ""), table))
		if err != nil {
			t.Errorf("%s: %v", table, err)
			continue
		}
		got := ""
		for _, r := range rows {
			got += r.Value
		}
		if got != want {
			t.Errorf("%s: values in file order %q, want %q", table, got, want)
		}
	}
}
