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

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
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
		SampleIdentifiers []core.Identifier `json:"sampleIdentifiers"`
		Chromosome        string            `json:"chromosome"`
	}
	// The body is a list of sample identifiers (an object wrapping them is also
	// accepted); the chromosome is a query parameter.
	var raw json.RawMessage
	core.ReadBody(r, &raw)
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
		core.WriteJSON(w, []CopyNumberSeg{})
		return
	}
	args := core.NewArgs()
	where := core.IdentifiersCond(args, core.TSeg, "g", f.SampleIdentifiers)
	if f.Chromosome != "" {
		where += " AND g.chrom = " + args.Str(f.Chromosome)
	}
	if meta {
		type countRow struct {
			C uint64 `json:"c"`
		}
		rows, err := core.QueryA[countRow](s.CH, "SELECT count() AS c FROM seg g WHERE "+where, args)
		if err != nil {
			core.Fail(w, r, err)
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
	sql := "SELECT g.study_id, g.sample_id, ifNull(sp.patient_id, ''), g.chrom, toInt64OrZero(g.start) AS start, " +
		"toInt64OrZero(g.end), toInt64OrZero(g.num_probes), toFloat64OrZero(g.segment_mean) " +
		"FROM seg g LEFT JOIN " + core.ByStudies(core.IdentifierStudies(f.SampleIdentifiers)).Samples(args) + " sp ON g.study_id = sp.study_id AND g.sample_id = sp.sample_id " +
		"WHERE " + where + core.SQLListOrder(r, segmentSortable, s.segOrder("g.study_id, g.sample_id, g.chrom, start"))
	body, err := s.CH.QueryStreamP(r.Context(), sql, "RowBinary", args.Params(sql))
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/json")
	if err := writeSegments(w, ch.NewRowBinaryReader(body)); err != nil {
		// The response is already under way: abort it so the client sees a
		// truncated transfer rather than a well-formed partial list.
		core.LogServerError(r, "streamed response aborted", err)
		panic(http.ErrAbortHandler)
	}
}

// segOrder is the ORDER BY of segment rows (seg, alias g): CopyNumberSegmentMapper's
// seg_id (file order) where the database carries it, else fallback.
func (s *State) segOrder(fallback string) string {
	if s.ModelHasColumn("seg", "seg_id") {
		return "g.seg_id"
	}
	return fallback
}

// segmentSortable are CopyNumberSegmentSortBy's fields as SQL over the
// fetched segments (alias g), sorted as the JSON values compare: the
// chromosome as text, the others as numbers.
var segmentSortable = map[string]string{
	"chromosome": "g.chrom", "start": "start", "end": "toInt64OrZero(g.end)",
	"numberOfProbes": "toInt64OrZero(g.num_probes)", "segmentMean": "toFloat64OrZero(g.segment_mean)",
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
			prefix = core.AppendJSONString(prefix, []byte(core.UniqueKey(string(study), string(sample))))
			prefix = append(prefix, `,"uniquePatientKey":`...)
			prefix = core.AppendJSONString(prefix, []byte(core.UniqueKey(string(study), string(patient))))
			prefix = append(prefix, `,"studyId":`...)
			prefix = core.AppendJSONString(prefix, study)
			prefix = append(prefix, `,"sampleId":`...)
			prefix = core.AppendJSONString(prefix, sample)
			prefix = append(prefix, `,"patientId":`...)
			prefix = core.AppendJSONString(prefix, patient)
			prefix = append(prefix, `,"chromosome":`...)
		}

		out = out[:0]
		if n > 0 {
			out = append(out, ',')
		}
		out = append(out, prefix...)
		out = core.AppendJSONString(out, chrom)
		out = append(out, `,"start":`...)
		out = strconv.AppendInt(out, start, 10)
		out = append(out, `,"end":`...)
		out = strconv.AppendInt(out, end, 10)
		out = append(out, `,"numberOfProbes":`...)
		out = strconv.AppendInt(out, probes, 10)
		out = append(out, `,"segmentMean":`...)
		if out, err = core.AppendJSONFloat(out, mean); err != nil {
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
	rows, err := ch.QueryP[clinAttrRow](s.CH, clinAttrSelect+" WHERE study_id = {study:String} ORDER BY clinical_attribute_id",
		ch.Params{"study": r.PathValue("studyId")})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]ClinicalAttribute, len(rows))
	for i, r := range rows {
		out[i] = toClinicalAttribute(r)
	}
	core.WriteJSON(w, out)
}

func (s *State) studyClinicalAttribute(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.QueryP[clinAttrRow](s.CH, clinAttrSelect+" WHERE study_id = {study:String} AND clinical_attribute_id = {attr:String} LIMIT 1",
		ch.Params{"study": r.PathValue("studyId"), "attr": r.PathValue("clinicalAttributeId")})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "clinical attribute not found", http.StatusNotFound)
		return
	}
	core.WriteJSON(w, toClinicalAttribute(rows[0]))
}

// ---------- Clinical attribute counts ----------

type ClinicalAttributeCount struct {
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	Count               uint64 `json:"count"`
}

// clinicalAttributeCountsFetch serves POST /api/clinical-attributes/counts/fetch.
func (s *State) clinicalAttributeCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers []core.Identifier `json:"sampleIdentifiers"`
		SampleListID      string            `json:"sampleListId"`
		StudyViewFilter   studyViewFilter   `json:"studyViewFilter"`
		StudyIDs          []string          `json:"studyIds"`
	}
	core.ReadBody(r, &f)

	scopeFilter := studyViewFilter{}
	args := core.NewArgs()
	// sc holds the clinical rows to the selected samples' studies, a key
	// range; sampleScope selects the samples over (study_id, sample_id) and
	// samples is their sample rows.
	var sc core.Scope
	var sampleScope, samples string
	switch {
	case f.SampleListID != "":
		sc = s.BySampleList(f.SampleListID)
		inList := "(study_id, sample_id) IN (SELECT study_id, sample_id FROM " + sc.From(args, core.TSampleList) + ")"
		sampleScope = sc.Key(args, core.TClinicalSample, "") + inList
		samples = sc.From(args, core.TSample) + " AND " + inList
	case len(f.SampleIdentifiers) > 0:
		scopeFilter.SampleIdentifiers = f.SampleIdentifiers
	case len(f.StudyViewFilter.SampleIdentifiers) > 0 || len(f.StudyViewFilter.StudyIDs) > 0:
		scopeFilter = f.StudyViewFilter
	case len(f.StudyIDs) > 0:
		scopeFilter.StudyIDs = f.StudyIDs
	default:
		core.WriteJSON(w, []ClinicalAttributeCount{})
		return
	}
	if sampleScope == "" {
		sc = scopeFilter.scope()
		sampleScope = s.sampleScope(args, scopeFilter)
		samples = s.sampleScopeFrom(args, scopeFilter, core.TSample)
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
		fmt.Sprintf("SELECT cp.attr_id AS attr_id, count() AS c FROM (SELECT * FROM clinical_patient WHERE %s1) cp "+
			"JOIN (SELECT study_id, patient_id FROM %s) sm ON sm.study_id = cp.study_id AND sm.patient_id = cp.patient_id "+
			"GROUP BY cp.attr_id ORDER BY attr_id", sc.Key(args, core.TClinicalPatient, ""), samples),
	}
	for _, q := range queries {
		rows, err := core.QueryA[countRow](s.CH, q, args)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		for _, r := range rows {
			out = append(out, ClinicalAttributeCount{ClinicalAttributeID: r.AttrID, Count: r.C})
		}
	}
	core.WriteJSON(w, out)
}
