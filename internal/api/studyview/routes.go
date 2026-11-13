package studyview

import (
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// router is where the study view registers its endpoints.
type router interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// Routes registers the endpoints of the study view. Each one first gives a
// filter that names no studies every study the caller may read
// (allStudies).
func (s Server) Routes(mux *http.ServeMux) {
	r := allStudiesRouter{mux, s}
	core.Handle(r, "POST /api/clinical-attributes/counts/fetch", s, Server.clinicalAttributeCountsFetch)
	core.Handle(r, "POST /api/filtered-samples/fetch", s, Server.FilteredSamplesFetch)
	core.Handle(r, "POST /api/clinical-data-counts/fetch", s, Server.clinicalDataCountsFetch)
	core.Handle(r, "POST /api/mutated-genes/fetch", s, Server.mutatedGenesFetch)
	core.Handle(r, "POST /api/cna-genes/fetch", s, Server.cnaGenesFetch)
	core.Handle(r, "POST /api/structuralvariant-genes/fetch", s, Server.svGenesFetch)
	core.Handle(r, "POST /api/molecular-profile-sample-counts/fetch", s, Server.molecularProfileSampleCountsFetch)
	core.Handle(r, "POST /api/clinical-data-density-plot/fetch", s, Server.clinicalDataDensityPlotFetch)
	core.Handle(r, "POST /api/clinical-data-violin-plots/fetch", s, Server.clinicalDataViolinPlotsFetch)
	core.Handle(r, "POST /api/generic-assay-data-counts/fetch", s, Server.genericAssayDataCountsFetch)
	core.Handle(r, "POST /api/generic-assay-data-bin-counts/fetch", s, Server.genericAssayDataBinCountsFetch)
	core.Handle(r, "POST /api/structuralvariant-counts/fetch", s, Server.svCountsFetch)
	s.routeStudyView2(r)
	s.routeStudyView3(r)
	s.routeNamespace(r)
	s.routeTreatments(r)
}
