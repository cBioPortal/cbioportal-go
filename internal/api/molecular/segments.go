package molecular

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// ---------- Single-sample copy-number segments ----------

// SampleCopyNumberSegments — GET
// /api/studies/{studyId}/samples/{sampleId}/copy-number-segments. Returns
// []CopyNumberSeg for one sample (same shape as copyNumberSegmentsFetch).
func (s Server) SampleCopyNumberSegments(w http.ResponseWriter, r *http.Request) {
	study, sample := r.PathValue("studyId"), r.PathValue("sampleId")
	p := ch.Params{"study": study, "sample": sample}
	chrom := ""
	if c := r.URL.Query().Get("chromosome"); c != "" {
		chrom = " AND g.chrom = {chrom:String}"
		p["chrom"] = c
	}
	rows, err := ch.QueryP[segRow](s.CH, fmt.Sprintf(
		"SELECT g.study_id AS studyId, g.sample_id AS sampleId, ifNull(sp.patient_id, '') AS patientId, "+
			"g.chrom AS chromosome, toInt64OrZero(g.start) AS start, toInt64OrZero(g.end) AS end, "+
			"toInt64OrZero(g.num_probes) AS numberOfProbes, toFloat64OrZero(g.segment_mean) AS segmentMean "+
			"FROM seg g LEFT JOIN %s sp ON g.study_id = sp.study_id AND g.sample_id = sp.sample_id "+
			"WHERE g.study_id = {study:String} AND g.sample_id = {sample:String}%s ORDER BY %s",
		core.BySample(study, sample).Samples(core.ParamsBinder(p)), chrom, s.segOrder("g.chrom, start")), p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]CopyNumberSeg, len(rows))
	for i, r := range rows {
		out[i] = CopyNumberSeg{
			UniqueSampleKey:  core.UniqueKey(r.StudyID, r.SampleID),
			UniquePatientKey: core.UniqueKey(r.StudyID, r.PatientID),
			PatientID:        r.PatientID, SampleID: r.SampleID, StudyID: r.StudyID,
			Chromosome: r.Chromosome, Start: r.Start, End: r.End,
			NumberOfProbes: r.NumberOfProbes, SegmentMean: r.SegmentMean,
			Genes: []any{},
		}
	}
	core.WriteJSON(w, out)
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

// CopyNumberSegmentsFetch serves POST /api/copy-number-segments/fetch like
// CopyNumberSegmentController: META answers with only a total-count header
// from a count query; every other projection returns the same fields.
func (s Server) CopyNumberSegmentsFetch(w http.ResponseWriter, r *http.Request) {
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
		"WHERE " + where + core.SQLListOrder(r, SegmentSortable, s.segOrder("g.study_id, g.sample_id, g.chrom, start"))
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
func (s Server) segOrder(fallback string) string {
	if s.ModelHasColumn("seg", "seg_id") {
		return "g.seg_id"
	}
	return fallback
}

// SegmentSortable are CopyNumberSegmentSortBy's fields as SQL over the
// fetched segments (alias g), sorted as the JSON values compare: the
// chromosome as text, the others as numbers.
var SegmentSortable = map[string]string{
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
