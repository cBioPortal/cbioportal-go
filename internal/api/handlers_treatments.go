package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeTreatments registers the study-view treatment and clinical-event-type
// charts, all computed from clinical_event_data_derived over the filtered
// samples/patients.
func (s *State) routeTreatments(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/treatments/patient-counts/fetch", s.patientTreatmentCountsFetch)
	mux.HandleFunc("POST /api/treatments/sample-counts/fetch", s.sampleTreatmentCountsFetch)
	mux.HandleFunc("POST /api/treatments/patient", s.treatmentsPatient)
	mux.HandleFunc("POST /api/treatments/sample", s.treatmentsSample)
	mux.HandleFunc("POST /api/clinical-event-type-counts/fetch", s.clinicalEventTypeCountsFetch)
	mux.HandleFunc("POST /api/treatments/display-patient", s.treatmentsDisplayPatient)
	mux.HandleFunc("POST /api/treatments/display-sample", s.treatmentsDisplaySample)
}

// treatmentTierKey maps the tier query param (ClinicalEventKeyCode) to the
// clinical event key it reads; Agent is the default.
func treatmentTierKey(r *http.Request) string {
	switch r.URL.Query().Get("tier") {
	case "AgentClass":
		return "AGENT_CLASS"
	case "AgentTarget":
		return "AGENT_TARGET"
	}
	return "AGENT"
}

// studiesWithTreatments returns the studies (of studyIDs) having treatment
// events with the given key.
func (s *State) studiesWithTreatments(studyIDs []string, key string) []string {
	type row struct {
		Study string `json:"study"`
	}
	rows, _ := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT DISTINCT cancer_study_identifier AS study FROM clinical_event_data_derived "+
			"WHERE cancer_study_identifier IN (%s) AND lower(event_type) = 'treatment' AND lower(key) = lower('%s')",
		orEmpty(studyIDs), sqlLit(key)))
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Study
	}
	return out
}

// treatmentsDisplayPatient serves POST /api/treatments/display-patient (body:
// study ids): whether any of the studies has treatment data for the tier.
func (s *State) treatmentsDisplayPatient(w http.ResponseWriter, r *http.Request) {
	var studyIDs []string
	readBody(r, &studyIDs)
	writeJSON(w, len(s.studiesWithTreatments(studyIDs, treatmentTierKey(r))) > 0)
}

// treatmentsDisplaySample serves POST /api/treatments/display-sample (body:
// study ids): whether the studies with treatment data also have sample
// acquisition timeline events.
func (s *State) treatmentsDisplaySample(w http.ResponseWriter, r *http.Request) {
	var studyIDs []string
	readBody(r, &studyIDs)
	treated := s.studiesWithTreatments(studyIDs, treatmentTierKey(r))
	if len(treated) == 0 {
		writeJSON(w, false)
		return
	}
	ok, err := s.existsIn(fmt.Sprintf(
		"SELECT count() AS n FROM clinical_event_data_derived WHERE cancer_study_identifier IN (%s) "+
			"AND lower(key) = 'sample_id' AND (event_type ILIKE 'Sample Acquisition' OR event_type ILIKE 'SPECIMEN')",
		inList(treated)))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, ok)
}

type PatientTreatment struct {
	Treatment string `json:"treatment"`
	Count     int    `json:"count"`
}

type PatientTreatmentReport struct {
	TotalPatients     int                `json:"totalPatients"`
	TotalSamples      int                `json:"totalSamples"`
	PatientTreatments []PatientTreatment `json:"patientTreatments"`
}

type ClinicalEventSample struct {
	PatientID string `json:"patientId"`
	SampleID  string `json:"sampleId"`
	StudyID   string `json:"studyId"`
	TimeTaken int    `json:"timeTaken"`
}

type SampleTreatmentRow struct {
	Time      string                `json:"time"`
	Treatment string                `json:"treatment"`
	Count     int                   `json:"count"`
	Samples   []ClinicalEventSample `json:"samples"`
}

type SampleTreatmentReport struct {
	TotalSamples int                  `json:"totalSamples"`
	Treatments   []SampleTreatmentRow `json:"treatments"`
}

// treatmentEvents selects the AGENT treatment events of the filtered patients.
func (s *State) treatmentEvents(f studyViewFilter) string {
	return fmt.Sprintf(
		"SELECT patient_unique_id, value, start_date FROM clinical_event_data_derived "+
			"WHERE cancer_study_identifier IN (%s) AND lower(event_type) = 'treatment' AND key = 'AGENT' "+
			"AND patient_unique_id IN (SELECT patient_unique_id FROM sample_derived WHERE %s)",
		orEmpty(f.studyIDs()), s.filterWhere(f))
}

func (s *State) patientTreatments(f studyViewFilter) ([]PatientTreatment, int) {
	type row struct {
		Treatment string `json:"treatment"`
		Count     int    `json:"count"`
	}
	rows, _ := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT value AS treatment, toInt64(uniqExact(patient_unique_id)) AS count FROM (%s) GROUP BY value ORDER BY value",
		s.treatmentEvents(f)))
	out := make([]PatientTreatment, len(rows))
	for i, r := range rows {
		out[i] = PatientTreatment{Treatment: r.Treatment, Count: r.Count}
	}
	type total struct {
		N int `json:"n"`
	}
	t, _ := ch.Query[total](s.CH, fmt.Sprintf(
		"SELECT toInt64(uniqExact(patient_unique_id)) AS n FROM (%s)", s.treatmentEvents(f)))
	n := 0
	if len(t) > 0 {
		n = t[0].N
	}
	return out, n
}

func (s *State) patientTreatmentCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	treatments, total := s.patientTreatments(f)
	writeJSON(w, PatientTreatmentReport{TotalPatients: total, PatientTreatments: treatments})
}

// sampleTreatmentRows pairs every filtered sample's acquisition time with the
// first dose of each treatment its patient received: Pre when the sample was
// taken on or before that dose, Post otherwise.
func (s *State) sampleTreatmentRows(f studyViewFilter, detailed bool) []SampleTreatmentRow {
	studyIn := orEmpty(f.studyIDs())
	type row struct {
		Treatment string `json:"treatment"`
		SampleID  string `json:"sampleId"`
		Patient   string `json:"patient"`
		Study     string `json:"study"`
		Time      int    `json:"time"`
		Pre       uint8  `json:"pre"`
	}
	rows, _ := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT t.treatment AS treatment, ced.sample_id AS sampleId, ced.patient_unique_id AS patient, "+
			"ced.cancer_study_identifier AS study, toInt64(ced.time_taken) AS time, toUInt8(ced.time_taken <= t.treatment_time_taken) AS pre "+
			"FROM (SELECT value AS sample_id, patient_unique_id, min(start_date) AS time_taken, cancer_study_identifier "+
			"FROM clinical_event_data_derived WHERE cancer_study_identifier IN (%[1]s) "+
			"AND (event_type ILIKE 'Sample Acquisition' OR event_type ILIKE 'SPECIMEN') AND key = 'SAMPLE_ID' "+
			"GROUP BY patient_unique_id, value, cancer_study_identifier) ced "+
			"INNER JOIN (SELECT patient_unique_id, value AS treatment, min(start_date) AS treatment_time_taken "+
			"FROM clinical_event_data_derived WHERE cancer_study_identifier IN (%[1]s) "+
			"AND lower(event_type) = 'treatment' AND key = 'AGENT' GROUP BY patient_unique_id, value) t "+
			"ON t.patient_unique_id = ced.patient_unique_id "+
			"WHERE concat(ced.cancer_study_identifier, '_', ced.sample_id) IN (%[2]s)",
		studyIn, s.filterSQL(f)))
	byKey := map[string]*SampleTreatmentRow{}
	for _, r := range rows {
		time := "Post"
		if r.Pre == 1 {
			time = "Pre"
		}
		k := time + "\x00" + r.Treatment
		row, ok := byKey[k]
		if !ok {
			row = &SampleTreatmentRow{Time: time, Treatment: r.Treatment, Samples: []ClinicalEventSample{}}
			byKey[k] = row
		}
		row.Count++
		if detailed {
			row.Samples = append(row.Samples, ClinicalEventSample{
				PatientID: strings.TrimPrefix(r.Patient, r.Study+"_"), SampleID: r.SampleID, StudyID: r.Study, TimeTaken: r.Time,
			})
		}
	}
	out := make([]SampleTreatmentRow, 0, len(byKey))
	for _, k := range sortedKeys(byKey) {
		out = append(out, *byKey[k])
	}
	return out
}

func (s *State) sampleTreatmentCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	detailed := strings.EqualFold(r.URL.Query().Get("projection"), "DETAILED")
	rows := s.sampleTreatmentRows(f, detailed)
	type total struct {
		N int `json:"n"`
	}
	t, _ := ch.Query[total](s.CH, fmt.Sprintf(
		"SELECT toInt64(uniqExact(value)) AS n FROM clinical_event_data_derived WHERE cancer_study_identifier IN (%s) "+
			"AND (event_type ILIKE 'Sample Acquisition' OR event_type ILIKE 'SPECIMEN') AND key = 'SAMPLE_ID' "+
			"AND concat(cancer_study_identifier, '_', value) IN (%s) "+
			"AND patient_unique_id IN (SELECT patient_unique_id FROM (%s))",
		orEmpty(f.studyIDs()), s.filterSQL(f), s.treatmentEvents(f)))
	n := 0
	if len(t) > 0 {
		n = t[0].N
	}
	writeJSON(w, SampleTreatmentReport{TotalSamples: n, Treatments: rows})
}

// treatmentsPatient serves the per-treatment patient rows (with the treated
// patients' samples) for POST /api/treatments/patient.
func (s *State) treatmentsPatient(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	type row struct {
		Treatment string `json:"treatment"`
		Patient   string `json:"patient"`
		Study     string `json:"study"`
		SampleID  string `json:"sampleId"`
	}
	rows, _ := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT DISTINCT t.value AS treatment, sd.patient_stable_id AS patient, sd.cancer_study_identifier AS study, "+
			"sd.sample_stable_id AS sampleId FROM (%s) t JOIN sample_derived sd ON sd.patient_unique_id = t.patient_unique_id "+
			"WHERE %s ORDER BY treatment, study, patient, sampleId",
		s.treatmentEvents(f), s.filterWhere(f)))
	type patientRow struct {
		Treatment string                `json:"treatment"`
		Count     int                   `json:"count"`
		Samples   []ClinicalEventSample `json:"samples"`
	}
	byTreatment := map[string]*patientRow{}
	patients := map[string]map[string]bool{}
	for _, r := range rows {
		pr, ok := byTreatment[r.Treatment]
		if !ok {
			pr = &patientRow{Treatment: r.Treatment, Samples: []ClinicalEventSample{}}
			byTreatment[r.Treatment] = pr
			patients[r.Treatment] = map[string]bool{}
		}
		if !patients[r.Treatment][r.Study+"\x00"+r.Patient] {
			patients[r.Treatment][r.Study+"\x00"+r.Patient] = true
			pr.Count++
		}
		pr.Samples = append(pr.Samples, ClinicalEventSample{PatientID: r.Patient, SampleID: r.SampleID, StudyID: r.Study})
	}
	out := make([]patientRow, 0, len(byTreatment))
	for _, k := range sortedKeys(byTreatment) {
		out = append(out, *byTreatment[k])
	}
	writeJSON(w, out)
}

// treatmentsSample serves the Pre/Post sample rows for POST /api/treatments/sample.
func (s *State) treatmentsSample(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	rows := s.sampleTreatmentRows(f, true)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Treatment < rows[j].Treatment })
	writeJSON(w, rows)
}

type ClinicalEventTypeCount struct {
	EventType string `json:"eventType"`
	Count     int    `json:"count"`
}

func (s *State) clinicalEventTypeCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	type row struct {
		EventType string `json:"eventType"`
		Count     int    `json:"count"`
	}
	rows, _ := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT event_type AS eventType, toInt64(uniqExact(patient_unique_id)) AS count FROM clinical_event_data_derived "+
			"WHERE patient_unique_id IN (SELECT patient_unique_id FROM sample_derived WHERE %s) GROUP BY event_type ORDER BY event_type",
		s.filterWhere(f)))
	out := make([]ClinicalEventTypeCount, len(rows))
	for i, r := range rows {
		out[i] = ClinicalEventTypeCount{EventType: r.EventType, Count: r.Count}
	}
	writeJSON(w, out)
}
