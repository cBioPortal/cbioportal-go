package api

import (
	"fmt"
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// ---------- Single-sample copy-number segments ----------

// sampleCopyNumberSegments — GET
// /api/studies/{studyId}/samples/{sampleId}/copy-number-segments. Returns
// []CopyNumberSeg for one sample (same shape as copyNumberSegmentsFetch).
func (s *State) sampleCopyNumberSegments(w http.ResponseWriter, r *http.Request) {
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
