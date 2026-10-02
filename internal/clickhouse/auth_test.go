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
