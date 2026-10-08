package portal

import "net/http"

// Routes registers the portal's endpoints.
func (s Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /config_service", s.ConfigService)
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("POST /api/custom-driver-annotation-report/fetch", s.customDriverAnnotationReport)
	s.routeStatus(mux)
}
