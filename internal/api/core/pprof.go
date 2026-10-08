package core

import (
	"log"
	"net/http"
	"net/http/pprof"
	"os"
)

// CBIOPORTAL_PPROF_ADDR (e.g. 127.0.0.1:6060) serves the net/http/pprof
// profiles on their own listener, apart from the API. Unset, nothing listens.
func init() {
	addr := os.Getenv("CBIOPORTAL_PPROF_ADDR")
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() {
		log.Printf("pprof listening on %s", addr)
		log.Print(http.ListenAndServe(addr, mux))
	}()
}
