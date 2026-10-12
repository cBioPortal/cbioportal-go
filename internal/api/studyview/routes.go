package studyview

import "net/http"

// router is where the study view registers its endpoints.
type router interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// Routes registers the endpoints of the study view. Each one first gives a
// filter that names no studies every study the caller may read
// (allStudies).
func (s Server) Routes(mux *http.ServeMux) {
	r := allStudiesRouter{mux, s}
	r.HandleFunc("POST /api/clinical-attributes/counts/fetch", s.clinicalAttributeCountsFetch)
	r.HandleFunc("POST /api/filtered-samples/fetch", s.FilteredSamplesFetch)
	r.HandleFunc("POST /api/clinical-data-counts/fetch", s.clinicalDataCountsFetch)
	r.HandleFunc("POST /api/mutated-genes/fetch", s.mutatedGenesFetch)
	r.HandleFunc("POST /api/cna-genes/fetch", s.cnaGenesFetch)
	r.HandleFunc("POST /api/structuralvariant-genes/fetch", s.svGenesFetch)
	r.HandleFunc("POST /api/molecular-profile-sample-counts/fetch", s.molecularProfileSampleCountsFetch)
	r.HandleFunc("POST /api/clinical-data-density-plot/fetch", s.clinicalDataDensityPlotFetch)
	r.HandleFunc("POST /api/clinical-data-violin-plots/fetch", s.clinicalDataViolinPlotsFetch)
	r.HandleFunc("POST /api/generic-assay-data-counts/fetch", s.genericAssayDataCountsFetch)
	r.HandleFunc("POST /api/generic-assay-data-bin-counts/fetch", s.genericAssayDataBinCountsFetch)
	r.HandleFunc("POST /api/structuralvariant-counts/fetch", s.svCountsFetch)
	s.routeStudyView2(r)
	s.routeStudyView3(r)
	s.routeNamespace(r)
	s.routeTreatments(r)
}
