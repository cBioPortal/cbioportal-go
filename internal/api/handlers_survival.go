package api

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/transform"
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
func patientIdentifierPairs(ids []core.Identifier) [][2]string {
	pairs := make([][2]string, 0, len(ids))
	for _, id := range ids {
		pairs = append(pairs, [2]string{id.StudyID, id.ID()})
	}
	return pairs
}

// patientEvents is the clinical_event_data_derived rows of the given
// patients, as a FROM source.
func patientEvents(a *core.SQLArgs, pairs [][2]string) string {
	keys := make([]string, len(pairs))
	for i, p := range pairs {
		keys[i] = p[0] + "_" + p[1]
	}
	return core.ByStudies(core.PairStudies(pairs)).From(a, core.TClinicalEventDataDerived) + " AND patient_unique_id IN " + a.Strs(keys)
}

// eventRequestsSQL renders event requests as an OR over event type (and, with
// attributes, the event_type_key[_value] combinations); "" selects every event.
func eventRequestsSQL(a *core.SQLArgs, reqs []clinicalEventRequest, withValue bool) string {
	var ors []string
	for _, q := range reqs {
		if len(q.Attributes) == 0 {
			ors = append(ors, "event_type = "+a.Str(q.EventType))
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
		ors = append(ors, fmt.Sprintf("%s IN %s", expr, a.Strs(combos)))
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

// survivalSpans collapses, in one ClickHouse pass, each patient's events
// matching the start, end and censoring requests (all events when a kind has
// no requests) into one span per kind: the earliest start and the latest
// start/stop day. Only patients with a start span are returned, keyed by
// studyId+"\x00"+patientId; order lists them by study and patient id.
func (s *State) survivalSpans(pairs [][2]string, startReqs, endReqs, censorReqs []clinicalEventRequest) (
	starts, ends, censors map[string]timelineSpan, order []string, err error) {
	starts, ends, censors = map[string]timelineSpan{}, map[string]timelineSpan{}, map[string]timelineSpan{}
	if len(pairs) == 0 {
		return starts, ends, censors, nil, nil
	}
	args := core.NewArgs()
	cond := func(reqs []clinicalEventRequest) string {
		if c := eventRequestsSQL(args, reqs, true); c != "" {
			return strings.TrimPrefix(c, " AND ")
		}
		return "1"
	}
	span := func(prefix, c string) string {
		return fmt.Sprintf("countIf(%[2]s) AS %[1]sN, toInt64(minIf(start_date, %[2]s)) AS %[1]sStart, "+
			"toInt64(greatest(maxIf(start_date, %[2]s), maxIf(stop_date, %[2]s))) AS %[1]sStop", prefix, c)
	}
	type row struct {
		StudyID     string `json:"studyId"`
		PatientID   string `json:"patientId"`
		StartN      uint64 `json:"startN"`
		StartStart  int64  `json:"startStart"`
		StartStop   int64  `json:"startStop"`
		EndN        uint64 `json:"endN"`
		EndStart    int64  `json:"endStart"`
		EndStop     int64  `json:"endStop"`
		CensorN     uint64 `json:"censorN"`
		CensorStart int64  `json:"censorStart"`
		CensorStop  int64  `json:"censorStop"`
	}
	sc, ec, cc := cond(startReqs), cond(endReqs), cond(censorReqs)
	rows, err := core.QueryA[row](s.CH, fmt.Sprintf(
		"SELECT cancer_study_identifier AS studyId, substring(patient_unique_id, length(cancer_study_identifier) + 2) AS patientId, "+
			"%s, %s, %s FROM %s AND ((%s) OR (%s) OR (%s)) "+
			"GROUP BY cancer_study_identifier, patient_unique_id HAVING startN > 0 ORDER BY studyId, patientId",
		span("start", sc), span("end", ec), span("censor", cc), patientEvents(args, pairs), sc, ec, cc), args)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	order = make([]string, len(rows))
	for i, r := range rows {
		k := r.StudyID + "\x00" + r.PatientID
		order[i] = k
		starts[k] = timelineSpan{StudyID: r.StudyID, PatientID: r.PatientID, StartDate: r.StartStart, StopDate: r.StartStop}
		if r.EndN > 0 {
			ends[k] = timelineSpan{StudyID: r.StudyID, PatientID: r.PatientID, StartDate: r.EndStart, StopDate: r.EndStop}
		}
		if r.CensorN > 0 {
			censors[k] = timelineSpan{StudyID: r.StudyID, PatientID: r.PatientID, StartDate: r.CensorStart, StopDate: r.CensorStop}
		}
	}
	return starts, ends, censors, order, nil
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
		PatientIdentifiers             []core.Identifier               `json:"patientIdentifiers"`
		StartEventRequestIdentifier    *clinicalEventRequestIdentifier `json:"startEventRequestIdentifier"`
		EndEventRequestIdentifier      *clinicalEventRequestIdentifier `json:"endEventRequestIdentifier"`
		CensoredEventRequestIdentifier *clinicalEventRequestIdentifier `json:"censoredEventRequestIdentifier"`
	}
	core.ReadBody(r, &req)
	out := []ClinicalData{}
	if req.AttributeIDPrefix == "" || len(req.PatientIdentifiers) == 0 || req.StartEventRequestIdentifier == nil {
		core.WriteJSON(w, out)
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
	starts, ends, censors, order, err := s.survivalSpans(patientIdentifierPairs(req.PatientIdentifiers),
		start.ClinicalEventRequests, end.ClinicalEventRequests, censored.ClinicalEventRequests)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	var kept []string
	for _, k := range order {
		if e, ok := ends[k]; !ok || starts[k].at(start.Position) < e.at(end.Position) {
			kept = append(kept, k)
		}
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
				UniquePatientKey: core.UniqueKey(ev.StudyID, ev.PatientID), PatientAttribute: true,
			})
		}
	}
	core.WriteJSON(w, out)
}

// ---------- Clinical events (timeline) ----------

// ClinicalEventData is one key/value attribute attached to a clinical event.
type ClinicalEventData struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ClinicalEvent mirrors the generated client's ClinicalEvent interface. Timeline
// events are patient-level; start/end days are omitted when the source date is
// blank, as Java's NON_NULL serialization omits the null Integer.
type ClinicalEvent struct {
	StudyID                         string              `json:"studyId"`
	PatientID                       string              `json:"patientId"`
	UniquePatientKey                string              `json:"uniquePatientKey"`
	EventType                       string              `json:"eventType"`
	StartNumberOfDaysSinceDiagnosis *int64              `json:"startNumberOfDaysSinceDiagnosis,omitempty"`
	EndNumberOfDaysSinceDiagnosis   *int64              `json:"endNumberOfDaysSinceDiagnosis,omitempty"`
	Attributes                      []ClinicalEventData `json:"attributes"`
}

// clinicalEventID is a ClinicalEvent in the ID projection: ClinicalEventMapper
// selects no dates and the service reads no attributes.
type clinicalEventID struct {
	StudyID          string `json:"studyId"`
	PatientID        string `json:"patientId"`
	UniquePatientKey string `json:"uniquePatientKey"`
	EventType        string `json:"eventType"`
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
		UniquePatientKey:                core.UniqueKey(row.StudyID, row.PatientID),
	}
}

// timelineSelect selects timeline events with their event data as
// (key, value) attributes in file column order. A staged timeline turns
// every non-core column into an attribute keyed by the file's header as
// written (recorded in source_header, else the upper-cased column name), as
// clinical_event_data_derived keys it; repeated headers stay repeated
// (timelineEventData keeps the last). A timeline built from a production
// clone carries the stored (key, value) pairs in its event_attributes
// column instead. Without staged timeline data it selects no events. order
// is the ORDER BY of ClinicalEventMapper's clinical_event_id where the
// timeline carries it (production's, or the importer's numbering: timeline
// files in import order, rows in file order), else empty.
func (s *State) timelineSelect() (sql, order string) {
	cols := s.RawColumns("timeline")
	if slices.Contains(cols, "event_attributes") {
		if slices.Contains(cols, "clinical_event_id") {
			order = "clinical_event_id"
		}
		return "SELECT study_id AS studyId, patient_id AS patientId, event_type AS eventType, " +
			"toInt64OrNull(start_date) AS startDate, toInt64OrNull(stop_date) AS stopDate, " +
			"event_attributes AS attrs FROM cbioportal_raw.timeline", order
	}
	from := s.RawSource("timeline", "study_id", "patient_id", "event_type", "start_date", "stop_date")
	joinable := from == "cbioportal_raw.timeline" && slices.Contains(cols, "source_file")
	headers := joinable && slices.Contains(s.RawColumns("source_header"), "header")
	var kv []string
	for i, c := range cols {
		if transform.TimelineCoreColumns[c] || transform.OrderColumns[c] {
			continue
		}
		key, pos := fmt.Sprintf("'%s'", core.SQLLit(strings.ToUpper(c))), fmt.Sprintf("toUInt32(%d)", i)
		if headers {
			lit := fmt.Sprintf("'%s'", core.SQLLit(c))
			key = fmt.Sprintf("if(mapContains(h_header, %[1]s), h_header[%[1]s], %[2]s)", lit, key)
			pos = fmt.Sprintf("if(mapContains(h_position, %[1]s), h_position[%[1]s], %[2]s)", lit, pos)
		}
		kv = append(kv, fmt.Sprintf("(%s, toString(`%s`), %s)", key, c, pos))
	}
	attrs := "[]"
	if len(kv) > 0 {
		attrs = fmt.Sprintf("arrayMap(x -> (x.1, x.2), arraySort(x -> x.3, arrayFilter(x -> x.2 != '', [%s])))",
			strings.Join(kv, ", "))
	}
	if headers {
		from += " LEFT JOIN (SELECT study_id AS h_study, source_file AS h_file, " +
			"CAST((groupArray(column_name), groupArray(header)), 'Map(String, String)') AS h_header, " +
			"CAST((groupArray(column_name), groupArray(position)), 'Map(String, UInt32)') AS h_position " +
			"FROM cbioportal_raw.source_header WHERE table_name = 'timeline' GROUP BY study_id, source_file) h " +
			"ON h.h_study = timeline.study_id AND h.h_file = timeline.source_file"
	}
	if joinable && len(s.RawColumns("import_order")) > 0 {
		from += " LEFT JOIN (SELECT study_id AS o_study, name AS o_file, seq AS o_seq FROM cbioportal_raw.import_order " +
			"WHERE kind = 'data') o ON o.o_study = timeline.study_id AND o.o_file = timeline.source_file"
		order = "o_seq, " + s.RawFileOrder("timeline", "timeline.")
	}
	return "SELECT study_id AS studyId, patient_id AS patientId, event_type AS eventType, " +
		"toInt64OrNull(start_date) AS startDate, toInt64OrNull(stop_date) AS stopDate, " + attrs +
		" AS attrs FROM " + from, order
}

// timelineEventData is the event data ImportTimelineData stores for a
// staged event's attributes in file column order: of a repeated header the
// last non-empty cell, kept at the header's first place.
func timelineEventData(attrs [][2]string) [][2]string {
	at := make(map[string]int, len(attrs))
	out := make([][2]string, 0, len(attrs))
	for _, kv := range attrs {
		if i, ok := at[kv[0]]; ok {
			out[i][1] = kv[1]
			continue
		}
		at[kv[0]] = len(out)
		out = append(out, kv)
	}
	return out
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
		PatientIdentifiers    []core.Identifier      `json:"patientIdentifiers"`
		ClinicalEventRequests []clinicalEventRequest `json:"clinicalEventRequests"`
	}
	core.ReadBody(r, &req)
	out := []ClinicalEventMeta{}
	if len(req.PatientIdentifiers) == 0 {
		core.WriteJSON(w, out)
		return
	}
	type metaRow struct {
		EventType string `json:"eventType"`
		Key       string `json:"attrKey"`
		Value     string `json:"attrValue"`
	}
	args := core.NewArgs()
	rows, err := core.QueryA[metaRow](s.CH, fmt.Sprintf(
		"SELECT DISTINCT lower(event_type) AS eventType, lower(key) AS attrKey, lower(value) AS attrValue "+
			"FROM %s%s ORDER BY eventType, attrKey, attrValue",
		patientEvents(args, patientIdentifierPairs(req.PatientIdentifiers)), eventRequestsSQL(args, req.ClinicalEventRequests, false)), args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	for _, row := range rows {
		if len(out) == 0 || out[len(out)-1].EventType != row.EventType {
			out = append(out, ClinicalEventMeta{EventType: row.EventType, Attributes: []ClinicalEventData{}})
		}
		m := &out[len(out)-1]
		m.Attributes = append(m.Attributes, ClinicalEventData{Key: row.Key, Value: row.Value})
	}
	core.WriteJSON(w, out)
}

// studyClinicalEvents serves GET /api/studies/{studyId}/clinical-events. Returns
// all timeline events for the study as ClinicalEvent rows. Empty (typed) when the
// study has no timeline data.
func (s *State) studyClinicalEvents(w http.ResponseWriter, r *http.Request) {
	s.writeClinicalEvents(w, r, "study_id = {study:String}", ch.Params{"study": r.PathValue("studyId")})
}

// patientClinicalEvents serves the patient-view timeline:
// GET /api/studies/{studyId}/patients/{patientId}/clinical-events.
func (s *State) patientClinicalEvents(w http.ResponseWriter, r *http.Request) {
	s.writeClinicalEvents(w, r, "study_id = {study:String} AND patient_id = {patient:String}",
		ch.Params{"study": r.PathValue("studyId"), "patient": r.PathValue("patientId")})
}

// clinicalEventSortable are the ClinicalEventSortBy columns (eventType,
// startDate, stopDate) as timelineSelect names them.
var clinicalEventSortable = map[string]string{
	"eventType": "eventType", "startNumberOfDaysSinceDiagnosis": "startDate", "endNumberOfDaysSinceDiagnosis": "stopDate",
}

// writeClinicalEvents streams the timeline events matching where, whose
// placeholders take their values from p; a sorted page is read as
// sqlPageOrder orders it.
func (s *State) writeClinicalEvents(w http.ResponseWriter, r *http.Request, where string, p ch.Params) {
	sel, def := s.timelineSelect()
	if def == "" {
		def = "patientId, startDate, eventType"
	}
	if core.ProjectionOf(r) == "META" {
		core.WriteMetaCount(w, r, s.CH, fmt.Sprintf("SELECT count() AS n FROM (%s WHERE %s)", sel, where), p)
		return
	}
	// Without clinical_event_id the order leaves ties.
	query := core.SQLStablePageQuery(r, clinicalEventSortable, fmt.Sprintf("%s WHERE %s", sel, where), def)
	// ImportTimelineData collects a staged event's attributes in a HashMap,
	// so they are stored (and read back) in its iteration order; events of a
	// production clone carry their stored order.
	staged := !slices.Contains(s.RawColumns("timeline"), "event_attributes")
	idOnly := core.ProjectionOf(r) == "ID"
	out := core.NewJSONArray(w, r)
	err := ch.QueryEachP(s.CH, query, p, func(row *timelineRow) error {
		if idOnly {
			return out.Add(clinicalEventID{StudyID: row.StudyID, PatientID: row.PatientID,
				UniquePatientKey: core.UniqueKey(row.StudyID, row.PatientID), EventType: row.EventType})
		}
		if staged {
			row.Attrs = javaHashMapAttrs(timelineEventData(row.Attrs))
		}
		return out.Add(row.toEvent())
	})
	out.Finish(err)
}

// javaHashMapAttrs orders (key, value) attributes as a java.util.HashMap
// they were put into in the given order iterates them.
func javaHashMapAttrs(attrs [][2]string) [][2]string {
	keys := make([]string, len(attrs))
	value := make(map[string]string, len(attrs))
	for i, kv := range attrs {
		keys[i] = kv[0]
		value[kv[0]] = kv[1]
	}
	out := make([][2]string, 0, len(attrs))
	for _, k := range core.JavaHashMapOrder(keys) {
		out = append(out, [2]string{k, value[k]})
	}
	return out
}
