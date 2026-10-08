package molecular

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// RouteCompat registers endpoints that are thin views over others: the GET
// molecular-data form and mRNA percentiles.
func (s Server) RouteCompat(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/molecular-profiles/{id}/molecular-data", s.molecularDataGet)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/mrna-percentile/fetch", s.mrnaPercentileFetch)
}

type MrnaPercentile struct {
	MolecularProfileID string  `json:"molecularProfileId"`
	SampleID           string  `json:"sampleId"`
	PatientID          string  `json:"patientId"`
	StudyID            string  `json:"studyId"`
	EntrezGeneID       int64   `json:"entrezGeneId"`
	Percentile         float64 `json:"percentile"`
	ZScore             float64 `json:"zScore"`
	UniqueSampleKey    string  `json:"uniqueSampleKey"`
	UniquePatientKey   string  `json:"uniquePatientKey"`
}

// mrnaPercentileFetch ranks the sample's value of each gene among all the
// profile's numeric values for that gene and reports rank/n as a percentile
// rounded half up to two decimals, as Java's MrnaPercentileServiceImpl: tied
// values all take the highest rank of their group (TiesStrategy.MAXIMUM).
func (s Server) mrnaPercentileFetch(w http.ResponseWriter, r *http.Request) {
	var entrez []int64
	core.ReadBody(r, &entrez)
	sampleID := r.URL.Query().Get("sampleId")
	out := []MrnaPercentile{}
	data, err := s.molecularData(r.PathValue("id"), molecularDataFilter{EntrezGeneIDs: entrez})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	byGene := map[int64][]float64{}
	for _, d := range data {
		if d.EntrezGeneID == nil {
			continue
		}
		if v, err := strconv.ParseFloat(d.Value, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			byGene[*d.EntrezGeneID] = append(byGene[*d.EntrezGeneID], v)
		}
	}
	for _, d := range data {
		if d.SampleID != sampleID || d.EntrezGeneID == nil {
			continue
		}
		v, err := strconv.ParseFloat(d.Value, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		values := byGene[*d.EntrezGeneID]
		sorted := append([]float64(nil), values...)
		sort.Float64s(sorted)
		hi := sort.SearchFloat64s(sorted, v)
		for hi < len(sorted) && sorted[hi] == v {
			hi++
		}
		pct := roundHalfUp(float64(hi)/float64(len(sorted))*100, 2)
		out = append(out, MrnaPercentile{
			MolecularProfileID: d.MolecularProfileID, SampleID: d.SampleID, PatientID: d.PatientID, StudyID: d.StudyID,
			EntrezGeneID: *d.EntrezGeneID, Percentile: pct, ZScore: v,
			UniqueSampleKey: d.UniqueSampleKey, UniquePatientKey: d.UniquePatientKey,
		})
	}
	core.WriteJSON(w, out)
}

// roundHalfUp rounds x to the given decimals as Java's
// BigDecimal.valueOf(x).setScale(decimals, ROUND_HALF_UP): on x's shortest
// decimal representation, halves away from zero, so 27.155 is 27.16.
func roundHalfUp(x float64, decimals int) float64 {
	s := strconv.FormatFloat(math.Abs(x), 'f', -1, 64)
	dot := strings.IndexByte(s, '.')
	if dot < 0 || len(s)-dot-1 <= decimals {
		return x
	}
	v, _ := strconv.ParseFloat(s[:dot+1+decimals], 64)
	if s[dot+1+decimals] >= '5' {
		v += math.Pow(10, -float64(decimals))
	}
	v, _ = strconv.ParseFloat(strconv.FormatFloat(v, 'f', decimals, 64), 64)
	return math.Copysign(v, x)
}
