package api

import (
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/apidocs"
	"github.com/cbioportal/cbioportal-go/internal/api/comparison"
	"github.com/cbioportal/cbioportal-go/internal/api/export"
	"github.com/cbioportal/cbioportal-go/internal/api/molecular"
	"github.com/cbioportal/cbioportal-go/internal/api/portal"
	"github.com/cbioportal/cbioportal-go/internal/api/session"
	"github.com/cbioportal/cbioportal-go/internal/api/studies"
	"github.com/cbioportal/cbioportal-go/internal/api/studyview"
)

// routes registers every endpoint on mux: each area package's, the health
// check, the OncoKB proxy, the data-access-token endpoints and the
// catch-all.
func (s *State) routes(mux *http.ServeMux) {
	s.SetMux(mux)
	portal.New(s.State).Routes(mux)
	apidocs.New(s.State).Routes(mux)
	session.New(s.State).Routes(mux)
	studies.New(s.State).Routes(mux)
	molecular.New(s.State).Routes(mux)
	studyview.New(s.State).Routes(mux)
	comparison.New(s.State).Routes(mux)
	export.New(s.State).Routes(mux)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	s.routeProxy(mux)
	s.auth.RouteDataAccessTokens(s.State, mux)
	// Catch-all: 404 for unknown /api/ paths, the portal UI for the rest.
	mux.HandleFunc("/", s.CatchAll)
}
