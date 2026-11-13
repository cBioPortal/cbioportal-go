package studies

import (
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// Routes registers the endpoints of the studies, samples, patients, clinical data, sample lists, genes,
// cancer types, resources and study tags.
func (s Server) Routes(mux *http.ServeMux) {
	core.Handle(mux, "GET /api/cancer-types", s, Server.cancerTypes)
	core.Handle(mux, "GET /api/studies", s, Server.studies)
	core.Handle(mux, "POST /api/studies/fetch", s, Server.studiesFetch)
	core.Handle(mux, "GET /api/studies/{studyId}", s, Server.study)
	core.Handle(mux, "GET /api/studies/{studyId}/samples", s, Server.samples)
	core.Handle(mux, "POST /api/samples/fetch", s, Server.samplesFetch)
	core.Handle(mux, "GET /api/studies/{studyId}/patients", s, Server.patients)
	core.Handle(mux, "POST /api/patients/fetch", s, Server.patientsFetch)
	core.Handle(mux, "GET /api/studies/{studyId}/patients/{patientId}", s, Server.patient)
	core.Handle(mux, "GET /api/studies/{studyId}/patients/{patientId}/samples", s, Server.samplesOfPatient)
	core.Handle(mux, "GET /api/studies/{studyId}/patients/{patientId}/clinical-data", s, Server.patientClinicalData)
	core.Handle(mux, "GET /api/studies/{studyId}/samples/{sampleId}/clinical-data", s, Server.sampleClinicalData)
	core.Handle(mux, "GET /api/studies/{studyId}/clinical-data", s, Server.clinicalData)
	core.Handle(mux, "POST /api/studies/{studyId}/clinical-data/fetch", s, Server.clinicalDataStudyFetch)
	core.Handle(mux, "POST /api/clinical-attributes/fetch", s, Server.clinicalAttributesFetch)
	core.Handle(mux, "GET /api/studies/{studyId}/sample-lists", s, Server.studySampleLists)
	core.Handle(mux, "GET /api/sample-lists/{id}", s, Server.sampleList)
	core.Handle(mux, "POST /api/sample-lists/fetch", s, Server.sampleListsFetch)
	core.Handle(mux, "POST /api/genes/fetch", s, Server.genesFetch)
	core.Handle(mux, "GET /api/genes/{id}", s, Server.gene)
	core.Handle(mux, "POST /api/clinical-data/fetch", s, Server.clinicalDataFetch)
	s.routeMisc(mux)
	s.routeReference(mux)
	s.routeBulkList(mux)
	s.routeResources(mux)
	s.routeResourceTable(mux)
	s.routeStudyTags(mux)
	s.routeCancerTypes(mux)
}
