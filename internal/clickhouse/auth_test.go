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
