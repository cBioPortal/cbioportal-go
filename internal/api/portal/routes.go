package portal

import "net/http"

// Routes registers the portal's endpoints, and the frontend configuration
// as what the portal page embeds.
func (s Server) Routes(mux *http.ServeMux) {
	s.PageConfig = s.Config
	mux.HandleFunc("GET /config_service", s.ConfigService)
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("POST /api/custom-driver-annotation-report/fetch", s.customDriverAnnotationReport)
	s.routeStatus(mux)
}
