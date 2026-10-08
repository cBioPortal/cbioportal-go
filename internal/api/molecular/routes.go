package molecular

import "net/http"

// Routes registers the endpoints of the molecular profiles and their data.
func (s Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/molecular-profiles", s.molecularProfiles)
	mux.HandleFunc("POST /api/molecular-profiles/fetch", s.molecularProfilesFetch)
	mux.HandleFunc("GET /api/molecular-profiles/{id}", s.molecularProfile)
	mux.HandleFunc("GET /api/studies/{studyId}/molecular-profiles", s.studyMolecularProfiles)
	mux.HandleFunc("GET /api/molecular-profiles/{id}/mutations", s.mutations)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/mutations/fetch", s.mutationsFetchProfile)
	mux.HandleFunc("POST /api/mutations/fetch", s.mutationsFetch)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/molecular-data/fetch", s.molecularDataFetch)
	mux.HandleFunc("GET /api/gene-panels", s.genePanels)
	mux.HandleFunc("POST /api/gene-panels/fetch", s.genePanelsFetch)
	mux.HandleFunc("GET /api/gene-panels/{id}", s.genePanel)
	mux.HandleFunc("GET /api/genesets", s.genesetsList)
	mux.HandleFunc("GET /api/genesets/version", s.genesetVersion)
	mux.HandleFunc("POST /api/geneset-hierarchy/fetch", s.genesetHierarchyFetch)
	mux.HandleFunc("POST /api/genesets/{genesetId}/expression-correlation/fetch", s.genesetExpressionCorrelationFetch)
	mux.HandleFunc("POST /api/copy-number-segments/fetch", s.copyNumberSegmentsFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}/copy-number-segments", s.sampleCopyNumberSegments)
	s.routeGenericAssay(mux)
	s.routeSvData(mux)
	s.routeCoexpression(mux)
	s.routeGenericAssay2(mux)
	s.routeMolecular2(mux)
	s.routeSignificance(mux)
	s.routeCompat(mux)
}
