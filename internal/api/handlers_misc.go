package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeMisc registers the view-backed endpoints implemented in this file.
func (s *State) routeMisc(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/copy-number-segments/fetch", s.copyNumberSegmentsFetch)
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-attributes", s.studyClinicalAttributes)
	mux.HandleFunc("GET /api/studies/{studyId}/clinical-attributes/{clinicalAttributeId}", s.studyClinicalAttribute)
}

// ---------- Copy-number segments ----------

type segRow struct {
	StudyID        string  `json:"studyId"`
	SampleID       string  `json:"sampleId"`
	PatientID      string  `json:"patientId"`
	Chromosome     string  `json:"chromosome"`
	Start          int64   `json:"start"`
	End            int64   `json:"end"`
	NumberOfProbes int64   `json:"numberOfProbes"`
	SegmentMean    float64 `json:"segmentMean"`
}

// CopyNumberSeg is the Java CopyNumberSeg as CopyNumberSegMixin serializes
// it: segId and the internal ids are not part of the JSON.
type CopyNumberSeg struct {
	UniqueSampleKey  string  `json:"uniqueSampleKey"`
	UniquePatientKey string  `json:"uniquePatientKey"`
	StudyID          string  `json:"studyId"`
	SampleID         string  `json:"sampleId"`
	PatientID        string  `json:"patientId"`
	Chromosome       string  `json:"chromosome"`
	Start            int64   `json:"start"`
	End              int64   `json:"end"`
	NumberOfProbes   int64   `json:"numberOfProbes"`
	SegmentMean      float64 `json:"segmentMean"`
	SegID            int64   `json:"-"`
	Genes            []any   `json:"-"`
}

// copyNumberSegmentsFetch serves POST /api/copy-number-segments/fetch like
// CopyNumberSegmentController: META answers with only a total-count header
// from a count query; every other projection returns the same fields.
func (s *State) copyNumberSegmentsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers []identifier `json:"sampleIdentifiers"`
		Chromosome        string       `json:"chromosome"`
	}
	// The body is a list of sample identifiers (an object wrapping them is also
	// accepted); the chromosome is a query parameter.
	var raw json.RawMessage
	readBody(r, &raw)
	if json.Unmarshal(raw, &f.SampleIdentifiers) != nil {
		json.Unmarshal(raw, &f)
	}
	if c := r.URL.Query().Get("chromosome"); c != "" {
		f.Chromosome = c
	}
	meta := strings.EqualFold(r.URL.Query().Get("projection"), "META")
	if len(f.SampleIdentifiers) == 0 {
		if meta {
			w.Header().Set("total-count", "0")
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, []CopyNumberSeg{})
		return
	}
	where := segSampleWhere(f.SampleIdentifiers)
	if f.Chromosome != "" {
		where += fmt.Sprintf(" AND g.chrom = '%s'", sqlLit(f.Chromosome))
	}
	if meta {
		type countRow struct {
			C uint64 `json:"c"`
		}
		rows, err := ch.Query[countRow](s.CH, "SELECT count() AS c FROM seg g WHERE "+where)
		if err != nil {
			fail(w, err)
			return
		}
		n := uint64(0)
		if len(rows) > 0 {
			n = rows[0].C
		}
		w.Header().Set("total-count", strconv.FormatUint(n, 10))
		w.WriteHeader(http.StatusOK)
		return
	}
	body, err := s.CH.QueryStream(r.Context(),
		"SELECT g.study_id, g.sample_id, ifNull(sp.patient_id, ''), g.chrom, toInt64OrZero(g.start) AS start, "+
			"toInt64OrZero(g.end), toInt64OrZero(g.num_probes), toFloat64OrZero(g.segment_mean) "+
			"FROM seg g LEFT JOIN sample sp ON g.study_id = sp.study_id AND g.sample_id = sp.sample_id "+
			"WHERE "+where+" ORDER BY g.study_id, g.sample_id, g.chrom, start", "RowBinary")
	if err != nil {
		fail(w, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/json")
	if err := writeSegments(w, ch.NewRowBinaryReader(body)); err != nil {
		// The response is already under way: abort it so the client sees a
		// truncated transfer rather than a well-formed partial list.
		panic(http.ErrAbortHandler)
	}
}

// segSampleWhere restricts seg (alias g) to the identified samples, one
// study_id = … AND sample_id IN (…) term per study.
func segSampleWhere(ids []identifier) string {
	byStudy := map[string][]string{}
	var studies []string
	for _, id := range ids {
		if _, ok := byStudy[id.StudyID]; !ok {
			studies = append(studies, id.StudyID)
		}
		byStudy[id.StudyID] = append(byStudy[id.StudyID], id.id())
	}
	terms := make([]string, len(studies))
	for i, st := range studies {
		terms[i] = fmt.Sprintf("(g.study_id = '%s' AND g.sample_id IN (%s))", sqlLit(st), inList(byStudy[st]))
	}
	return "(" + strings.Join(terms, " OR ") + ")"
}

// writeSegments encodes RowBinary segment rows (study, sample, patient,
// chromosome, start, end, probes, mean) as the JSON array writeJSON would
// produce for []CopyNumberSeg, as the rows arrive.
func writeSegments(w io.Writer, rb *ch.RowBinaryReader) error {
	bw := bufio.NewWriterSize(w, 1<<16)
	var study, sample, patient, chrom []byte
	var prevStudy, prevSample, prevPatient []byte
	// prefix is the encoded object up to "chromosome", shared by the
	// consecutive rows of one sample.
	var prefix []byte
	var err error
	out := make([]byte, 0, 512)
	bw.WriteByte('[')
	for n := 0; rb.More(); n++ {
		if study, err = rb.String(study); err != nil {
			return err
		}
		if sample, err = rb.String(sample); err != nil {
			return err
		}
		if patient, err = rb.String(patient); err != nil {
			return err
		}
		if chrom, err = rb.String(chrom); err != nil {
			return err
		}
		start, err1 := rb.Int64()
		end, err2 := rb.Int64()
		probes, err3 := rb.Int64()
		mean, err4 := rb.Float64()
		if err = errors.Join(err1, err2, err3, err4); err != nil {
			return err
		}
		if n == 0 || !bytes.Equal(study, prevStudy) || !bytes.Equal(sample, prevSample) || !bytes.Equal(patient, prevPatient) {
			prevStudy = append(prevStudy[:0], study...)
			prevSample = append(prevSample[:0], sample...)
			prevPatient = append(prevPatient[:0], patient...)
			prefix = append(prefix[:0], `{"uniqueSampleKey":`...)
			prefix = appendJSONString(prefix, []byte(uniqueKey(string(study), string(sample))))
			prefix = append(prefix, `,"uniquePatientKey":`...)
			prefix = appendJSONString(prefix, []byte(uniqueKey(string(study), string(patient))))
			prefix = append(prefix, `,"studyId":`...)
			prefix = appendJSONString(prefix, study)
			prefix = append(prefix, `,"sampleId":`...)
			prefix = appendJSONString(prefix, sample)
			prefix = append(prefix, `,"patientId":`...)
			prefix = appendJSONString(prefix, patient)
			prefix = append(prefix, `,"chromosome":`...)
		}

		out = out[:0]
		if n > 0 {
			out = append(out, ',')
		}
		out = append(out, prefix...)
		out = appendJSONString(out, chrom)
		out = append(out, `,"start":`...)
		out = strconv.AppendInt(out, start, 10)
		out = append(out, `,"end":`...)
		out = strconv.AppendInt(out, end, 10)
		out = append(out, `,"numberOfProbes":`...)
		out = strconv.AppendInt(out, probes, 10)
		out = append(out, `,"segmentMean":`...)
		if out, err = appendJSONFloat(out, mean); err != nil {
			return err
		}
		out = append(out, '}')
		if _, err = bw.Write(out); err != nil {
			return err
		}
	}
	bw.WriteString("]\n")
	return bw.Flush()
}

// ---------- Clinical attributes (per study) ----------

func toClinicalAttribute(r clinAttrRow) ClinicalAttribute {
	dt := r.Datatype
	if dt == "" {
		dt = "STRING"
	}
	pr := r.Priority
	if pr == "" {
		pr = "1"
	}
	return ClinicalAttribute{
		ClinicalAttributeID: r.ClinicalAttributeID, DisplayName: r.DisplayName, Description: r.Description,
		Datatype: dt, PatientAttribute: r.PatientFlag != 0, Priority: pr, StudyID: r.StudyID,
	}
}

const clinAttrSelect = "SELECT study_id AS studyId, clinical_attribute_id AS clinicalAttributeId, " +
	"display_name AS displayName, description, datatype, priority, patient_attribute AS patientFlag " +
	"FROM clinical_attribute_meta"

func (s *State) studyClinicalAttributes(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	rows, err := ch.Query[clinAttrRow](s.CH, fmt.Sprintf(
		"%s WHERE study_id = '%s' ORDER BY clinical_attribute_id", clinAttrSelect, sid))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]ClinicalAttribute, len(rows))
	for i, r := range rows {
		out[i] = toClinicalAttribute(r)
	}
	writeJSON(w, out)
}

func (s *State) studyClinicalAttribute(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	aid := sqlLit(r.PathValue("clinicalAttributeId"))
	rows, err := ch.Query[clinAttrRow](s.CH, fmt.Sprintf(
		"%s WHERE study_id = '%s' AND clinical_attribute_id = '%s' LIMIT 1", clinAttrSelect, sid, aid))
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "clinical attribute not found", http.StatusNotFound)
		return
	}
	writeJSON(w, toClinicalAttribute(rows[0]))
}

// ---------- Clinical attribute counts ----------

type ClinicalAttributeCount struct {
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	Count               uint64 `json:"count"`
}

// clinicalAttributeCountsFetch serves POST /api/clinical-attributes/counts/fetch.
func (s *State) clinicalAttributeCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers []identifier    `json:"sampleIdentifiers"`
		SampleListID      string          `json:"sampleListId"`
		StudyViewFilter   studyViewFilter `json:"studyViewFilter"`
		StudyIDs          []string        `json:"studyIds"`
	}
	readBody(r, &f)

	scopeFilter := studyViewFilter{}
	var sampleScope string
	switch {
	case f.SampleListID != "":
		sampleScope = fmt.Sprintf("(study_id, sample_id) IN (SELECT study_id, sample_id FROM sample_list WHERE list_id = '%s')",
			sqlLit(f.SampleListID))
	case len(f.SampleIdentifiers) > 0:
		scopeFilter.SampleIdentifiers = f.SampleIdentifiers
	case len(f.StudyViewFilter.SampleIdentifiers) > 0 || len(f.StudyViewFilter.StudyIDs) > 0:
		scopeFilter = f.StudyViewFilter
	case len(f.StudyIDs) > 0:
		scopeFilter.StudyIDs = f.StudyIDs
	default:
		writeJSON(w, []ClinicalAttributeCount{})
		return
	}
	if sampleScope == "" {
		sampleScope = s.sampleScope(scopeFilter)
	}
	type countRow struct {
		AttrID string `json:"attr_id"`
		C      uint64 `json:"c"`
	}
	out := []ClinicalAttributeCount{}
	// ClinicalAttributeMapper counts rows: a sample attribute once per sample,
	// a patient attribute once per selected sample of the patient (the
	// patient row joined to its samples). Sample counts come first.
	queries := []string{
		fmt.Sprintf("SELECT attr_id, count() AS c FROM clinical_sample WHERE %s GROUP BY attr_id ORDER BY attr_id", sampleScope),
		fmt.Sprintf("SELECT cp.attr_id AS attr_id, count() AS c FROM clinical_patient cp "+
			"JOIN (SELECT study_id, patient_id FROM sample WHERE %s) sm ON sm.study_id = cp.study_id AND sm.patient_id = cp.patient_id "+
			"GROUP BY cp.attr_id ORDER BY attr_id", sampleScope),
	}
	for _, q := range queries {
		rows, err := ch.Query[countRow](s.CH, q)
		if err != nil {
			fail(w, err)
			return
		}
		for _, r := range rows {
			out = append(out, ClinicalAttributeCount{ClinicalAttributeID: r.AttrID, Count: r.C})
		}
	}
	writeJSON(w, out)
}
