package api

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeSurvival registers survival-data and clinical-event (timeline) endpoints.
func (s *State) routeSurvival(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/survival-data/fetch", s.survivalDataFetch)
	mux.HandleFunc("POST /api/clinical-events-meta/fetch", s.clinicalEventsMetaFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-events", s.studyClinicalEvents)
	mux.HandleFunc("GET /api/studies/{studyId}/patients/{patientId}/clinical-events", s.patientClinicalEvents)
}

// ---------- Survival data ----------

// clinicalEventRequest selects events of one type, optionally only those
// carrying one of the given key/value attributes.
type clinicalEventRequest struct {
	EventType  string              `json:"eventType"`
	Attributes []ClinicalEventData `json:"attributes"`
}

// clinicalEventRequestIdentifier is a set of event requests plus which
// occurrence (FIRST: earliest start, LAST: latest start/stop) dates them.
type clinicalEventRequestIdentifier struct {
	ClinicalEventRequests []clinicalEventRequest `json:"clinicalEventRequests"`
	Position              string                 `json:"position"`
}

// patientIdentifierPairs turns PatientIdentifiers into (studyId, patientId) pairs.
func patientIdentifierPairs(ids []identifier) [][2]string {
	pairs := make([][2]string, 0, len(ids))
	for _, id := range ids {
		pairs = append(pairs, [2]string{id.StudyID, id.id()})
	}
	return pairs
}

// patientEventScope is the clinical_event_data_derived predicate selecting the
// given patients.
func patientEventScope(pairs [][2]string) string {
	keys := make([]string, len(pairs))
	studies := map[string]bool{}
	for i, p := range pairs {
		keys[i] = p[0] + "_" + p[1]
		studies[p[0]] = true
	}
	return fmt.Sprintf("cancer_study_identifier IN (%s) AND patient_unique_id IN (%s)",
		inList(sortedKeys(studies)), inList(keys))
}

// eventRequestsSQL renders event requests as an OR over event type (and, with
// attributes, the event_type_key[_value] combinations); "" selects every event.
func eventRequestsSQL(reqs []clinicalEventRequest, withValue bool) string {
	var ors []string
	for _, q := range reqs {
		if len(q.Attributes) == 0 {
			ors = append(ors, fmt.Sprintf("event_type = '%s'", sqlLit(q.EventType)))
			continue
		}
		combos := make([]string, len(q.Attributes))
		for i, a := range q.Attributes {
			combos[i] = q.EventType + "_" + a.Key
			if withValue {
				combos[i] += "_" + a.Value
			}
		}
		expr := "concat(event_type, '_', key)"
		if withValue {
			expr = "concat(event_type, '_', key, '_', value)"
		}
		ors = append(ors, fmt.Sprintf("%s IN (%s)", expr, inList(combos)))
	}
	if len(ors) == 0 {
		return ""
	}
	return " AND (" + strings.Join(ors, " OR ") + ")"
}

// timelineSpan is a patient's matching events collapsed to the earliest start
// and the latest start/stop day.
type timelineSpan struct {
	StudyID   string `json:"studyId"`
	PatientID string `json:"patientId"`
	StartDate int64  `json:"startDate"`
	StopDate  int64  `json:"stopDate"`
}

func (t timelineSpan) at(position string) int64 {
	if strings.EqualFold(position, "FIRST") {
		return t.StartDate
	}
	return t.StopDate
}

// timelineSpans collapses each patient's events matching reqs (all events when
// reqs is empty) into a span, keyed by studyId+"\x00"+patientId.
func (s *State) timelineSpans(pairs [][2]string, reqs []clinicalEventRequest) (map[string]timelineSpan, []string, error) {
	out := map[string]timelineSpan{}
	if len(pairs) == 0 {
		return out, nil, nil
	}
	rows, err := ch.Query[timelineSpan](s.CH, fmt.Sprintf(
		"SELECT cancer_study_identifier AS studyId, substring(patient_unique_id, length(cancer_study_identifier) + 2) AS patientId, "+
			"toInt64(min(start_date)) AS startDate, toInt64(greatest(max(start_date), max(stop_date))) AS stopDate "+
			"FROM clinical_event_data_derived WHERE %s%s GROUP BY cancer_study_identifier, patient_unique_id "+
			"ORDER BY studyId, patientId",
		patientEventScope(pairs), eventRequestsSQL(reqs, true)))
	if err != nil {
		return nil, nil, err
	}
	order := make([]string, len(rows))
	for i, r := range rows {
		order[i] = r.StudyID + "\x00" + r.PatientID
		out[order[i]] = r
	}
	return out, order, nil
}

// javaDouble formats v like Java's String.valueOf(double) for the magnitudes
// survival months take.
func javaDouble(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e7 {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// survivalDataFetch serves POST /api/survival-data/fetch: survival computed from
// timeline events (ClinicalEventServiceImpl.getSurvivalData). For each patient
// with a start event, <prefix>_MONTHS is the time from the start event to the
// end event (status 1:EVENT) or, without one, to a later censoring event
// (status 0:CENSORED); patients with neither are skipped.
func (s *State) survivalDataFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AttributeIDPrefix              string                          `json:"attributeIdPrefix"`
		PatientIdentifiers             []identifier                    `json:"patientIdentifiers"`
		StartEventRequestIdentifier    *clinicalEventRequestIdentifier `json:"startEventRequestIdentifier"`
		EndEventRequestIdentifier      *clinicalEventRequestIdentifier `json:"endEventRequestIdentifier"`
		CensoredEventRequestIdentifier *clinicalEventRequestIdentifier `json:"censoredEventRequestIdentifier"`
	}
	readBody(r, &req)
	out := []ClinicalData{}
	if req.AttributeIDPrefix == "" || len(req.PatientIdentifiers) == 0 || req.StartEventRequestIdentifier == nil {
		writeJSON(w, out)
		return
	}
	start := req.StartEventRequestIdentifier
	end := req.EndEventRequestIdentifier
	if end == nil {
		end = &clinicalEventRequestIdentifier{Position: "LAST"}
	}
	censored := req.CensoredEventRequestIdentifier
	if censored == nil {
		censored = &clinicalEventRequestIdentifier{Position: "LAST"}
	}
	spansOf := func(keys []string, spans map[string]timelineSpan, reqs []clinicalEventRequest) (map[string]timelineSpan, error) {
		pairs := make([][2]string, len(keys))
		for i, k := range keys {
			pairs[i] = [2]string{spans[k].StudyID, spans[k].PatientID}
		}
		m, _, err := s.timelineSpans(pairs, reqs)
		return m, err
	}
	starts, order, err := s.timelineSpans(patientIdentifierPairs(req.PatientIdentifiers), start.ClinicalEventRequests)
	if err != nil {
		fail(w, err)
		return
	}
	ends, err := spansOf(order, starts, end.ClinicalEventRequests)
	if err != nil {
		fail(w, err)
		return
	}
	var kept []string
	for _, k := range order {
		if e, ok := ends[k]; !ok || starts[k].at(start.Position) < e.at(end.Position) {
			kept = append(kept, k)
		}
	}
	censors, err := spansOf(kept, starts, censored.ClinicalEventRequests)
	if err != nil {
		fail(w, err)
		return
	}
	for _, k := range kept {
		ev := starts[k]
		startDate := ev.at(start.Position)
		status := "1:EVENT"
		var endDate int64
		if e, ok := ends[k]; ok {
			endDate = e.at(end.Position)
		} else {
			c, ok := censors[k]
			if !ok || startDate >= c.at(censored.Position) {
				continue
			}
			endDate, status = c.at(censored.Position), "0:CENSORED"
		}
		for _, d := range [][2]string{
			{req.AttributeIDPrefix + "_MONTHS", javaDouble(float64(endDate-startDate) / 30.4)},
			{req.AttributeIDPrefix + "_STATUS", status},
		} {
			out = append(out, ClinicalData{
				ClinicalAttributeID: d[0], Value: d[1], PatientID: ev.PatientID, StudyID: ev.StudyID,
				UniquePatientKey: uniqueKey(ev.StudyID, ev.PatientID), PatientAttribute: true,
			})
		}
	}
	writeJSON(w, out)
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
	UniqueSampleKey                 string              `json:"uniqueSampleKey,omitempty"`
}

// timelineRow is one event of cbioportal_raw.timeline with its non-empty
// extra columns as (KEY, value) attributes.
type timelineRow struct {
	StudyID   string      `json:"studyId"`
	PatientID string      `json:"patientId"`
	EventType string      `json:"eventType"`
	StartDate *int64      `json:"startDate"`
	StopDate  *int64      `json:"stopDate"`
	Attrs     [][2]string `json:"attrs"`
}

func (row timelineRow) toEvent() ClinicalEvent {
	attrs := make([]ClinicalEventData, len(row.Attrs))
	for i, kv := range row.Attrs {
		attrs[i] = ClinicalEventData{Key: kv[0], Value: kv[1]}
	}
	return ClinicalEvent{
		StudyID:                         row.StudyID,
		PatientID:                       row.PatientID,
		EventType:                       row.EventType,
		StartNumberOfDaysSinceDiagnosis: row.StartDate,
		EndNumberOfDaysSinceDiagnosis:   row.StopDate,
		Attributes:                      attrs,
		UniquePatientKey:                uniqueKey(row.StudyID, row.PatientID),
	}
}

// timelineFixedCols are the timeline columns that are not event attributes.
var timelineFixedCols = map[string]bool{
	"study_id": true, "source_file": true, "alteration_type": true, "datatype": true, "profile_stable_id": true,
	"patient_id": true, "start_date": true, "stop_date": true, "event_type": true,
}

// timelineSelect selects timeline events, turning every other staged column
// into an attribute keyed by its upper-cased header.
func (s *State) timelineSelect() string {
	type col struct {
		Name string `json:"name"`
	}
	cols, _ := ch.Query[col](s.CH,
		"SELECT name FROM system.columns WHERE database = 'cbioportal_raw' AND table = 'timeline' ORDER BY position")
	var kv []string
	for _, c := range cols {
		if !timelineFixedCols[c.Name] {
			kv = append(kv, fmt.Sprintf("('%s', toString(`%s`))", sqlLit(strings.ToUpper(c.Name)), c.Name))
		}
	}
	attrs := "[]"
	if len(kv) > 0 {
		attrs = fmt.Sprintf("arrayFilter(x -> x.2 != '', [%s])", strings.Join(kv, ", "))
	}
	return "SELECT study_id AS studyId, patient_id AS patientId, event_type AS eventType, " +
		"toInt64OrNull(start_date) AS startDate, toInt64OrNull(stop_date) AS stopDate, " + attrs +
		" AS attrs FROM cbioportal_raw.timeline"
}

// ClinicalEventMeta is an event type with the distinct (lower-cased)
// attribute key/values seen for it, as POST /api/clinical-events-meta/fetch
// returns it.
type ClinicalEventMeta struct {
	EventType  string              `json:"eventType"`
	Attributes []ClinicalEventData `json:"attributes"`
}

// clinicalEventsMetaFetch serves POST /api/clinical-events-meta/fetch: per
// event type of the requested patients (optionally limited to the
// clinicalEventRequests types / attribute keys), its distinct attributes.
func (s *State) clinicalEventsMetaFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PatientIdentifiers    []identifier           `json:"patientIdentifiers"`
		ClinicalEventRequests []clinicalEventRequest `json:"clinicalEventRequests"`
	}
	readBody(r, &req)
	out := []ClinicalEventMeta{}
	if len(req.PatientIdentifiers) == 0 {
		writeJSON(w, out)
		return
	}
	type metaRow struct {
		EventType string `json:"eventType"`
		Key       string `json:"attrKey"`
		Value     string `json:"attrValue"`
	}
	rows, err := ch.Query[metaRow](s.CH, fmt.Sprintf(
		"SELECT DISTINCT lower(event_type) AS eventType, lower(key) AS attrKey, lower(value) AS attrValue "+
			"FROM clinical_event_data_derived WHERE %s%s ORDER BY eventType, attrKey, attrValue",
		patientEventScope(patientIdentifierPairs(req.PatientIdentifiers)), eventRequestsSQL(req.ClinicalEventRequests, false)))
	if err != nil {
		fail(w, err)
		return
	}
	for _, row := range rows {
		if len(out) == 0 || out[len(out)-1].EventType != row.EventType {
			out = append(out, ClinicalEventMeta{EventType: row.EventType, Attributes: []ClinicalEventData{}})
		}
		m := &out[len(out)-1]
		m.Attributes = append(m.Attributes, ClinicalEventData{Key: row.Key, Value: row.Value})
	}
	writeJSON(w, out)
}

// studyClinicalEvents serves GET /api/studies/{studyId}/clinical-events. Returns
// all timeline events for the study as ClinicalEvent rows. Empty (typed) when the
// study has no timeline data.
func (s *State) studyClinicalEvents(w http.ResponseWriter, r *http.Request) {
	s.writeClinicalEvents(w, fmt.Sprintf("study_id = '%s'", sqlLit(r.PathValue("studyId"))))
}

// patientClinicalEvents serves the patient-view timeline:
// GET /api/studies/{studyId}/patients/{patientId}/clinical-events.
func (s *State) patientClinicalEvents(w http.ResponseWriter, r *http.Request) {
	s.writeClinicalEvents(w, fmt.Sprintf("study_id = '%s' AND patient_id = '%s'",
		sqlLit(r.PathValue("studyId")), sqlLit(r.PathValue("patientId"))))
}

func (s *State) writeClinicalEvents(w http.ResponseWriter, where string) {
	rows, err := ch.Query[timelineRow](s.CH, fmt.Sprintf(
		"%s WHERE %s ORDER BY patientId, startDate, eventType", s.timelineSelect(), where))
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
