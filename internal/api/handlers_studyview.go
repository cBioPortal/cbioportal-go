package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// filteredSample is a sample as ColumnarStoreStudyViewController's
// filtered-samples endpoint writes it: the mapper selects ids only, so the
// sample type and the sequenced / segment flags are absent.
type filteredSample struct {
	SampleID         string `json:"sampleId"`
	PatientID        string `json:"patientId"`
	StudyID          string `json:"studyId"`
	UniqueSampleKey  string `json:"uniqueSampleKey"`
	UniquePatientKey string `json:"uniquePatientKey"`
}

// filteredSamplesFetch serves POST /api/filtered-samples/fetch: the samples
// passing the study-view filter, by sample id (negateFilters is
// accepted and ignored, as in Java).
func (s *State) filteredSamplesFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)
	args := newArgs()
	rows, err := queryA[idRow](s.CH, fmt.Sprintf(
		"SELECT s.study_id AS studyId, s.sample_id AS sampleId, s.patient_id AS patientId FROM sample s "+
			"WHERE s.study_id IN %s AND concat(s.study_id, '_', s.sample_id) IN (%s) ORDER BY s.sample_id, s.study_id",
		args.strs(f.scopeStudies()), s.filterSQL(args, f)), args)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]filteredSample, len(rows))
	for i, r := range rows {
		out[i] = filteredSample{
			SampleID: r.SampleID, PatientID: r.PatientID, StudyID: r.StudyID,
			UniqueSampleKey: uniqueKey(r.StudyID, r.SampleID), UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
		}
	}
	writeJSON(w, out)
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
	writeJSON(w, s.clinicalDataCountsWithMissing(svf, ids))
}

// clinicalDataCountsWithMissing is GetClinicalDataCountsUseCase: the counts of
// the attributes that have a value among the filtered cases and, when those
// are fewer than the requested attribute ids (duplicates included), an NA
// count for every involved study's definition of each attribute without one
// (StudyViewColumnarServiceUtil.addClinicalDataCountsForMissingAttributes):
// all filtered patients for a patient attribute, all filtered samples
// otherwise, once per study defining it.
func (s *State) clinicalDataCountsWithMissing(f studyViewFilter, attrIDs []string) []ClinicalDataCountItem {
	all := s.clinicalDataCounts(f, attrIDs)
	out := []ClinicalDataCountItem{}
	withData := map[string]bool{}
	for _, item := range all {
		for _, c := range item.Counts {
			if c.Value != "NA" {
				withData[item.AttributeID] = true
				out = append(out, item)
				break
			}
		}
	}
	studies := f.studyIDs()
	if len(out) == len(attrIDs) || len(studies) == 0 {
		return out
	}
	type defRow struct {
		AttrID  string `json:"attrId"`
		Patient uint8  `json:"patient"`
	}
	args := newArgs()
	defs, err := queryA[defRow](s.CH, fmt.Sprintf(
		"SELECT clinical_attribute_id AS attrId, toUInt8(patient_attribute = 1) AS patient FROM clinical_attribute_meta "+
			"WHERE study_id IN %s AND clinical_attribute_id IN %s ORDER BY clinical_attribute_id, study_id",
		args.strs(studies), args.strs(attrIDs)), args)
	if err != nil {
		return out
	}
	type c struct {
		Samples  uint64 `json:"samples"`
		Patients uint64 `json:"patients"`
	}
	n, _ := queryA[c](s.CH, "SELECT count() AS samples, uniqExact(patient_unique_id) AS patients FROM sample_derived WHERE "+
		s.filterWhere(args, f), args)
	var total c
	if len(n) > 0 {
		total = n[0]
	}
	for _, d := range defs {
		if withData[d.AttrID] {
			continue
		}
		count := total.Samples
		if d.Patient == 1 {
			count = total.Patients
		}
		out = append(out, ClinicalDataCountItem{AttributeID: d.AttrID, Counts: []ClinicalDataCount{{Value: "NA", Count: count}}})
	}
	return out
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
	args := newArgs()
	levels, _ := queryA[levelRow](s.CH, fmt.Sprintf(
		"SELECT clinical_attribute_id AS attrId, max(patient_attribute = 1) AS patient, max(patient_attribute = 0) AS sample "+
			"FROM clinical_attribute_meta WHERE study_id IN %s AND clinical_attribute_id IN %s GROUP BY clinical_attribute_id",
		args.strs(studies), args.strs(attrIDs)), args)
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
	filtered := s.filterSQL(args, f)
	notNA := "NOT " + isNASQL("attribute_value")
	key := f.studyKey(args, "cancer_study_identifier")
	type countRow struct {
		AttrID string `json:"attrId"`
		Value  string `json:"value"`
		C      uint64 `json:"c"`
	}
	var queries []string
	if len(sampleAttrs) > 0 {
		queries = append(queries, fmt.Sprintf(
			"SELECT attribute_name AS attrId, attribute_value AS value, count() AS c FROM clinical_data_derived "+
				"WHERE %stype = 'sample' AND %s AND sample_unique_id IN (%s) AND attribute_name IN %s GROUP BY attribute_name, attribute_value",
			key, notNA, filtered, args.strs(sampleAttrs)))
	}
	if len(patientAttrs) > 0 {
		queries = append(queries, fmt.Sprintf(
			"SELECT attribute_name AS attrId, attribute_value AS value, count() AS c FROM clinical_data_derived "+
				"WHERE %[1]stype = 'patient' AND %[2]s AND patient_unique_id IN (SELECT patient_unique_id FROM sample_derived WHERE %[1]ssample_unique_id IN (%[3]s)) "+
				"AND attribute_name IN %[4]s GROUP BY attribute_name, attribute_value",
			key, notNA, filtered, args.strs(patientAttrs)))
	}
	if len(conflicting) > 0 {
		queries = append(queries, fmt.Sprintf(
			"SELECT attribute_name AS attrId, attribute_value AS value, count() AS c FROM ("+
				"SELECT attribute_name, attribute_value, sample_unique_id FROM clinical_data_derived "+
				"WHERE %[4]stype = 'sample' AND %[1]s AND sample_unique_id IN (%[2]s) AND attribute_name IN %[3]s "+
				"UNION ALL SELECT cdd.attribute_name, cdd.attribute_value, sd.sample_unique_id FROM (SELECT * FROM clinical_data_derived WHERE %[4]s1) cdd "+
				"JOIN (SELECT * FROM sample_derived WHERE %[4]s1) sd ON cdd.patient_unique_id = sd.patient_unique_id "+
				"WHERE cdd.type = 'patient' AND %[1]s AND sd.sample_unique_id IN (%[2]s) AND cdd.attribute_name IN %[3]s"+
				") GROUP BY attribute_name, attribute_value",
			notNA, filtered, args.strs(conflicting), key))
	}
	counts := map[string]map[string]*ClinicalDataCount{}
	sums := map[string]uint64{}
	for _, q := range queries {
		rows, err := queryA[countRow](s.CH, q, args)
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
		rows, _ := queryA[c](s.CH, fmt.Sprintf(
			"SELECT uniqExact(patient_unique_id) AS n FROM sample_derived WHERE %ssample_unique_id IN (%s)", key, filtered), args)
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
	EntrezGeneID          *int64 `json:"entrezGeneId,omitempty"`
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
	Label       string   `json:"label"`
	Value       string   `json:"value"`
	Count       uint64   `json:"count"`
	UniqueCount *uint64  `json:"uniqueCount,omitempty"`
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
	Gene1EntrezGeneID     *int64   `json:"gene1EntrezGeneId,omitempty"`
	Gene2HugoGeneSymbol   string   `json:"gene2HugoGeneSymbol"`
	Gene2EntrezGeneID     *int64   `json:"gene2EntrezGeneId,omitempty"`
	HugoGeneSymbols       []string `json:"hugoGeneSymbols"`
	EntrezGeneIDs         []int64  `json:"entrezGeneIds"`
	NumberOfAlteredCases  uint64   `json:"numberOfAlteredCases"`
	NumberOfProfiledCases uint64   `json:"numberOfProfiledCases"`
	TotalCount            uint64   `json:"totalCount"`
	UniqueEventKey        string   `json:"uniqueEventKey"`
}
