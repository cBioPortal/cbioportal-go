package clickhouse

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const memoryLimitError = "Code: 241. DB::Exception: Query memory limit exceeded: would use 49.08 MiB " +
	"(attempt to allocate chunk of 4.50 MiB bytes), maximum: 47.68 MiB. (MEMORY_LIMIT_EXCEEDED) (version 25.3.14.14 (official build))"

func TestLimitExceeded(t *testing.T) {
	for msg, want := range map[string]string{
		memoryLimitError: "memory",
		"Code: 241. DB::Exception: Memory limit (for query) exceeded: would use 9.31 GiB. (MEMORY_LIMIT_EXCEEDED)":                       "memory",
		"Code: 241. DB::Exception: (total) memory limit exceeded: would use 30.20 GiB. (MEMORY_LIMIT_EXCEEDED)":                          "",
		"Code: 241. DB::Exception: Memory limit (for user) exceeded: would use 9.31 GiB. (MEMORY_LIMIT_EXCEEDED)":                        "",
		"Code: 159. DB::Exception: Timeout exceeded: elapsed 1006.012926 ms, maximum: 1000 ms. (TIMEOUT_EXCEEDED)":                       "time",
		"Code: 160. DB::Exception: Estimated query execution time (700 seconds) is too long. Maximum: 600. (TOO_SLOW)":                   "time",
		"Code: 60. DB::Exception: Unknown table expression identifier 'sample'. (UNKNOWN_TABLE)":                                         "",
		"clickhouse: query failed mid-stream: Code: 159. DB::Exception: Timeout exceeded: elapsed 1 s, maximum: 1 s. (TIMEOUT_EXCEEDED)": "time",
	} {
		if got := LimitExceeded(errors.New(msg)); got != want {
			t.Errorf("LimitExceeded(%.60q) = %q, want %q", msg, got, want)
		}
	}
	if LimitExceeded(nil) != "" {
		t.Error("nil error")
	}
}

// TestWithSettings checks that a client's settings ride on its requests,
// on the HTTP and the driver path, and that a query stopped at a limit is
// not sent again through the JSON path.
func TestWithSettings(t *testing.T) {
	var got []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Clone(r.Context()))
		http.Error(w, memoryLimitError, http.StatusInternalServerError)
	}))
	defer srv.Close()
	base := New(srv.URL, "db")
	c := base.WithSettings(map[string]string{"max_memory_usage": "1000", "max_execution_time": "5"})
	if len(base.Settings()) != 0 {
		t.Fatalf("WithSettings changed the base client: %v", base.Settings())
	}
	type row struct {
		N int64 `json:"n"`
	}
	check := func(what string, err error, requests int) {
		t.Helper()
		if LimitExceeded(err) != "memory" {
			t.Errorf("%s: error %v, want the memory limit", what, err)
		}
		if len(got) != requests {
			t.Fatalf("%s: %d requests, want %d", what, len(got), requests)
		}
		// The driver's request here is its connection handshake, which runs
		// under its dial timeout; that the query itself carries
		// max_execution_time is checked against a server (bulkLimitsLive).
		for _, r := range got {
			q := r.URL.Query()
			if q.Get("max_memory_usage") != "1000" || q.Get("max_execution_time") == "" || q.Get("database") != "db" {
				t.Errorf("%s: request settings %v", what, q)
			}
		}
		got = nil
	}
	err := QueryEachP(c, "SELECT 1 AS n", nil, func(*row) error { return nil })
	check("QueryEachP", err, 1)
	_, err = QueryP[row](c, "SELECT 1 AS n", nil)
	check("QueryP", err, 1)
	_, err = c.QueryJSONLines("SELECT 1 AS n")
	check("QueryJSONLines", err, 1)
	_, err = c.QueryStream(t.Context(), "SELECT 1 AS n", "RowBinary")
	check("QueryStream", err, 1)

	// A query's own setting wins over the client's.
	c.QueryStreamP(t.Context(), "SELECT 1", "RowBinary", nil)
	c2 := c.WithSettings(map[string]string{"max_memory_usage": "2000"})
	c2.Execute("SELECT 1")
	if q := got[len(got)-1].URL.Query(); q.Get("max_memory_usage") != "2000" || q.Get("max_execution_time") != "5" {
		t.Errorf("derived client settings %v", q)
	}
}

// TestMidStreamException checks that rows streamed before ClickHouse
// reports a failure are not followed by a row decoded from the report.
func TestMidStreamException(t *testing.T) {
	for _, tail := range []string{
		"{\"exception\": \"Code: 159. DB::Exception: Timeout exceeded: elapsed 1 s, maximum: 1 s. (TIMEOUT_EXCEEDED)\"}\n",
		"__exception__\nCode: 159. DB::Exception: Timeout exceeded: elapsed 1 s, maximum: 1 s. (TIMEOUT_EXCEEDED)\n",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("http_write_exception_in_output_format") != "0" {
				t.Errorf("JSON reads must ask for a broken transfer on errors: %v", r.URL.Query())
			}
			w.Write([]byte("{\"n\":1}\n{\"n\":2}\n" + tail))
		}))
		var n int
		err := New(srv.URL, "db").eachJSONLine("SELECT n", nil, func([]byte) error { n++; return nil })
		srv.Close()
		if n != 2 || LimitExceeded(err) != "time" || !strings.Contains(err.Error(), "mid-stream") {
			t.Errorf("%.20q: %d rows, error %v", tail, n, err)
		}
	}
}
