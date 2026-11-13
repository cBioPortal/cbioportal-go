package core

import "net/http"

// Mux is where handlers are registered: an http.ServeMux, or a router that
// wraps one.
type Mux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// RequestScoped is a handler package's Server, which ForRequest copies for
// one request (its ClickHouse client bound to the request's context).
type RequestScoped[S any] interface {
	ForRequest(r *http.Request) S
}

// Handle registers h for pattern, serving each request with
// s.ForRequest(r): when the client goes away, the request's ClickHouse
// queries stop instead of running to their time limit.
func Handle[S RequestScoped[S]](mux Mux, pattern string, s S, h func(S, http.ResponseWriter, *http.Request)) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		h(s.ForRequest(r), w, r)
	})
}
