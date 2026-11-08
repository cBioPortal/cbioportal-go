package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFailClientClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/proxy/x", nil).WithContext(ctx)
	Fail(w, r, context.Canceled)
	if w.Code != StatusClientClosedRequest || w.Body.Len() != 0 {
		t.Errorf("client gone: %d %q, want 499 and no body", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	Fail(w, httptest.NewRequest(http.MethodGet, "/proxy/x", nil), errors.New("boom"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("client still there: %d, want 500", w.Code)
	}
}
