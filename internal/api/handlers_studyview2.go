package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeStudyView2 registers the remaining study-view aggregation endpoints:
// numeric clinical bins, mutation/genomic data counts, treatments, and
// case-list (sample-list) counts.
func (s *State) routeStudyView2(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/clinical-data-bin-counts/fetch", s.clinicalDataBinCountsFetch)
	mux.HandleFunc("POST /api/mutation-data-counts/fetch", s.mutationDataCountsFetch)
	mux.HandleFunc("POST /api/genomic-data-counts/fetch", s.genomicDataCountsFetch)
	mux.HandleFunc("POST /api/genomic-data-bin-counts/fetch", s.genomicDataBinCountsFetch)
	mux.HandleFunc("POST /api/treatments/patient", s.treatmentsPatient)
	mux.HandleFunc("POST /api/treatments/sample", s.treatmentsSample)
	mux.HandleFunc("POST /api/sample-lists-counts/fetch", s.sampleListsCountsFetch)
}

// studyIDs derives the distinct study ids referenced by the filter, from the
// explicit study list or the sample identifiers.
func (f studyViewFilter) studyIDs() []string {
	if len(f.StudyIDs) > 0 {
		return f.StudyIDs
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range f.SampleIdentifiers {
		if id.StudyID != "" && !seen[id.StudyID] {
			seen[id.StudyID] = true
			out = append(out, id.StudyID)
		}
	}
	return out
}

// ---------- Clinical data bin counts ----------

// ClinicalDataBin is one histogram bucket for a numeric clinical attribute.
// A ranged bucket carries start/end; a categorical/NA bucket carries a
// specialValue instead.
type ClinicalDataBin struct {
	AttributeID  string   `json:"attributeId"`
	SpecialValue string   `json:"specialValue,omitempty"`
	Start        *float64 `json:"start,omitempty"`
	End          *float64 `json:"end,omitempty"`
	Count        uint64   `json:"count"`
}

func (s *State) clinicalDataBinCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Attributes []struct {
			AttributeID string `json:"attributeId"`
		} `json:"attributes"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	readBody(r, &f)

	studyIDs := f.StudyViewFilter.studyIDs()
	out := []ClinicalDataBin{}
	if len(f.Attributes) == 0 || len(studyIDs) == 0 {
		writeJSON(w, out)
		return
	}

	// Resolve which attributes are numeric and whether they are patient-level.
	type metaRow struct {
		AttrID     string `json:"attrId"`
		Datatype   string `json:"datatype"`
		PatientAtt uint8  `json:"patientAtt"`
	}
	attrIDs := make([]string, len(f.Attributes))
	for i, a := range f.Attributes {
		attrIDs[i] = a.AttributeID
	}
	metaRows, _ := ch.Query[metaRow](s.CH, fmt.Sprintf(
		"SELECT clinical_attribute_id AS attrId, any(datatype) AS datatype, max(patient_attribute) AS patientAtt "+
			"FROM clinical_attribute_meta WHERE study_id IN (%s) AND clinical_attribute_id IN (%s) "+
			"GROUP BY clinical_attribute_id", inList(studyIDs), inList(attrIDs)))
	meta := map[string]metaRow{}
	for _, m := range metaRows {
		meta[m.AttrID] = m
	}

	sampleScope := f.StudyViewFilter.sampleDerivedScope("sample_unique_id")
	patientScope := f.StudyViewFilter.sampleDerivedScope("patient_unique_id")

	for _, a := range f.Attributes {
		m, ok := meta[a.AttributeID]
		if !ok || !strings.EqualFold(m.Datatype, "NUMBER") {
			continue
		}
		typ, scope := "sample", sampleScope
		if m.PatientAtt == 1 {
			typ, scope = "patient", patientScope
		}
		rows, _ := ch.Query[binValRow](s.CH, fmt.Sprintf(
			"SELECT attribute_value AS value, count() AS c FROM clinical_data_derived "+
				"WHERE type = '%s' AND %s AND attribute_name = '%s' GROUP BY attribute_value",
			typ, scope, sqlLit(a.AttributeID)))
		out = append(out, binNumericValues(a.AttributeID, rows)...)
	}
	writeJSON(w, out)
}

// binValRow is a grouped clinical value with its occurrence count.
type binValRow struct {
	Value string `json:"value"`
	C     uint64 `json:"c"`
}

// binNumericValues buckets grouped (value,count) rows into ~10 equal-width bins
// plus an NA bucket for values that are not finite numbers.
func binNumericValues(attrID string, rows []binValRow) []ClinicalDataBin {
	type num struct {
		v float64
		c uint64
	}
	var nums []num
	var na uint64
	minV, maxV := 0.0, 0.0
	first := true
	for _, r := range rows {
		v, err := strconv.ParseFloat(strings.TrimSpace(r.Value), 64)
		if err != nil || r.Value == "" {
			na += r.C
			continue
		}
		nums = append(nums, num{v, r.C})
		if first {
			minV, maxV, first = v, v, false
		} else {
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
	}

	var out []ClinicalDataBin
	const nbins = 10
	if len(nums) > 0 {
		if minV == maxV {
			var total uint64
			for _, n := range nums {
				total += n.c
			}
			lo, hi := minV, maxV
			out = append(out, ClinicalDataBin{AttributeID: attrID, Start: &lo, End: &hi, Count: total})
		} else {
			width := (maxV - minV) / nbins
			counts := make([]uint64, nbins)
			for _, n := range nums {
				idx := int((n.v - minV) / width)
				if idx >= nbins {
					idx = nbins - 1
				}
				if idx < 0 {
					idx = 0
				}
				counts[idx] += n.c
			}
			for i := 0; i < nbins; i++ {
				if counts[i] == 0 {
					continue
				}
				lo := minV + float64(i)*width
				hi := minV + float64(i+1)*width
				loCopy, hiCopy := lo, hi
				out = append(out, ClinicalDataBin{AttributeID: attrID, Start: &loCopy, End: &hiCopy, Count: counts[i]})
			}
		}
	}
	if na > 0 {
		out = append(out, ClinicalDataBin{AttributeID: attrID, SpecialValue: "NA", Count: na})
	}
	return out
}

// ---------- Mutation data counts ----------

// GenomicDataCountItem groups the per-category counts for one gene/profile.
type GenomicDataCountItem struct {
	HugoGeneSymbol string             `json:"hugoGeneSymbol"`
	ProfileType    string             `json:"profileType"`
	Counts         []GenomicDataCount `json:"counts"`
}

func (s *State) mutationDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		GenomicDataFilters []struct {
			HugoGeneSymbol string `json:"hugoGeneSymbol"`
			ProfileType    string `json:"profileType"`
		} `json:"genomicDataFilters"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	readBody(r, &f)
	scope := f.StudyViewFilter.sampleScope()
	profiled := s.scopeSampleCount(f.StudyViewFilter)

	out := []GenomicDataCountItem{}
	type c struct {
		N uint64 `json:"n"`
	}
	for _, g := range f.GenomicDataFilters {
		if g.HugoGeneSymbol == "" {
			continue
		}
		rows, _ := ch.Query[c](s.CH, fmt.Sprintf(
			"SELECT uniqExact(sample_id) AS n FROM mutation WHERE %s AND hugo_symbol = '%s'",
			scope, sqlLit(g.HugoGeneSymbol)))
		var mutated uint64
		if len(rows) > 0 {
			mutated = rows[0].N
		}
		notMutated := uint64(0)
		if profiled > mutated {
			notMutated = profiled - mutated
		}
		out = append(out, GenomicDataCountItem{
			HugoGeneSymbol: g.HugoGeneSymbol,
			ProfileType:    g.ProfileType,
			Counts: []GenomicDataCount{
				{Value: "Mutated", Label: "Mutated", Count: mutated, UniqueCount: mutated, SampleIDs: []string{}},
				{Value: "Not Mutated", Label: "Not Mutated", Count: notMutated, UniqueCount: notMutated, SampleIDs: []string{}},
			},
		})
	}
	writeJSON(w, out)
}

// ---------- Genomic (molecular) data counts / bins ----------

func (s *State) genomicDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	readBody(r, nil)
	writeJSON(w, []GenomicDataCountItem{})
}

// GenomicDataBin is one histogram bucket for a numeric molecular profile.
type GenomicDataBin struct {
	HugoGeneSymbol string   `json:"hugoGeneSymbol"`
	ProfileType    string   `json:"profileType"`
	SpecialValue   string   `json:"specialValue,omitempty"`
	Start          *float64 `json:"start,omitempty"`
	End            *float64 `json:"end,omitempty"`
	Count          uint64   `json:"count"`
}

func (s *State) genomicDataBinCountsFetch(w http.ResponseWriter, r *http.Request) {
	readBody(r, nil)
	writeJSON(w, []GenomicDataBin{})
}

// ---------- Treatments (no treatment data loaded) ----------

func (s *State) treatmentsPatient(w http.ResponseWriter, r *http.Request) {
	readBody(r, nil)
	writeJSON(w, []any{})
}

func (s *State) treatmentsSample(w http.ResponseWriter, r *http.Request) {
	readBody(r, nil)
	writeJSON(w, []any{})
}

// ---------- Sample list (case list) counts ----------

// CaseListDataCount is the count of filtered samples in one case list.
type CaseListDataCount struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count uint64 `json:"count"`
}

func (s *State) sampleListsCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	scope := f.sampleScope()
	rows, err := ch.Query[CaseListDataCount](s.CH, fmt.Sprintf(
		"SELECT list_id AS value, any(name) AS label, uniqExact(sample_id) AS count "+
			"FROM sample_list WHERE %s GROUP BY list_id ORDER BY count DESC", scope))
	if err != nil {
		fail(w, err)
		return
	}
	if rows == nil {
		rows = []CaseListDataCount{}
	}
	writeJSON(w, rows)
}
