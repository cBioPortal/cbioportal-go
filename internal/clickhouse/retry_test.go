package clickhouse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	assignAlterError = "Code: 517. DB::Exception: Previous mutation query is not finished yet. " +
		"You can retry this error. (CANNOT_ASSIGN_ALTER) (version 26.6.1.2455 (official build))"
	changedTableError = "Code: 341. DB::Exception: Table clinical_sample has been changed by another replica. " +
		"(UNFINISHED) (version 26.6.1.2455 (official build))"
)

// TestExecuteRetriesRefusedAlter checks that a statement ClickHouse refused
// because an earlier ALTER of its table was still being applied, or because
// another replica changed the table meanwhile, runs again, and that other
// errors are returned at once.
func TestExecuteRetriesRefusedAlter(t *testing.T) {
	var calls atomic.Int32
	refusals := int32(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			http.Error(w, assignAlterError, http.StatusInternalServerError)
			return
		case 2:
			http.Error(w, changedTableError, http.StatusInternalServerError)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := New(srv.URL, "db")
	out, err := c.Execute("ALTER TABLE t ADD COLUMN IF NOT EXISTS x String")
	if err != nil || out != "ok" || calls.Load() != refusals+1 {
		t.Errorf("refused twice: %q, %v after %d calls", out, err, calls.Load())
	}

	calls.Store(0)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "Code: 62. DB::Exception: Syntax error. (SYNTAX_ERROR)", http.StatusBadRequest)
	})
	if _, err := c.Execute("ALTER TABLE"); err == nil || !strings.Contains(err.Error(), "SYNTAX_ERROR") || calls.Load() != 1 {
		t.Errorf("other error: %v after %d calls", err, calls.Load())
	}
}
