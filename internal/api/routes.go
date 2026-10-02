package api

import "net/http"

func (s *State) routes(mux *http.ServeMux) {
	// Bootstrap / config
	mux.HandleFunc("GET /config_service", s.configService)
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/cancer-types", s.cancerTypes)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	// Catch-all: portal UI at "/", and a body-draining [] for any unmatched
	// endpoint (Genome Nexus /proxy, features not yet implemented).
	mux.HandleFunc("/", s.catchAll)

	// Session service + public virtual studies
	s.routeSession(mux)

	// Studies
	mux.HandleFunc("GET /api/studies", s.studies)
	mux.HandleFunc("POST /api/studies/fetch", s.studiesFetch)
	mux.HandleFunc("GET /api/studies/{studyId}", s.study)

	// Samples / patients
	mux.HandleFunc("GET /api/studies/{studyId}/samples", s.samples)
	mux.HandleFunc("POST /api/samples/fetch", s.samplesFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/patients", s.patients)
	mux.HandleFunc("POST /api/patients/fetch", s.patientsFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}", s.patient)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}/samples", s.samplesOfPatient)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}/clinical-data", s.patientClinicalData)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}/clinical-data", s.sampleClinicalData)

	// Clinical
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-data", s.clinicalData)
	mux.HandleFunc("POST /api/studies/{studyId}/clinical-data/fetch", s.clinicalDataStudyFetch)
	mux.HandleFunc("POST /api/clinical-attributes/fetch", s.clinicalAttributesFetch)
	mux.HandleFunc("POST /api/clinical-attributes/counts/fetch", s.clinicalAttributeCountsFetch)

	// Molecular profiles
	mux.HandleFunc("GET /api/molecular-profiles", s.molecularProfiles)
	mux.HandleFunc("POST /api/molecular-profiles/fetch", s.molecularProfilesFetch)
	mux.HandleFunc("GET /api/molecular-profiles/{id}", s.molecularProfile)
	mux.HandleFunc("GET /api/studies/{studyId}/molecular-profiles", s.studyMolecularProfiles)

	// Mutations
	mux.HandleFunc("GET /api/molecular-profiles/{id}/mutations", s.mutations)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/mutations/fetch", s.mutationsFetchProfile)
	mux.HandleFunc("POST /api/mutations/fetch", s.mutationsFetch)

	// Molecular (CNA/expression/…) data
	mux.HandleFunc("POST /api/molecular-profiles/{id}/molecular-data/fetch", s.molecularDataFetch)

	// Sample lists (case sets)
	mux.HandleFunc("GET /api/studies/{studyId}/sample-lists", s.studySampleLists)
	mux.HandleFunc("GET /api/sample-lists/{id}", s.sampleList)
	mux.HandleFunc("POST /api/sample-lists/fetch", s.sampleListsFetch)

	// Genes
	mux.HandleFunc("POST /api/genes/fetch", s.genesFetch)
	mux.HandleFunc("GET /api/genes/{id}", s.gene)

	// Gene panels
	mux.HandleFunc("GET /api/gene-panels", s.genePanels)
	mux.HandleFunc("POST /api/gene-panels/fetch", s.genePanelsFetch)
	mux.HandleFunc("GET /api/gene-panels/{id}", s.genePanel)

	// Study-view aggregations
	mux.HandleFunc("POST /api/filtered-samples/fetch", s.filteredSamplesFetch)
	mux.HandleFunc("POST /api/clinical-data-counts/fetch", s.clinicalDataCountsFetch)
	mux.HandleFunc("POST /api/mutated-genes/fetch", s.mutatedGenesFetch)
	mux.HandleFunc("POST /api/cna-genes/fetch", s.cnaGenesFetch)
	mux.HandleFunc("POST /api/structuralvariant-genes/fetch", s.svGenesFetch)
	mux.HandleFunc("POST /api/molecular-profile-sample-counts/fetch", s.molecularProfileSampleCountsFetch)

	mux.HandleFunc("POST /api/clinical-data/fetch", s.clinicalDataFetch)

	// Endpoint groups implemented in separate handler files.
	s.routeGenericAssay(mux)
	s.routeStudyView2(mux)
	s.routeSvData(mux)
	s.routeEnrichment(mux)
	s.routeEnrichment2(mux)
	s.routeMisc(mux)
	s.routeCoexpression(mux)
	s.routeSurvival(mux)
	s.routeStudyView3(mux)
	s.routeGenericAssay2(mux)
	s.routeMolecular2(mux)
	s.routeReference(mux)
	s.routeBulkList(mux)
	s.routeResources(mux)
	s.routeSignificance(mux)
	s.routeStudyTags(mux)
	s.routeCancerTypes(mux)
	mux.HandleFunc("POST /api/custom-driver-annotation-report/fetch", s.customDriverAnnotationReport)
	s.routeTreatments(mux)
	s.routeCompat(mux)
	s.routeProxy(mux)
}
