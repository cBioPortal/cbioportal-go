package molecular

import (
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// Routes registers the endpoints of the molecular profiles and their data.
func (s Server) Routes(mux *http.ServeMux) {
	core.Handle(mux, "GET /api/molecular-profiles", s, Server.molecularProfiles)
	core.Handle(mux, "POST /api/molecular-profiles/fetch", s, Server.molecularProfilesFetch)
	core.Handle(mux, "GET /api/molecular-profiles/{id}", s, Server.molecularProfile)
	core.Handle(mux, "GET /api/studies/{studyId}/molecular-profiles", s, Server.studyMolecularProfiles)
	core.Handle(mux, "GET /api/molecular-profiles/{id}/mutations", s.Bulk(), Server.mutations)
	core.Handle(mux, "POST /api/molecular-profiles/{id}/mutations/fetch", s.Bulk(), Server.mutationsFetchProfile)
	core.Handle(mux, "POST /api/mutations/fetch", s.Bulk(), Server.mutationsFetch)
	core.Handle(mux, "POST /api/molecular-profiles/{id}/molecular-data/fetch", s.Bulk(), Server.molecularDataFetch)
	core.Handle(mux, "GET /api/gene-panels", s, Server.genePanels)
	core.Handle(mux, "POST /api/gene-panels/fetch", s, Server.genePanelsFetch)
	core.Handle(mux, "GET /api/gene-panels/{id}", s, Server.genePanel)
	core.Handle(mux, "GET /api/genesets", s, Server.genesetsList)
	core.Handle(mux, "GET /api/genesets/version", s, Server.genesetVersion)
	core.Handle(mux, "POST /api/geneset-hierarchy/fetch", s, Server.genesetHierarchyFetch)
	core.Handle(mux, "POST /api/genesets/{genesetId}/expression-correlation/fetch", s, Server.genesetExpressionCorrelationFetch)
	core.Handle(mux, "POST /api/copy-number-segments/fetch", s.Bulk(), Server.copyNumberSegmentsFetch)
	core.Handle(mux, "GET /api/studies/{studyId}/samples/{sampleId}/copy-number-segments", s.Bulk(), Server.sampleCopyNumberSegments)
	s.routeGenericAssay(mux)
	s.routeSvData(mux)
	s.routeCoexpression(mux)
	s.routeGenericAssay2(mux)
	s.routeMolecular2(mux)
	s.routeSignificance(mux)
	s.routeCompat(mux)
}
