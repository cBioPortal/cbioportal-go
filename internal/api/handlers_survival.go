package api

import (
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeSurvival registers survival-data and clinical-event (timeline) endpoints.
func (s *State) routeSurvival(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/survival-data/fetch", s.survivalDataFetch)
	mux.HandleFunc("POST /api/clinical-events-meta/fetch", s.clinicalEventsMetaFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-events", s.studyClinicalEvents)
}

// ---------- Survival data ----------

// survivalDataFetch serves POST /api/survival-data/fetch. Survival data is a set
// of patient-level clinical attributes derived from an attributeIdPrefix
// (e.g. "OS" -> OS_STATUS, OS_MONTHS). Returns ClinicalData rows, matching the
// generated client's Array<ClinicalData> response.
func (s *State) survivalDataFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AttributeIdPrefix  string       `json:"attributeIdPrefix"`
		PatientIdentifiers []identifier `json:"patientIdentifiers"`
		SampleIds          []string     `json:"sampleIds"`
		StudyIds           []string     `json:"studyIds"`
	}
	readBody(r, &req)

	prefix := strings.TrimSpace(req.AttributeIdPrefix)
	if prefix == "" {
		writeJSON(w, []ClinicalData{})
		return
	}
	attrs := inList([]string{prefix + "_STATUS", prefix + "_MONTHS"})

	scope := survivalPatientScope(req.PatientIdentifiers, req.SampleIds, req.StudyIds)
	if scope == "" {
		writeJSON(w, []ClinicalData{})
		return
	}

	rows, err := ch.Query[clinicalRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, patient_id AS id, attr_id AS clinicalAttributeId, attr_value AS value "+
			"FROM cbioportal.clinical_patient WHERE %s AND attr_id IN (%s) ORDER BY id, attr_id",
		scope, attrs))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toClinicalData(rows, true))
}

// survivalPatientScope builds a SQL predicate over (study_id, patient_id) for the
// requested patients. Patients may be given directly as patientIdentifiers, or
// resolved from sampleIds + studyIds (parallel arrays; a single studyId applies
// to every sample). Returns "" when no scope can be built.
func survivalPatientScope(patients []identifier, sampleIds, studyIds []string) string {
	if len(patients) > 0 {
		pairs := make([][2]string, len(patients))
		for i, id := range patients {
			pairs[i] = [2]string{id.StudyID, id.id()}
		}
		return fmt.Sprintf("(study_id, patient_id) IN (%s)", tupleIn(pairs))
	}
	if len(sampleIds) == 0 {
		return ""
	}
	pairs := make([][2]string, 0, len(sampleIds))
	for i, sid := range sampleIds {
		var study string
		switch {
		case len(studyIds) == len(sampleIds):
			study = studyIds[i]
		case len(studyIds) == 1:
			study = studyIds[0]
		default:
			return ""
		}
		pairs = append(pairs, [2]string{study, sid})
	}
	return fmt.Sprintf(
		"(study_id, patient_id) IN (SELECT study_id, patient_id FROM cbioportal.sample WHERE (study_id, sample_id) IN (%s))",
		tupleIn(pairs))
}

// ---------- Clinical events (timeline) ----------

// ClinicalEventData is one key/value attribute attached to a clinical event.
type ClinicalEventData struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ClinicalEvent mirrors the generated client's ClinicalEvent interface. Timeline
// events are patient-level; start/end days are null when the source date is blank.
type ClinicalEvent struct {
	StudyID                         string              `json:"studyId"`
	PatientID                       string              `json:"patientId"`
	EventType                       string              `json:"eventType"`
	StartNumberOfDaysSinceDiagnosis *int64              `json:"startNumberOfDaysSinceDiagnosis"`
	EndNumberOfDaysSinceDiagnosis   *int64              `json:"endNumberOfDaysSinceDiagnosis"`
	Attributes                      []ClinicalEventData `json:"attributes"`
	UniquePatientKey                string              `json:"uniquePatientKey"`
	UniqueSampleKey                 string              `json:"uniqueSampleKey"`
}

// timelineRow is one row of cbioportal_raw.timeline.
type timelineRow struct {
	StudyID           string `json:"studyId"`
	PatientID         string `json:"patientId"`
	SampleID          string `json:"sampleId"`
	EventType         string `json:"eventType"`
	StartDate         *int64 `json:"startDate"`
	StopDate          *int64 `json:"stopDate"`
	Status            string `json:"status"`
	EventTypeDetailed string `json:"eventTypeDetailed"`
	TreatmentType     string `json:"treatmentType"`
	Surgery           string `json:"surgery"`
	Subtype           string `json:"subtype"`
	Agent             string `json:"agent"`
}

func (row timelineRow) toEvent() ClinicalEvent {
	attrs := []ClinicalEventData{}
	add := func(k, v string) {
		if v != "" {
			attrs = append(attrs, ClinicalEventData{Key: k, Value: v})
		}
	}
	add("EVENT_TYPE_DETAILED", row.EventTypeDetailed)
	add("STATUS", row.Status)
	add("TREATMENT_TYPE", row.TreatmentType)
	add("SURGERY", row.Surgery)
	add("SUBTYPE", row.Subtype)
	add("AGENT", row.Agent)

	sampleKey := ""
	if row.SampleID != "" {
		sampleKey = uniqueKey(row.StudyID, row.SampleID)
	}
	return ClinicalEvent{
		StudyID:                         row.StudyID,
		PatientID:                       row.PatientID,
		EventType:                       row.EventType,
		StartNumberOfDaysSinceDiagnosis: row.StartDate,
		EndNumberOfDaysSinceDiagnosis:   row.StopDate,
		Attributes:                      attrs,
		UniquePatientKey:                uniqueKey(row.StudyID, row.PatientID),
		UniqueSampleKey:                 sampleKey,
	}
}

const timelineSelect = "SELECT study_id AS studyId, patient_id AS patientId, sample_id AS sampleId, " +
	"event_type AS eventType, toInt64OrNull(start_date) AS startDate, toInt64OrNull(stop_date) AS stopDate, " +
	"status AS status, event_type_detailed AS eventTypeDetailed, treatment_type AS treatmentType, " +
	"surgery AS surgery, subtype AS subtype, agent AS agent FROM cbioportal_raw.timeline"

// clinicalEventsMetaFetch serves POST /api/clinical-events-meta/fetch. Returns one
// ClinicalEvent per distinct event type present for the requested patients (event
// meta). When no patients are given or none have timeline data, returns a typed
// empty array.
func (s *State) clinicalEventsMetaFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PatientIdentifiers []identifier `json:"patientIdentifiers"`
	}
	readBody(r, &req)
	if len(req.PatientIdentifiers) == 0 {
		writeJSON(w, []ClinicalEvent{})
		return
	}
	pairs := make([][2]string, len(req.PatientIdentifiers))
	for i, id := range req.PatientIdentifiers {
		pairs[i] = [2]string{id.StudyID, id.id()}
	}

	type metaRow struct {
		StudyID   string `json:"studyId"`
		EventType string `json:"eventType"`
	}
	rows, err := ch.Query[metaRow](s.CH, fmt.Sprintf(
		"SELECT DISTINCT study_id AS studyId, event_type AS eventType FROM cbioportal_raw.timeline "+
			"WHERE (study_id, patient_id) IN (%s) ORDER BY studyId, eventType",
		tupleIn(pairs)))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]ClinicalEvent, len(rows))
	for i, row := range rows {
		out[i] = ClinicalEvent{StudyID: row.StudyID, EventType: row.EventType, Attributes: []ClinicalEventData{}}
	}
	writeJSON(w, out)
}

// studyClinicalEvents serves GET /api/studies/{studyId}/clinical-events. Returns
// all timeline events for the study as ClinicalEvent rows. Empty (typed) when the
// study has no timeline data.
func (s *State) studyClinicalEvents(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	rows, err := ch.Query[timelineRow](s.CH, fmt.Sprintf(
		"%s WHERE study_id = '%s' ORDER BY patientId, startDate, eventType",
		timelineSelect, sid))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]ClinicalEvent, len(rows))
	for i, row := range rows {
		out[i] = row.toEvent()
	}
	writeJSON(w, out)
}
