package studies

import "net/http"

// Routes registers the endpoints of the studies, samples, patients, clinical data, sample lists, genes,
// cancer types, resources and study tags.
func (s Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cancer-types", s.cancerTypes)
	mux.HandleFunc("GET /api/studies", s.studies)
	mux.HandleFunc("POST /api/studies/fetch", s.studiesFetch)
	mux.HandleFunc("GET /api/studies/{studyId}", s.study)
	mux.HandleFunc("GET /api/studies/{studyId}/samples", s.samples)
	mux.HandleFunc("POST /api/samples/fetch", s.samplesFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/patients", s.patients)
	mux.HandleFunc("POST /api/patients/fetch", s.patientsFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}", s.patient)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}/samples", s.samplesOfPatient)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}/clinical-data", s.patientClinicalData)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}/clinical-data", s.sampleClinicalData)
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-data", s.clinicalData)
	mux.HandleFunc("POST /api/studies/{studyId}/clinical-data/fetch", s.clinicalDataStudyFetch)
	mux.HandleFunc("POST /api/clinical-attributes/fetch", s.clinicalAttributesFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/sample-lists", s.studySampleLists)
	mux.HandleFunc("GET /api/sample-lists/{id}", s.sampleList)
	mux.HandleFunc("POST /api/sample-lists/fetch", s.sampleListsFetch)
	mux.HandleFunc("POST /api/genes/fetch", s.genesFetch)
	mux.HandleFunc("GET /api/genes/{id}", s.gene)
	mux.HandleFunc("POST /api/clinical-data/fetch", s.clinicalDataFetch)
	s.routeMisc(mux)
	s.routeReference(mux)
	s.routeBulkList(mux)
	s.routeResources(mux)
	s.routeStudyTags(mux)
	s.routeCancerTypes(mux)
}
