package studyview

import "net/http"

// Routes registers the endpoints of the study view.
func (s Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/clinical-attributes/counts/fetch", s.clinicalAttributeCountsFetch)
	mux.HandleFunc("POST /api/filtered-samples/fetch", s.FilteredSamplesFetch)
	mux.HandleFunc("POST /api/clinical-data-counts/fetch", s.clinicalDataCountsFetch)
	mux.HandleFunc("POST /api/mutated-genes/fetch", s.mutatedGenesFetch)
	mux.HandleFunc("POST /api/cna-genes/fetch", s.cnaGenesFetch)
	mux.HandleFunc("POST /api/structuralvariant-genes/fetch", s.svGenesFetch)
	mux.HandleFunc("POST /api/molecular-profile-sample-counts/fetch", s.molecularProfileSampleCountsFetch)
	mux.HandleFunc("POST /api/clinical-data-density-plot/fetch", s.clinicalDataDensityPlotFetch)
	mux.HandleFunc("POST /api/clinical-data-violin-plots/fetch", s.clinicalDataViolinPlotsFetch)
	mux.HandleFunc("POST /api/generic-assay-data-counts/fetch", s.genericAssayDataCountsFetch)
	mux.HandleFunc("POST /api/generic-assay-data-bin-counts/fetch", s.genericAssayDataBinCountsFetch)
	mux.HandleFunc("POST /api/structuralvariant-counts/fetch", s.svCountsFetch)
	s.routeStudyView2(mux)
	s.routeStudyView3(mux)
	s.routeNamespace(mux)
	s.routeTreatments(mux)
}
