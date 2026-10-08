package comparison

import "net/http"

// Routes registers the endpoints of the group comparison and clinical event.
func (s Server) Routes(mux *http.ServeMux) {
	s.routeEnrichment(mux)
	s.routeEnrichment2(mux)
	s.RouteSurvival(mux)
}
