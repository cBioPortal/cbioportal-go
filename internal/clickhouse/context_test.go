package clickhouse

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestLiveWithContextCancels: cancelling a WithContext client's context ends
// its running queries on the server, on the driver and on the HTTP path. Both
// reach ClickHouse over HTTP, which stops a query when its connection closes
// only for a read-only user with cancel_http_readonly_queries_on_client_close,
// as the API's production user is set up.
func TestLiveWithContextCancels(t *testing.T) {
	c := liveClient(t).WithSettings(map[string]string{
		"readonly": "2", "cancel_http_readonly_queries_on_client_close": "1",
	})
	// One row after 20 half-second blocks: nothing reaches the client while
	// the query runs.
	const slow = "SELECT sum(sleepEachRow(0.5)) AS n, '%s' AS tag FROM numbers(20) SETTINGS max_block_size = 1"
	type row struct {
		N   uint64 `json:"n"`
		Tag string `json:"tag"`
	}
	for name, run := range map[string]func(*Client, string) error{
		"driver": func(c *Client, sql string) error {
			return QueryEachP(c, sql, nil, func(*row) error { return nil })
		},
		"http": func(c *Client, sql string) error {
			_, err := c.Execute(sql)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			tag := "ctx-cancel-" + name + "-" + time.Now().Format("150405.000")
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(300*time.Millisecond, cancel)
			start := time.Now()
			err := run(c.WithContext(ctx), strings.Replace(slow, "%s", tag, 1))
			if err == nil {
				t.Fatal("query finished despite the cancelled context")
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("query ran %v after its context was cancelled", d)
			}
			// The server stops the query too.
			deadline := time.Now().Add(5 * time.Second)
			for {
				out, err := c.Execute("SELECT count() FROM system.processes WHERE query LIKE '%" + tag + "%' AND query NOT LIKE '%system.processes%' FORMAT TabSeparated")
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(out) == "0" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("query still running on the server")
				}
				time.Sleep(200 * time.Millisecond)
			}
		})
	}
}
