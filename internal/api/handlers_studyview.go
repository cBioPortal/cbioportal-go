package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

func (s *State) filteredSamplesFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	s.writeSamples(w, fmt.Sprintf("concat(s.study_id, '_', s.sample_id) IN (%s)", s.filterSQL(f)))
}

// ---------- Clinical data counts ----------

type ClinicalDataCount struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}
type ClinicalDataCountItem struct {
	AttributeID string              `json:"attributeId"`
	Counts      []ClinicalDataCount `json:"counts"`
}

func (s *State) clinicalDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Attributes []struct {
			AttributeID string `json:"attributeId"`
		} `json:"attributes"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	readBody(r, &f)
	svf := f.StudyViewFilter
	if len(f.Attributes) == 1 {
		svf = svf.withoutClinical(f.Attributes[0].AttributeID)
	}
	ids := make([]string, len(f.Attributes))
	for i, a := range f.Attributes {
		ids[i] = a.AttributeID
	}
	writeJSON(w, s.clinicalDataCounts(svf, ids))
}

// clinicalDataCounts counts the values of each attribute over the filtered
// samples (sample attributes) or patients (patient attributes), with the
// remainder reported as NA. An attribute defined at both levels in the
// involved studies is counted per sample.
func (s *State) clinicalDataCounts(f studyViewFilter, attrIDs []string) []ClinicalDataCountItem {
	out := []ClinicalDataCountItem{}
	studies := f.studyIDs()
	if len(attrIDs) == 0 || len(studies) == 0 {
		return out
	}
	type levelRow struct {
		AttrID  string `json:"attrId"`
		Patient uint8  `json:"patient"`
		Sample  uint8  `json:"sample"`
	}
	levels, _ := ch.Query[levelRow](s.CH, fmt.Sprintf(
		"SELECT clinical_attribute_id AS attrId, max(patient_attribute = 1) AS patient, max(patient_attribute = 0) AS sample "+
			"FROM clinical_attribute_meta WHERE study_id IN (%s) AND clinical_attribute_id IN (%s) GROUP BY clinical_attribute_id",
		inList(studies), inList(attrIDs)))
	var sampleAttrs, patientAttrs, conflicting []string
	isPatient := map[string]bool{}
	for _, l := range levels {
		switch {
		case l.Patient == 1 && l.Sample == 1:
			conflicting = append(conflicting, l.AttrID)
		case l.Patient == 1:
			patientAttrs = append(patientAttrs, l.AttrID)
			isPatient[l.AttrID] = true
		default:
			sampleAttrs = append(sampleAttrs, l.AttrID)
		}
	}
	filtered := s.filterSQL(f)
	notNA := "NOT " + isNASQL("attribute_value")
	type countRow struct {
		AttrID string `json:"attrId"`
		Value  string `json:"value"`
		C      uint64 `json:"c"`
	}
	var queries []string
	if len(sampleAttrs) > 0 {
		queries = append(queries, fmt.Sprintf(
			"SELECT attribute_name AS attrId, attribute_value AS value, count() AS c FROM clinical_data_derived "+
				"WHERE type = 'sample' AND %s AND sample_unique_id IN (%s) AND attribute_name IN (%s) GROUP BY attribute_name, attribute_value",
			notNA, filtered, inList(sampleAttrs)))
	}
	if len(patientAttrs) > 0 {
		queries = append(queries, fmt.Sprintf(
			"SELECT attribute_name AS attrId, attribute_value AS value, count() AS c FROM clinical_data_derived "+
				"WHERE type = 'patient' AND %s AND patient_unique_id IN (SELECT patient_unique_id FROM sample_derived WHERE sample_unique_id IN (%s)) "+
				"AND attribute_name IN (%s) GROUP BY attribute_name, attribute_value",
			notNA, filtered, inList(patientAttrs)))
	}
	if len(conflicting) > 0 {
		queries = append(queries, fmt.Sprintf(
			"SELECT attribute_name AS attrId, attribute_value AS value, count() AS c FROM ("+
				"SELECT attribute_name, attribute_value, sample_unique_id FROM clinical_data_derived "+
				"WHERE type = 'sample' AND %[1]s AND sample_unique_id IN (%[2]s) AND attribute_name IN (%[3]s) "+
				"UNION ALL SELECT cdd.attribute_name, cdd.attribute_value, sd.sample_unique_id FROM clinical_data_derived cdd "+
				"JOIN sample_derived sd ON cdd.patient_unique_id = sd.patient_unique_id "+
				"WHERE cdd.type = 'patient' AND %[1]s AND sd.sample_unique_id IN (%[2]s) AND cdd.attribute_name IN (%[3]s)"+
				") GROUP BY attribute_name, attribute_value",
			notNA, filtered, inList(conflicting)))
	}
	counts := map[string]map[string]*ClinicalDataCount{}
	sums := map[string]uint64{}
	for _, q := range queries {
		rows, err := ch.Query[countRow](s.CH, q)
		if err != nil {
			continue
		}
		for _, r := range rows {
			byLower := counts[r.AttrID]
			if byLower == nil {
				byLower = map[string]*ClinicalDataCount{}
				counts[r.AttrID] = byLower
			}
			k := strings.ToLower(r.Value)
			if c, ok := byLower[k]; ok {
				c.Count += r.C
				if r.Value > c.Value {
					c.Value = r.Value
				}
			} else {
				byLower[k] = &ClinicalDataCount{Value: r.Value, Count: r.C}
			}
			sums[r.AttrID] += r.C
		}
	}
	nSamples := s.scopeSampleCount(f)
	var nPatients uint64
	if len(patientAttrs) > 0 {
		type c struct {
			N uint64 `json:"n"`
		}
		rows, _ := ch.Query[c](s.CH, fmt.Sprintf(
			"SELECT uniqExact(patient_unique_id) AS n FROM sample_derived WHERE sample_unique_id IN (%s)", filtered))
		if len(rows) > 0 {
			nPatients = rows[0].N
		}
	}
	for _, l := range levels {
		total := nSamples
		if isPatient[l.AttrID] {
			total = nPatients
		}
		// Order as Java's two HashMap groupings do: by lower-cased value, then by
		// value (NA included); the frontend keeps this order for equal counts.
		byValue := map[string]ClinicalDataCount{}
		var values []string
		for _, k := range javaHashMapOrder(sortedKeys(counts[l.AttrID])) {
			c := *counts[l.AttrID][k]
			byValue[c.Value] = c
			values = append(values, c.Value)
		}
		if total > sums[l.AttrID] {
			byValue["NA"] = ClinicalDataCount{Value: "NA", Count: total - sums[l.AttrID]}
			values = append(values, "NA")
		}
		item := ClinicalDataCountItem{AttributeID: l.AttrID, Counts: []ClinicalDataCount{}}
		for _, v := range javaHashMapOrder(values) {
			item.Counts = append(item.Counts, byValue[v])
		}
		out = append(out, item)
	}
	return out
}

// sortedKeys returns the map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------- Gene alteration counts ----------

type geneCountRow struct {
	HugoGeneSymbol       string `json:"hugoGeneSymbol"`
	EntrezGeneID         *int64 `json:"entrezGeneId"`
	Alteration           *int64 `json:"alteration"`
	NumberOfAlteredCases uint64 `json:"numberOfAlteredCases"`
	TotalCount           uint64 `json:"totalCount"`
}

type AlterationCountByGene struct {
	EntrezGeneID          *int64 `json:"entrezGeneId"`
	HugoGeneSymbol        string `json:"hugoGeneSymbol"`
	Alteration            *int64 `json:"alteration,omitempty"`
	NumberOfAlteredCases  uint64 `json:"numberOfAlteredCases"`
	NumberOfProfiledCases uint64 `json:"numberOfProfiledCases"`
	TotalCount            uint64 `json:"totalCount"`
}

func toAlterationCounts(rows []geneCountRow, profiled uint64, keepAlteration bool) []AlterationCountByGene {
	out := make([]AlterationCountByGene, len(rows))
	for i, r := range rows {
		a := AlterationCountByGene{
			EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol,
			NumberOfAlteredCases: r.NumberOfAlteredCases, NumberOfProfiledCases: profiled, TotalCount: r.TotalCount,
		}
		if keepAlteration {
			a.Alteration = r.Alteration
		}
		out[i] = a
	}
	return out
}

// ---------- Data types + structural variants ----------

type GenomicDataCount struct {
	Value       string   `json:"value"`
	Label       string   `json:"label"`
	Count       uint64   `json:"count"`
	UniqueCount uint64   `json:"uniqueCount,omitempty"`
	SampleIDs   []string `json:"sampleIds,omitempty"`
}

type svRow struct {
	Gene1                string `json:"gene1"`
	Gene1Entrez          *int64 `json:"gene1Entrez"`
	Gene2                string `json:"gene2"`
	Gene2Entrez          *int64 `json:"gene2Entrez"`
	NumberOfAlteredCases uint64 `json:"numberOfAlteredCases"`
	TotalCount           uint64 `json:"totalCount"`
}

type AlterationCountByStructuralVariant struct {
	Gene1HugoGeneSymbol   string   `json:"gene1HugoGeneSymbol"`
	Gene1EntrezGeneID     *int64   `json:"gene1EntrezGeneId"`
	Gene2HugoGeneSymbol   string   `json:"gene2HugoGeneSymbol"`
	Gene2EntrezGeneID     *int64   `json:"gene2EntrezGeneId"`
	HugoGeneSymbols       []string `json:"hugoGeneSymbols"`
	EntrezGeneIDs         []int64  `json:"entrezGeneIds"`
	NumberOfAlteredCases  uint64   `json:"numberOfAlteredCases"`
	NumberOfProfiledCases uint64   `json:"numberOfProfiledCases"`
	TotalCount            uint64   `json:"totalCount"`
	UniqueEventKey        string   `json:"uniqueEventKey"`
}
