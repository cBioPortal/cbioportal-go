package api

import (
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

func (s *State) routes(mux *http.ServeMux) {
	s.SetMux(mux)
	// Bootstrap / config
	mux.HandleFunc("GET /config_service", s.configService)
	mux.HandleFunc("GET /api/info", s.info)
	routeAPIDocs(mux, s)
	mux.HandleFunc("GET /api/cancer-types", s.CancerTypes)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	// Catch-all: 404 for unknown /api/ paths, the portal UI for the rest.
	mux.HandleFunc("/", s.CatchAll)

	// Session service + public virtual studies
	s.routeSession(mux)

	// Studies
	mux.HandleFunc("GET /api/studies", s.Studies)
	mux.HandleFunc("POST /api/studies/fetch", s.StudiesFetch)
	mux.HandleFunc("GET /api/studies/{studyId}", s.Study)

	// Samples / patients
	mux.HandleFunc("GET /api/studies/{studyId}/samples", s.Samples)
	mux.HandleFunc("POST /api/samples/fetch", s.SamplesFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/patients", s.Patients)
	mux.HandleFunc("POST /api/patients/fetch", s.PatientsFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}", s.Patient)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}/samples", s.SamplesOfPatient)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}/clinical-data", s.PatientClinicalData)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}/clinical-data", s.SampleClinicalData)

	// Clinical
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-data", s.ClinicalData)
	mux.HandleFunc("POST /api/studies/{studyId}/clinical-data/fetch", s.ClinicalDataStudyFetch)
	mux.HandleFunc("POST /api/clinical-attributes/fetch", s.ClinicalAttributesFetch)
	mux.HandleFunc("POST /api/clinical-attributes/counts/fetch", s.ClinicalAttributeCountsFetch)

	// Molecular profiles
	mux.HandleFunc("GET /api/molecular-profiles", s.MolecularProfiles)
	mux.HandleFunc("POST /api/molecular-profiles/fetch", s.MolecularProfilesFetch)
	mux.HandleFunc("GET /api/molecular-profiles/{id}", s.MolecularProfile)
	mux.HandleFunc("GET /api/studies/{studyId}/molecular-profiles", s.StudyMolecularProfiles)

	// Mutations
	mux.HandleFunc("GET /api/molecular-profiles/{id}/mutations", s.Mutations)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/mutations/fetch", s.MutationsFetchProfile)
	mux.HandleFunc("POST /api/mutations/fetch", s.MutationsFetch)

	// Molecular (CNA/expression/…) data
	mux.HandleFunc("POST /api/molecular-profiles/{id}/molecular-data/fetch", s.MolecularDataFetch)

	// Sample lists (case sets)
	mux.HandleFunc("GET /api/studies/{studyId}/sample-lists", s.StudySampleLists)
	mux.HandleFunc("GET /api/sample-lists/{id}", s.SampleList)
	mux.HandleFunc("POST /api/sample-lists/fetch", s.SampleListsFetch)

	// Genes
	mux.HandleFunc("POST /api/genes/fetch", s.GenesFetch)
	mux.HandleFunc("GET /api/genes/{id}", s.Gene)

	// Gene panels
	mux.HandleFunc("GET /api/gene-panels", s.GenePanels)
	mux.HandleFunc("POST /api/gene-panels/fetch", s.GenePanelsFetch)
	mux.HandleFunc("GET /api/gene-panels/{id}", s.GenePanel)

	// Study-view aggregations
	mux.HandleFunc("POST /api/filtered-samples/fetch", s.FilteredSamplesFetch)
	mux.HandleFunc("POST /api/clinical-data-counts/fetch", s.ClinicalDataCountsFetch)
	mux.HandleFunc("POST /api/mutated-genes/fetch", s.MutatedGenesFetch)
	mux.HandleFunc("POST /api/cna-genes/fetch", s.CNAGenesFetch)
	mux.HandleFunc("POST /api/structuralvariant-genes/fetch", s.SVGenesFetch)
	mux.HandleFunc("POST /api/molecular-profile-sample-counts/fetch", s.MolecularProfileSampleCountsFetch)

	mux.HandleFunc("POST /api/clinical-data/fetch", s.ClinicalDataFetch)

	// Gene sets and copy-number segments.
	mux.HandleFunc("GET /api/genesets", s.GenesetsList)
	mux.HandleFunc("GET /api/genesets/version", s.GenesetVersion)
	mux.HandleFunc("POST /api/geneset-hierarchy/fetch", s.GenesetHierarchyFetch)
	mux.HandleFunc("POST /api/genesets/{genesetId}/expression-correlation/fetch", s.GenesetExpressionCorrelationFetch)
	mux.HandleFunc("POST /api/copy-number-segments/fetch", s.CopyNumberSegmentsFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}/copy-number-segments", s.SampleCopyNumberSegments)

	// Study-view plots and counts.
	mux.HandleFunc("POST /api/clinical-data-density-plot/fetch", s.ClinicalDataDensityPlotFetch)
	mux.HandleFunc("POST /api/clinical-data-violin-plots/fetch", s.ClinicalDataViolinPlotsFetch)
	mux.HandleFunc("POST /api/generic-assay-data-counts/fetch", s.GenericAssayDataCountsFetch)
	mux.HandleFunc("POST /api/generic-assay-data-bin-counts/fetch", s.GenericAssayDataBinCountsFetch)
	mux.HandleFunc("POST /api/structuralvariant-counts/fetch", s.SVCountsFetch)

	// Endpoint groups implemented in separate handler files.
	s.RouteGenericAssay(mux)
	s.RouteStudyView2(mux)
	s.RouteSvData(mux)
	s.RouteEnrichment(mux)
	s.RouteEnrichment2(mux)
	s.RouteMisc(mux)
	s.RouteCoexpression(mux)
	s.RouteSurvival(mux)
	s.RouteStudyView3(mux)
	s.RouteGenericAssay2(mux)
	s.RouteMolecular2(mux)
	s.RouteReference(mux)
	s.RouteBulkList(mux)
	s.RouteResources(mux)
	s.RouteSignificance(mux)
	s.RouteStudyTags(mux)
	s.RouteNamespace(mux)
	s.routeStatus(mux)
	s.RouteCancerTypes(mux)
	mux.HandleFunc("POST /api/custom-driver-annotation-report/fetch", s.customDriverAnnotationReport)
	s.RouteTreatments(mux)
	s.RouteCompat(mux)
	s.routeProxy(mux)
	s.routeExport(mux)
	s.routeDATs(mux)
}

// routeDATs serves the data-access-token endpoints: dat.method=oauth2's, or
// placeholders without it.
func (s *State) routeDATs(mux *http.ServeMux) {
	if s.dat != nil {
		s.routeDataAccessTokens(mux, s.dat)
		return
	}
	// Data-access tokens without dat.method=oauth2: placeholders.
	mux.HandleFunc("GET /api/data-access-token", func(w http.ResponseWriter, r *http.Request) { core.WriteJSON(w, map[string]any{}) })
	mux.HandleFunc("GET /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) { core.WriteJSON(w, []any{}) })
	mux.HandleFunc("POST /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) {
		core.ReadBody(r, nil)
		core.WriteJSON(w, map[string]any{})
	})
	mux.HandleFunc("DELETE /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /api/data-access-tokens/{token}", func(w http.ResponseWriter, r *http.Request) { core.WriteJSON(w, map[string]any{}) })
	mux.HandleFunc("DELETE /api/data-access-tokens/{token}", func(w http.ResponseWriter, r *http.Request) {})
}
