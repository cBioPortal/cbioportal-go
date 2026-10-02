package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/stats"
)

// routeStudyView3 registers study-view endpoints for clinical-data enrichments,
// the clinical-data table, custom (session) charts, namespace (custom-column)
// attributes, and MutSig/GISTIC significance lists.
func (s *State) routeStudyView3(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/clinical-data-enrichments/fetch", s.clinicalDataEnrichmentsFetch)
	mux.HandleFunc("POST /api/clinical-data-table/fetch", s.clinicalDataTableFetch)

	// Custom (session-defined) chart data is not persisted in this backend, so
	// these return correctly-typed empty results after draining the body.
	mux.HandleFunc("POST /api/custom-data-counts/fetch", s.customDataCountsFetch)
	mux.HandleFunc("POST /api/custom-data-bin-counts/fetch", s.customDataBinCountsFetch)

	// Namespace (custom-column) attributes are not modeled in staging — the
	// clinical_attribute_meta view carries no namespace-prefixed attributes —
	// so these return typed empties.
	mux.HandleFunc("POST /api/namespace-attributes/fetch", emptyArray)
	mux.HandleFunc("POST /api/namespace-attributes/counts/fetch", emptyArray)
	mux.HandleFunc("POST /api/namespace-data-counts/fetch", emptyArray)
	mux.HandleFunc("POST /api/namespace-data/fetch", emptyArray)

}

// naFilter is the cBioPortal rule for excluding "not available" clinical values,
// mirroring the backend's isAttributeValueNA SQL fragment.
func naFilter(col string) string {
	return fmt.Sprintf("NOT (%s = '' OR upperUTF8(%s) = 'NA' OR upperUTF8(%s) = 'NAN' OR upperUTF8(%s) = 'N/A')", col, col, col, col)
}

// ---------- Clinical data enrichments ----------

// ClinicalDataEnrichment is one attribute's group-comparison result: a
// Kruskal-Wallis/Wilcoxon test for numeric attributes or a chi-squared test for
// categorical ones.
type ClinicalDataEnrichment struct {
	ClinicalAttribute ClinicalAttribute `json:"clinicalAttribute"`
	Method            string            `json:"method"`
	PValue            float64           `json:"pValue"`
	QValue            *float64          `json:"qValue"`
	Score             float64           `json:"score"`
}

type enrichAttr struct {
	attr    ClinicalAttribute
	numeric bool
	patient bool
}

func (s *State) clinicalDataEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Groups []struct {
			Name              string       `json:"name"`
			SampleIdentifiers []identifier `json:"sampleIdentifiers"`
		} `json:"groups"`
	}
	readBody(r, &f)

	out := []ClinicalDataEnrichment{}
	if len(f.Groups) < 2 {
		writeJSON(w, out)
		return
	}

	// Each group's samples as a predicate over the sample view. Every query
	// below spells each group's sample list once (as the group_samples CTE) and
	// resolves the groups' samples and patients inside ClickHouse.
	var groupSelects []string
	for gi, g := range f.Groups {
		groupSelects = append(groupSelects, fmt.Sprintf(
			"SELECT toUInt32(%d) AS grp, study_id, sample_id, patient_id FROM sample WHERE %s",
			gi, compactSampleScope(g.SampleIdentifiers)))
	}
	withGroups := "WITH group_samples AS (" + strings.Join(groupSelects, " UNION ALL ") + ") "

	// Load attribute metadata for the involved studies (dedup by attribute id).
	attrRows, err := ch.Query[clinAttrRow](s.CH, fmt.Sprintf(
		"%sSELECT study_id AS studyId, clinical_attribute_id AS clinicalAttributeId, display_name AS displayName, "+
			"description, datatype, priority, patient_attribute AS patientFlag FROM clinical_attribute_meta "+
			"WHERE study_id IN (SELECT DISTINCT study_id FROM group_samples)",
		withGroups))
	if err != nil {
		fail(w, err)
		return
	}
	attrs := map[string]enrichAttr{}
	var attrOrder []string
	for _, a := range attrRows {
		if _, ok := attrs[a.ClinicalAttributeID]; ok {
			continue
		}
		dt := a.Datatype
		if dt == "" {
			dt = "STRING"
		}
		pr := a.Priority
		if pr == "" {
			pr = "1"
		}
		attrOrder = append(attrOrder, a.ClinicalAttributeID)
		attrs[a.ClinicalAttributeID] = enrichAttr{
			attr: ClinicalAttribute{
				ClinicalAttributeID: a.ClinicalAttributeID, DisplayName: a.DisplayName, Description: a.Description,
				Datatype: dt, PatientAttribute: a.PatientFlag != 0, Priority: pr, StudyID: a.StudyID,
			},
			numeric: strings.EqualFold(dt, "NUMBER"),
			patient: a.PatientFlag != 0,
		}
	}
	if len(attrOrder) == 0 {
		writeJSON(w, out)
		return
	}
	sort.Strings(attrOrder)

	var numSample, numPatient, catSample, catPatient []string
	for _, id := range attrOrder {
		a := attrs[id]
		switch {
		case a.numeric && a.patient:
			numPatient = append(numPatient, id)
		case a.numeric:
			numSample = append(numSample, id)
		case a.patient:
			catPatient = append(catPatient, id)
		default:
			catSample = append(catSample, id)
		}
	}

	// groupValues selects (attr_id, grp, v) for the given sample- and
	// patient-level attributes over every group's samples, counting a
	// patient-level value once per patient and group. valueOf renders the value
	// (and cond the row filter) for an attr_value column. It reads the
	// group_samples CTE once, so ClickHouse builds the groups' sample sets once.
	groupValues := func(valueOf, cond func(col string) string, sampleIDs, patientIDs []string) string {
		var parts []string
		if len(sampleIDs) > 0 {
			parts = append(parts, fmt.Sprintf(
				"SELECT study_id, sample_id, attr_id, %s AS v, sample_id AS k FROM clinical_sample WHERE attr_id IN (%s) AND %s",
				valueOf("attr_value"), inList(sampleIDs), cond("attr_value")))
		}
		if len(patientIDs) > 0 {
			parts = append(parts, fmt.Sprintf(
				"SELECT s.study_id, s.sample_id, c.attr_id, %s, c.patient_id FROM clinical_patient c "+
					"INNER JOIN sample s ON s.study_id = c.study_id AND s.patient_id = c.patient_id WHERE c.attr_id IN (%s) AND %s",
				valueOf("c.attr_value"), inList(patientIDs), cond("c.attr_value")))
		}
		return fmt.Sprintf(
			"SELECT attr_id, grp, v FROM (SELECT c.attr_id AS attr_id, g.grp AS grp, c.v AS v, c.study_id AS st, c.k AS k "+
				"FROM group_samples g INNER JOIN (%s) c ON c.study_id = g.study_id AND c.sample_id = g.sample_id "+
				"LIMIT 1 BY grp, attr_id, st, k)", strings.Join(parts, " UNION ALL "))
	}

	// Numeric attributes: per attribute and group, the number of values and the
	// sum of their tie-averaged ranks among all groups' values, plus the number
	// of distinct values, computed in ClickHouse.
	numRanks := map[string][]groupRankRow{}
	if len(numSample)+len(numPatient) > 0 {
		rows, err := ch.Query[groupRankRow](s.CH, fmt.Sprintf(
			"%sSELECT attr_id, grp, count() AS n, sum(r) AS rs, any(nd) AS nd FROM ("+
				"SELECT attr_id, grp, rank() OVER (PARTITION BY attr_id ORDER BY v) + (count() OVER (PARTITION BY attr_id, v) - 1) / 2 AS r, "+
				"uniqExact(v) OVER (PARTITION BY attr_id) AS nd FROM (%s)"+
				") GROUP BY attr_id, grp ORDER BY attr_id, grp",
			withGroups, groupValues(func(col string) string { return "toFloat64(" + col + ")" },
				func(col string) string { return "toFloat64OrNull(" + col + ") IS NOT NULL" }, numSample, numPatient)))
		if err != nil {
			fail(w, err)
			return
		}
		for _, rw := range rows {
			numRanks[rw.AttrID] = append(numRanks[rw.AttrID], rw)
		}
	}

	// Categorical attributes: per attribute, group and value, the case count.
	type catRow struct {
		AttrID string `json:"attr_id"`
		Grp    uint32 `json:"grp"`
		Value  string `json:"value"`
		C      uint64 `json:"c"`
	}
	catData := make([]map[string]map[string]uint64, len(f.Groups)) // group -> attr -> value -> count
	for gi := range f.Groups {
		catData[gi] = map[string]map[string]uint64{}
	}
	if len(catSample)+len(catPatient) > 0 {
		rows, err := ch.Query[catRow](s.CH, fmt.Sprintf(
			"%sSELECT attr_id, grp, v AS value, count() AS c FROM (%s) GROUP BY attr_id, grp, v",
			withGroups, groupValues(func(col string) string { return col }, naFilter, catSample, catPatient)))
		if err != nil {
			fail(w, err)
			return
		}
		for _, rw := range rows {
			m := catData[rw.Grp]
			if m[rw.AttrID] == nil {
				m[rw.AttrID] = map[string]uint64{}
			}
			m[rw.AttrID][rw.Value] = rw.C
		}
	}

	// Numeric attributes: Kruskal-Wallis (Wilcoxon for exactly 2 groups with data).
	for _, id := range append(append([]string{}, numSample...), numPatient...) {
		groupRanks := numRanks[id]
		if len(groupRanks) < 2 || groupRanks[0].Distinct < 2 {
			continue
		}
		p := kruskalWallisFromRanks(groupRanks)
		if math.IsNaN(p) {
			continue
		}
		method := "Kruskal Wallis Test"
		if len(groupRanks) == 2 {
			method = "Wilcoxon Test"
		}
		out = append(out, ClinicalDataEnrichment{
			ClinicalAttribute: attrs[id].attr,
			Method:            method,
			PValue:            p,
			Score:             invChiSquare(p, len(groupRanks)-1),
		})
	}

	// Categorical attributes: chi-squared over the group x category contingency table.
	for _, id := range append(append([]string{}, catSample...), catPatient...) {
		catSet := map[string]bool{}
		for gi := range f.Groups {
			for v := range catData[gi][id] {
				catSet[v] = true
			}
		}
		if len(catSet) < 2 {
			continue
		}
		categories := make([]string, 0, len(catSet))
		for v := range catSet {
			categories = append(categories, v)
		}
		sort.Strings(categories)

		var table [][]float64
		for gi := range f.Groups {
			row := make([]float64, len(categories))
			nonzero := false
			for j, cat := range categories {
				c := catData[gi][id][cat]
				row[j] = float64(c)
				if c > 0 {
					nonzero = true
				}
			}
			if nonzero {
				table = append(table, row)
			}
		}
		if len(table) < 2 {
			continue
		}
		score, p := chiSquareContingency(table)
		out = append(out, ClinicalDataEnrichment{
			ClinicalAttribute: attrs[id].attr,
			Method:            "Chi-squared Test",
			PValue:            p,
			Score:             score,
		})
	}

	// Benjamini-Hochberg q-values across all tested attributes.
	if len(out) > 0 {
		idx := make([]int, len(out))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return out[idx[a]].PValue < out[idx[b]].PValue })
		sortedP := make([]float64, len(out))
		for rank, i := range idx {
			sortedP[rank] = out[i].PValue
		}
		q := benjaminiHochberg(sortedP)
		for rank, i := range idx {
			v := q[rank]
			out[i].QValue = &v
		}
	}

	writeJSON(w, out)
}

// compactSampleScope is a predicate over the sample view selecting the given
// samples, written per study as sample_id IN (...) to keep large groups well
// under ClickHouse's max_query_size.
func compactSampleScope(ids []identifier) string {
	byStudy := map[string][]string{}
	for _, id := range ids {
		byStudy[id.StudyID] = append(byStudy[id.StudyID], id.id())
	}
	if len(byStudy) == 0 {
		return falseClause
	}
	var ors []string
	for _, st := range sortedKeys(byStudy) {
		ors = append(ors, fmt.Sprintf("(study_id = '%s' AND sample_id IN (%s))", sqlLit(st), inList(byStudy[st])))
	}
	return strings.Join(ors, " OR ")
}

// groupRankRow is one numeric attribute's values within one group: their
// count, the sum of their tie-averaged ranks among all groups' values, and the
// number of distinct values across all groups.
type groupRankRow struct {
	AttrID   string  `json:"attr_id"`
	Grp      uint32  `json:"grp"`
	N        uint64  `json:"n"`
	RankSum  float64 `json:"rs"`
	Distinct uint64  `json:"nd"`
}

// kruskalWallisFromRanks is stats.KruskalWallisTest computed from the
// non-empty groups' value counts and rank sums, in group order:
// H = 12/(N(N+1)) * sum(R_i^2/n_i) - 3(N+1), tested against chi-square with
// k-1 degrees of freedom. Rank sums of tie-averaged ranks are exact in float64,
// so the result is bit-identical to ranking the raw values.
func kruskalWallisFromRanks(groups []groupRankRow) float64 {
	k := len(groups)
	if k < 2 {
		return 1.0
	}
	var n uint64
	for _, g := range groups {
		n += g.N
	}
	if n < 2 {
		return 1.0
	}
	N := float64(n)
	h := 0.0
	for _, g := range groups {
		ni := float64(g.N)
		if ni == 0 {
			continue
		}
		h += (g.RankSum * g.RankSum) / ni
	}
	h = (12.0/(N*(N+1)))*h - 3.0*(N+1)
	if math.IsNaN(h) || h < 0 {
		return 1.0
	}
	pValue := 1.0 - stats.ChiSquareCDF(h, float64(k-1))
	if math.IsNaN(pValue) {
		return 1.0
	}
	return pValue
}

// invChiSquare returns the chi-square statistic whose upper-tail probability
// equals pValue at df degrees of freedom (the inverse of the survival function),
// matching the backend's score for Kruskal-Wallis enrichments.
func invChiSquare(pValue float64, df int) float64 {
	if df < 1 {
		return 0
	}
	target := 1 - pValue
	d := float64(df)
	lo, hi := 0.0, 1.0
	for stats.ChiSquareCDF(hi, d) < target && hi < 1e12 {
		hi *= 2
	}
	for i := 0; i < 100; i++ {
		mid := (lo + hi) / 2
		if stats.ChiSquareCDF(mid, d) < target {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// chiSquareContingency computes the Pearson chi-square statistic and p-value for
// a groups x categories contingency table.
func chiSquareContingency(table [][]float64) (float64, float64) {
	rows := len(table)
	if rows < 2 {
		return 0, 1
	}
	cols := len(table[0])
	rowT := make([]float64, rows)
	colT := make([]float64, cols)
	total := 0.0
	for i := range table {
		for j := 0; j < cols; j++ {
			rowT[i] += table[i][j]
			colT[j] += table[i][j]
			total += table[i][j]
		}
	}
	if total == 0 {
		return 0, 1
	}
	chi := 0.0
	for i := range table {
		for j := 0; j < cols; j++ {
			exp := rowT[i] * colT[j] / total
			if exp > 0 {
				diff := table[i][j] - exp
				chi += diff * diff / exp
			}
		}
	}
	df := float64((rows - 1) * (cols - 1))
	if df < 1 {
		return chi, 1
	}
	p := 1 - stats.ChiSquareCDF(chi, df)
	if math.IsNaN(p) {
		p = 1
	}
	return chi, p
}

// ---------- Clinical data table (study-view Clinical tab) ----------

type sampleClinicalDataCollection struct {
	ByUniqueSampleKey map[string][]ClinicalData `json:"byUniqueSampleKey"`
}

func (s *State) clinicalDataTableFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	readBody(r, &f)

	q := r.URL.Query()
	pageSize := 10000000
	if v, err := strconv.Atoi(q.Get("pageSize")); err == nil && v > 0 {
		pageSize = v
	}
	pageNumber := 0
	if v, err := strconv.Atoi(q.Get("pageNumber")); err == nil && v >= 0 {
		pageNumber = v
	}
	searchTerm := q.Get("searchTerm")
	sortBy := q.Get("sortBy")
	dir := "ASC"
	if strings.EqualFold(q.Get("direction"), "DESC") {
		dir = "DESC"
	}

	empty := sampleClinicalDataCollection{ByUniqueSampleKey: map[string][]ClinicalData{}}

	inner := fmt.Sprintf("SELECT study_id, sample_id, patient_id FROM sample WHERE %s", s.sampleScope(f))
	if searchTerm != "" {
		like := "'%" + sqlLit(searchTerm) + "%'"
		inner += fmt.Sprintf(
			" AND (sample_id ILIKE %s OR patient_id ILIKE %s"+
				" OR (study_id, sample_id) IN (SELECT study_id, sample_id FROM clinical_sample WHERE attr_value ILIKE %s)"+
				" OR (study_id, patient_id) IN (SELECT study_id, patient_id FROM clinical_patient WHERE attr_value ILIKE %s))",
			like, like, like, like)
	}

	// Total number of matching samples (before paging) -> total-count header.
	type countRow struct {
		N uint64 `json:"n"`
	}
	total := uint64(0)
	if cr, _ := ch.Query[countRow](s.CH, fmt.Sprintf("SELECT count() AS n FROM (%s)", inner)); len(cr) > 0 {
		total = cr[0].N
	}
	w.Header().Set("total-count", strconv.FormatUint(total, 10))
	if total == 0 {
		writeJSON(w, empty)
		return
	}

	// Build the ordered + paged sample page, pushing sort/limit into ClickHouse.
	offset := pageNumber * pageSize
	var pageSQL string
	switch sortBy {
	case "", "sampleId":
		pageSQL = fmt.Sprintf(
			"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM (%s) "+
				"ORDER BY sample_id %s LIMIT %d OFFSET %d", inner, dir, pageSize, offset)
	case "patientId":
		pageSQL = fmt.Sprintf(
			"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM (%s) "+
				"ORDER BY patient_id %s, sample_id ASC LIMIT %d OFFSET %d", inner, dir, pageSize, offset)
	default:
		view, keyCol := "clinical_sample", "sample_id"
		if s.attrIsPatientLevel(f.studyIDs(), sortBy) {
			view, keyCol = "clinical_patient", "patient_id"
		}
		pageSQL = fmt.Sprintf(
			"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM (%s) AS s "+
				"LEFT JOIN (SELECT study_id, %s, any(attr_value) AS sortval FROM %s WHERE attr_id = '%s' GROUP BY study_id, %s) AS v "+
				"ON s.study_id = v.study_id AND s.%s = v.%s "+
				"ORDER BY sortval %s, sample_id ASC LIMIT %d OFFSET %d",
			inner, keyCol, view, sqlLit(sortBy), keyCol, keyCol, keyCol, dir, pageSize, offset)
	}

	pageRows, err := ch.Query[idRow](s.CH, pageSQL)
	if err != nil {
		fail(w, err)
		return
	}
	if len(pageRows) == 0 {
		writeJSON(w, empty)
		return
	}

	samplePairs := make([][2]string, 0, len(pageRows))
	patientSet := map[string]bool{}
	var patientPairs [][2]string
	patientToSamples := map[string][]idRow{}
	byKey := map[string][]ClinicalData{}
	for _, pr := range pageRows {
		samplePairs = append(samplePairs, [2]string{pr.StudyID, pr.SampleID})
		byKey[uniqueKey(pr.StudyID, pr.SampleID)] = []ClinicalData{}
		pk := pr.StudyID + "\x00" + pr.PatientID
		patientToSamples[pk] = append(patientToSamples[pk], pr)
		if !patientSet[pk] {
			patientSet[pk] = true
			patientPairs = append(patientPairs, [2]string{pr.StudyID, pr.PatientID})
		}
	}

	// Sample-level clinical data for the page.
	sampleData, _ := ch.Query[clinicalRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS id, attr_id AS clinicalAttributeId, attr_value AS value "+
			"FROM clinical_sample WHERE (study_id, sample_id) IN (%s)", tupleIn(samplePairs)))
	sampleToPatient := map[string]string{}
	for _, pr := range pageRows {
		sampleToPatient[pr.StudyID+"\x00"+pr.SampleID] = pr.PatientID
	}
	for _, cr := range sampleData {
		patientID := sampleToPatient[cr.StudyID+"\x00"+cr.ID]
		key := uniqueKey(cr.StudyID, cr.ID)
		byKey[key] = append(byKey[key], ClinicalData{
			ClinicalAttributeID: cr.ClinicalAttributeID, Value: cr.Value,
			SampleID: cr.ID, PatientID: patientID, StudyID: cr.StudyID,
			UniqueSampleKey: key, UniquePatientKey: uniqueKey(cr.StudyID, patientID),
			PatientAttribute: false,
		})
	}

	// Patient-level clinical data, mapped onto each of the patient's page samples.
	if len(patientPairs) > 0 {
		patientData, _ := ch.Query[clinicalRow](s.CH, fmt.Sprintf(
			"SELECT study_id AS studyId, patient_id AS id, attr_id AS clinicalAttributeId, attr_value AS value "+
				"FROM clinical_patient WHERE (study_id, patient_id) IN (%s)", tupleIn(patientPairs)))
		for _, cr := range patientData {
			for _, sm := range patientToSamples[cr.StudyID+"\x00"+cr.ID] {
				key := uniqueKey(sm.StudyID, sm.SampleID)
				byKey[key] = append(byKey[key], ClinicalData{
					ClinicalAttributeID: cr.ClinicalAttributeID, Value: cr.Value,
					SampleID: sm.SampleID, PatientID: sm.PatientID, StudyID: sm.StudyID,
					UniqueSampleKey: key, UniquePatientKey: uniqueKey(sm.StudyID, sm.PatientID),
					PatientAttribute: true,
				})
			}
		}
	}

	writeJSON(w, sampleClinicalDataCollection{ByUniqueSampleKey: byKey})
}

// attrIsPatientLevel reports whether a clinical attribute is patient-level in any
// of the given studies.
func (s *State) attrIsPatientLevel(studyIDs []string, attrID string) bool {
	if len(studyIDs) == 0 || attrID == "" {
		return false
	}
	type r struct {
		Flag uint8 `json:"flag"`
	}
	rows, _ := ch.Query[r](s.CH, fmt.Sprintf(
		"SELECT max(patient_attribute) AS flag FROM clinical_attribute_meta WHERE study_id IN (%s) AND clinical_attribute_id = '%s'",
		inList(studyIDs), sqlLit(attrID)))
	return len(rows) > 0 && rows[0].Flag != 0
}

// customDataCountsFetch counts the values of user-defined (custom chart) data
// stored as custom_data sessions over the filtered samples — or their patients
// for patient-level data — with the remainder reported as NA.
func (s *State) customDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Attributes []struct {
			AttributeID string `json:"attributeId"`
		} `json:"attributes"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	readBody(r, &f)
	svf := f.StudyViewFilter
	if len(f.Attributes) == 1 {
		svf = svf.withoutCustom(f.Attributes[0].AttributeID)
	}
	out := []ClinicalDataCountItem{}
	type sample struct {
		StudyID   string `json:"studyId"`
		SampleID  string `json:"sampleId"`
		PatientID string `json:"patientId"`
	}
	samples, _ := ch.Query[sample](s.CH, fmt.Sprintf(
		"SELECT cancer_study_identifier AS studyId, sample_stable_id AS sampleId, patient_stable_id AS patientId "+
			"FROM sample_derived WHERE %s", s.filterWhere(svf)))
	if len(samples) == 0 {
		writeJSON(w, out)
		return
	}
	filtered := map[string]bool{}
	patients := map[string]bool{}
	for _, sm := range samples {
		filtered[sm.StudyID+sm.SampleID] = true
		patients[sm.StudyID+sm.PatientID] = true
	}
	for _, a := range f.Attributes {
		raw, ok := s.sessionData("custom_data", a.AttributeID)
		if !ok {
			continue
		}
		var sess struct {
			PatientAttribute bool               `json:"patientAttribute"`
			Data             []customDataSample `json:"data"`
		}
		if json.Unmarshal(raw, &sess) != nil {
			continue
		}
		cases := map[string]map[string]bool{}
		var order []string
		for _, d := range sess.Data {
			if !filtered[d.StudyID+d.SampleID] {
				continue
			}
			if cases[d.Value] == nil {
				cases[d.Value] = map[string]bool{}
				order = append(order, d.Value)
			}
			id := d.SampleID
			if sess.PatientAttribute {
				id = d.PatientID
			}
			cases[d.Value][d.StudyID+id] = true
		}
		item := ClinicalDataCountItem{AttributeID: a.AttributeID, Counts: []ClinicalDataCount{}}
		var sum uint64
		for _, v := range order {
			if u := strings.ToUpper(v); u == "NA" || u == "NAN" || u == "N/A" {
				continue
			}
			n := uint64(len(cases[v]))
			item.Counts = append(item.Counts, ClinicalDataCount{Value: v, Count: n})
			sum += n
		}
		total := uint64(len(filtered))
		if sess.PatientAttribute {
			total = uint64(len(patients))
		}
		if total > sum {
			item.Counts = append(item.Counts, ClinicalDataCount{Value: "NA", Count: total - sum})
		}
		out = append(out, item)
	}
	writeJSON(w, out)
}
