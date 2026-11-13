package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type scopedServer struct{ ctx context.Context }

func (s scopedServer) ForRequest(r *http.Request) scopedServer { return scopedServer{r.Context()} }

type ctxKey struct{}

// TestHandleScopesServerToRequest: each request is served by the Server
// ForRequest makes from that request.
func TestHandleScopesServerToRequest(t *testing.T) {
	mux := http.NewServeMux()
	var got context.Context
	Handle(mux, "GET /x", scopedServer{}, func(s scopedServer, w http.ResponseWriter, r *http.Request) {
		got = s.ctx
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, "this request"))
	mux.ServeHTTP(httptest.NewRecorder(), r)
	if got == nil || got.Value(ctxKey{}) != "this request" {
		t.Fatalf("handler's server context %v, want the request's", got)
	}
}
