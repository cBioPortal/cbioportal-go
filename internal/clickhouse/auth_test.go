package clickhouse

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCredentialsFromEnvironment(t *testing.T) {
	var user, key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, key = r.Header.Get("X-ClickHouse-User"), r.Header.Get("X-ClickHouse-Key")
		w.Write([]byte("1\n"))
	}))
	defer srv.Close()

	t.Setenv("CLICKHOUSE_USER", "go_portal")
	t.Setenv("CLICKHOUSE_PASSWORD", "s3cret")
	if _, err := New(srv.URL, "db").Execute("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if user != "go_portal" || key != "s3cret" {
		t.Fatalf("headers = %q / %q", user, key)
	}

	t.Setenv("CLICKHOUSE_USER", "")
	user, key = "unset", "unset"
	if _, err := New(srv.URL, "db").Execute("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if user != "" || key != "" {
		t.Fatalf("unexpected credentials without CLICKHOUSE_USER: %q / %q", user, key)
	}
}

func TestLogComment(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("log_comment")
		w.Write([]byte("1\n"))
	}))
	defer srv.Close()
	t.Setenv("CLICKHOUSE_LOG_COMMENT", "cbioportal-go-api")
	if _, err := New(srv.URL, "db").Execute("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if got != "cbioportal-go-api" {
		t.Errorf("log_comment %q", got)
	}
	t.Setenv("CLICKHOUSE_LOG_COMMENT", "")
	New(srv.URL, "db").Execute("SELECT 1")
	if got != "" {
		t.Errorf("log_comment without the setting: %q", got)
	}
}

// TestDriverRequests checks that the driver's requests authenticate and
// carry settings as the client's own do, over plain HTTP and TLS.
func TestDriverRequests(t *testing.T) {
	for _, tlsServer := range []bool{false, true} {
		var got []*http.Request
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = append(got, r.Clone(r.Context()))
			http.Error(w, "Code: 1. not a server", http.StatusInternalServerError)
		})
		var srv *httptest.Server
		if tlsServer {
			srv = httptest.NewTLSServer(h)
		} else {
			srv = httptest.NewServer(h)
		}
		t.Setenv("CLICKHOUSE_USER", "go_portal")
		t.Setenv("CLICKHOUSE_PASSWORD", "s3cret")
		t.Setenv("CLICKHOUSE_LOG_COMMENT", "api-test")
		c := New(srv.URL, "db")
		if tlsServer {
			c.http.Transport = observedTransport{base: commentTransport{
				base:    authTransport{base: srv.Client().Transport, user: "go_portal", password: "s3cret"},
				comment: "api-test",
			}}
		}
		type row struct {
			N int64 `json:"n"`
		}
		if _, ok, _ := queryNative[row](c, "SELECT 1 AS n", nil); ok {
			t.Fatal("a failing server must send the query to the JSON path")
		}
		srv.Close()
		if len(got) == 0 {
			t.Fatalf("tls=%v: no driver request", tlsServer)
		}
		r := got[0]
		q := r.URL.Query()
		if r.Header.Get("X-ClickHouse-User") != "go_portal" || r.Header.Get("X-ClickHouse-Key") != "s3cret" ||
			r.Header.Get("Authorization") != "" || r.Header.Get("X-ClickHouse-SSL-Certificate-Auth") != "" ||
			q.Get("log_comment") != "api-test" || q.Get("database") != "db" || q.Get("max_query_size") == "" {
			t.Errorf("tls=%v: headers %v, query %v", tlsServer, r.Header, q)
		}
	}
}
