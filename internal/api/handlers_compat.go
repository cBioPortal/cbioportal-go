package api

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// routeCompat registers endpoints that are thin views over others: the GET
// molecular-data form, mRNA percentiles, and the /api/legacy/* aliases of the
// study-view endpoints.
func (s *State) routeCompat(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/molecular-profiles/{id}/molecular-data", s.molecularDataGet)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/mrna-percentile/fetch", s.mrnaPercentileFetch)
	mux.HandleFunc("/api/legacy/", func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/api/" + strings.TrimPrefix(r.URL.Path, "/api/legacy/")
		r2.URL.RawPath = ""
		mux.ServeHTTP(w, r2)
	})
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
// profile's numeric values for that gene (ties share their average rank) and
// reports rank/n as a percentile rounded to two decimals.
func (s *State) mrnaPercentileFetch(w http.ResponseWriter, r *http.Request) {
	var entrez []int64
	readBody(r, &entrez)
	sampleID := r.URL.Query().Get("sampleId")
	out := []MrnaPercentile{}
	data, err := s.molecularData(r.PathValue("id"), molecularDataFilter{EntrezGeneIDs: entrez})
	if err != nil {
		fail(w, err)
		return
	}
	byGene := map[int64][]float64{}
	for _, d := range data {
		if d.EntrezGeneID == nil {
			continue
		}
		if v, err := strconv.ParseFloat(d.Value, 64); err == nil && !math.IsNaN(v) {
			byGene[*d.EntrezGeneID] = append(byGene[*d.EntrezGeneID], v)
		}
	}
	for _, d := range data {
		if d.SampleID != sampleID || d.EntrezGeneID == nil {
			continue
		}
		v, err := strconv.ParseFloat(d.Value, 64)
		if err != nil || math.IsNaN(v) {
			continue
		}
		values := byGene[*d.EntrezGeneID]
		sorted := append([]float64(nil), values...)
		sort.Float64s(sorted)
		lo := sort.SearchFloat64s(sorted, v)
		hi := lo
		for hi < len(sorted) && sorted[hi] == v {
			hi++
		}
		rank := float64(lo+1+hi) / 2
		pct := math.Round(rank/float64(len(sorted))*100*100) / 100
		out = append(out, MrnaPercentile{
			MolecularProfileID: d.MolecularProfileID, SampleID: d.SampleID, PatientID: d.PatientID, StudyID: d.StudyID,
			EntrezGeneID: *d.EntrezGeneID, Percentile: pct, ZScore: v,
			UniqueSampleKey: d.UniqueSampleKey, UniquePatientKey: d.UniquePatientKey,
		})
	}
	writeJSON(w, out)
}
