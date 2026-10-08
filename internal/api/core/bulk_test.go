package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBulkLimitsSettings(t *testing.T) {
	got := DefaultBulkLimits.Settings()
	want := map[string]string{
		"max_memory_usage": "10737418240", "max_bytes_before_external_group_by": "5368709120",
		"max_bytes_before_external_sort": "1073741824", "max_execution_time": "600",
	}
	if len(got) != len(want) {
		t.Errorf("settings %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if s := (BulkLimits{MaxTime: 1500 * time.Millisecond}).Settings(); len(s) != 1 || s["max_execution_time"] != "1.5" {
		t.Errorf("time only: %v", s)
	}
	if s := (BulkLimits{}).Settings(); len(s) != 0 {
		t.Errorf("no limits: %v", s)
	}
}

const limitErr = "clickhouse HTTP 500: Code: 241. DB::Exception: Query memory limit exceeded: would use 10.00 GiB, " +
	"maximum: 10.00 GiB. (MEMORY_LIMIT_EXCEEDED)"

// TestQueryLimitBeforeData checks a query stopped at its limit before the
// first element: a 400 with Java's error body.
func TestQueryLimitBeforeData(t *testing.T) {
	for limit, msg := range map[string]string{
		"memory": limitErr,
		"time":   "Code: 159. DB::Exception: Timeout exceeded: elapsed 600.1 s, maximum: 600 s. (TIMEOUT_EXCEEDED)",
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/molecular-data/fetch", nil)
		NewJSONArray(w, r).Finish(errors.New(msg))
		if w.Code != http.StatusBadRequest || w.Body.String() != `{"message":"`+MsgQueryLimit(limit)+`"}` {
			t.Errorf("%s: %d %s", limit, w.Code, w.Body)
		}
		if !strings.Contains(MsgQueryLimit(limit), "pageSize") {
			t.Errorf("%s: the message does not suggest paging", limit)
		}
	}
	w := httptest.NewRecorder()
	Fail(w, httptest.NewRequest(http.MethodGet, "/api/studies", nil), errors.New("Code: 241. DB::Exception: (total) memory limit exceeded. (MEMORY_LIMIT_EXCEEDED)"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("a server-wide memory limit: %d, want 500", w.Code)
	}
}

// TestQueryLimitMidStream checks a query stopped after the first element:
// the response is aborted, not ended as a shorter list.
func TestQueryLimitMidStream(t *testing.T) {
	w := httptest.NewRecorder()
	a := NewJSONArray(w, httptest.NewRequest(http.MethodPost, "/api/molecular-data/fetch", nil))
	if err := a.Add(map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", v)
		}
		if strings.HasSuffix(w.Body.String(), "]\n") {
			t.Errorf("the array was closed: %s", w.Body)
		}
	}()
	a.Finish(errors.New(limitErr))
}
