package studyview

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/studies"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/stats"
)

// routeStudyView3 registers study-view endpoints for clinical-data enrichments,
// the clinical-data table and custom (session) charts.
func (s Server) routeStudyView3(mux router) {
	mux.HandleFunc("POST /api/clinical-data-enrichments/fetch", s.clinicalDataEnrichmentsFetch)
	mux.HandleFunc("POST /api/clinical-data-table/fetch", s.clinicalDataTableFetch)
	mux.HandleFunc("POST /api/custom-data-counts/fetch", s.customDataCountsFetch)
	mux.HandleFunc("POST /api/custom-data-bin-counts/fetch", s.customDataBinCountsFetch)
}

// naFilter is the cBioPortal rule for excluding "not available" clinical values,
// mirroring the backend's isAttributeValueNA SQL fragment.
func naFilter(col string) string {
	return "NOT " + isNASQL(col)
}

// ---------- Clinical data enrichments ----------

// ClinicalDataEnrichment is one attribute's group-comparison result: a
// Kruskal-Wallis/Wilcoxon test for numeric attributes or a chi-squared test for
// categorical ones (legacy ClinicalDataEnrichment; no q-value).
type ClinicalDataEnrichment struct {
	ClinicalAttribute studies.ClinicalAttribute `json:"clinicalAttribute"`
	Score             float64                   `json:"score"`
	Method            string                    `json:"method"`
	PValue            float64                   `json:"pValue"`
}

// enrichmentLevel is how an attribute's values are read for the groups, as
// ClinicalAttributeUtil.extractCategorizedClinicalAttributes classifies it
// across the groups' studies: sample-level, patient-level (one value per
// patient), or conflicting (patient-level in some studies, sample-level in
// others: sample values plus the patient value repeated for every sample of
// the patient).
type enrichmentLevel int

const (
	levelSample enrichmentLevel = iota
	levelPatient
	levelConflicting
)

// clinicalDataEnrichmentsFetch ports ClinicalDataEnrichmentController and
// ClinicalDataEnrichmentUtil: groups keep their resolvable samples and empty
// groups are dropped; attributes are deduplicated by id and patient flag (the
// last study's metadata wins) and tested in that HashMap's order, categorical
// (STRING) attributes first, then numeric (NUMBER) ones. Statistics are
// Datumbox's (internal/stats DatumboxKruskalWallisFromRanks /
// DatumboxChisquare).
func (s Server) clinicalDataEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Groups []struct {
			Name              string            `json:"name"`
			SampleIdentifiers []core.Identifier `json:"sampleIdentifiers"`
		} `json:"groups"`
	}
	core.ReadBody(r, &f)

	out := []ClinicalDataEnrichment{}
	if len(f.Groups) < 2 {
		core.WriteJSON(w, out)
		return
	}

	// Each group's samples as a predicate over the sample view. Every query
	// below spells each group's sample list once (as the group_samples CTE) and
	// resolves the groups' samples and patients inside ClickHouse.
	args := core.NewArgs()
	var groupSelects []string
	studySet := map[string]bool{}
	for gi, g := range f.Groups {
		groupSelects = append(groupSelects, fmt.Sprintf(
			"SELECT toUInt32(%d) AS grp, study_id, sample_id, patient_id FROM %s",
			gi, core.IdentifiersFrom(args, core.TSample, g.SampleIdentifiers)))
		for _, id := range g.SampleIdentifiers {
			studySet[id.StudyID] = true
		}
	}
	// The groups' studies, the key range of the clinical tables they read.
	groupStudies := core.ByStudies(core.SortedKeys(studySet))
	withGroups := "WITH group_samples AS (" + strings.Join(groupSelects, " UNION ALL ") + ") "

	type groupRow struct {
		Grp uint32 `json:"grp"`
	}
	present, err := core.QueryA[groupRow](s.CH, withGroups+"SELECT DISTINCT grp FROM group_samples ORDER BY grp", args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	numGroups := len(present)
	if numGroups < 2 {
		core.WriteJSON(w, out)
		return
	}
	testName := "Kruskal Wallis Test"
	if numGroups == 2 {
		testName = "Wilcoxon Test"
	}

	attrRows, err := core.QueryA[studies.ClinAttrRow](s.CH, fmt.Sprintf(
		"%sSELECT study_id AS studyId, clinical_attribute_id AS clinicalAttributeId, display_name AS displayName, "+
			"description, datatype, priority, patient_attribute AS patientFlag FROM clinical_attribute_meta "+
			"WHERE study_id IN (SELECT DISTINCT study_id FROM group_samples) ORDER BY study_id, clinical_attribute_id",
		withGroups), args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	// Unique attributes by id + patient flag, and each id's level.
	attrs := map[string]studies.ClinicalAttribute{}
	var keys []string
	flags := map[string]map[bool]bool{}
	for _, a := range attrRows {
		patient := a.PatientFlag != 0
		key := a.ClinicalAttributeID + strconv.FormatBool(patient)
		if _, ok := attrs[key]; !ok {
			keys = append(keys, key)
		}
		attrs[key] = studies.ClinicalAttribute{
			ClinicalAttributeID: a.ClinicalAttributeID, DisplayName: a.DisplayName, Description: a.Description,
			Datatype: a.Datatype, PatientAttribute: patient, Priority: a.Priority, StudyID: a.StudyID,
		}
		if flags[a.ClinicalAttributeID] == nil {
			flags[a.ClinicalAttributeID] = map[bool]bool{}
		}
		flags[a.ClinicalAttributeID][patient] = true
	}
	levelOf := func(id string) enrichmentLevel {
		switch fl := flags[id]; {
		case len(fl) > 1:
			return levelConflicting
		case fl[true]:
			return levelPatient
		}
		return levelSample
	}
	var numeric, categorical []studies.ClinicalAttribute
	for _, key := range core.JavaHashMapOrder(keys) {
		switch a := attrs[key]; a.Datatype {
		case "NUMBER":
			numeric = append(numeric, a)
		case "STRING":
			categorical = append(categorical, a)
		}
	}
	idsByLevel := func(list []studies.ClinicalAttribute) map[enrichmentLevel][]string {
		m := map[enrichmentLevel][]string{}
		seen := map[string]bool{}
		for _, a := range list {
			if !seen[a.ClinicalAttributeID] {
				seen[a.ClinicalAttributeID] = true
				l := levelOf(a.ClinicalAttributeID)
				m[l] = append(m[l], a.ClinicalAttributeID)
			}
		}
		return m
	}

	// groupValues selects (attr_id, grp, v) over every group: sample-level
	// values of the group's samples, patient-level values once per patient of
	// the group, and for conflicting attributes both the sample-level values
	// and the patient value once per sample of each of the group's patients.
	// valueOf renders the value (and cond the row filter) for an attr_value
	// column.
	groupValues := func(valueOf, cond func(col string) string, ids map[enrichmentLevel][]string) string {
		var parts []string
		sampleIDs := append(append([]string{}, ids[levelSample]...), ids[levelConflicting]...)
		if len(sampleIDs) > 0 {
			parts = append(parts, fmt.Sprintf(
				"SELECT c.attr_id AS attr_id, g.grp AS grp, %s AS v FROM group_samples g "+
					"INNER JOIN %s c ON c.study_id = g.study_id AND c.sample_id = g.sample_id "+
					"WHERE c.attr_id IN %s AND %s",
				valueOf("c.attr_value"), groupStudies.Rows(args, core.TClinicalSample), args.Strs(sampleIDs), cond("c.attr_value")))
		}
		groupPatients := "(SELECT DISTINCT grp, study_id, patient_id FROM group_samples)"
		if p := ids[levelPatient]; len(p) > 0 {
			parts = append(parts, fmt.Sprintf(
				"SELECT c.attr_id AS attr_id, g.grp AS grp, %s AS v FROM %s g "+
					"INNER JOIN %s c ON c.study_id = g.study_id AND c.patient_id = g.patient_id "+
					"WHERE c.attr_id IN %s AND %s",
				valueOf("c.attr_value"), groupPatients, groupStudies.Rows(args, core.TClinicalPatient), args.Strs(p), cond("c.attr_value")))
		}
		if p := ids[levelConflicting]; len(p) > 0 {
			parts = append(parts, fmt.Sprintf(
				"SELECT c.attr_id AS attr_id, g.grp AS grp, %s AS v FROM %s g "+
					"INNER JOIN %s s ON s.study_id = g.study_id AND s.patient_id = g.patient_id "+
					"INNER JOIN %s c ON c.study_id = g.study_id AND c.patient_id = g.patient_id "+
					"WHERE c.attr_id IN %s AND %s",
				valueOf("c.attr_value"), groupPatients, groupStudies.Samples(args), groupStudies.Rows(args, core.TClinicalPatient), args.Strs(p), cond("c.attr_value")))
		}
		return strings.Join(parts, " UNION ALL ")
	}

	// Categorical attributes: per attribute, group and value, the case count
	// (NA-like values excluded).
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
	if len(categorical) > 0 {
		rows, err := core.QueryA[catRow](s.CH, fmt.Sprintf(
			"%sSELECT attr_id, grp, v AS value, count() AS c FROM (%s) GROUP BY attr_id, grp, v ORDER BY attr_id, grp, v",
			withGroups, groupValues(func(col string) string { return col }, naFilter, idsByLevel(categorical))), args)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		for _, rw := range rows {
			m := catData[rw.Grp]
			if m[rw.AttrID] == nil {
				m[rw.AttrID] = map[string]uint64{}
			}
			m[rw.AttrID][rw.Value] += rw.C
		}
	}
	for _, a := range categorical {
		id := a.ClinicalAttributeID
		var seen []string
		catSet := map[string]bool{}
		for gi := range f.Groups {
			for _, v := range core.SortedKeys(catData[gi][id]) {
				if !catSet[v] {
					catSet[v] = true
					seen = append(seen, v)
				}
			}
		}
		if len(catSet) < 2 {
			continue
		}
		categories := core.JavaHashMapOrder(seen)
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
		score, p := stats.DatumboxChisquare(table)
		out = append(out, ClinicalDataEnrichment{ClinicalAttribute: a, Score: score, Method: "Chi-squared Test", PValue: p})
	}

	// Numeric attributes: per attribute and group, the number of values and the
	// sum of their tie-averaged ranks among all groups' values, the attribute's
	// tie term (sum over values of t^2 - 1 for the t values tied with it, i.e.
	// sum over distinct values of t^3 - t) and its number of distinct values.
	type rankRow struct {
		AttrID   string  `json:"attr_id"`
		Grp      uint32  `json:"grp"`
		N        uint64  `json:"n"`
		RankSum  float64 `json:"rs"`
		TieSum   float64 `json:"ts"`
		Distinct uint64  `json:"nd"`
	}
	numRanks := map[string][]rankRow{}
	if len(numeric) > 0 {
		rows, err := core.QueryA[rankRow](s.CH, fmt.Sprintf(
			"%sSELECT attr_id, grp, count() AS n, sum(r) AS rs, sum(t * t - 1) AS ts, any(nd) AS nd FROM ("+
				"SELECT attr_id, grp, rank() OVER (PARTITION BY attr_id ORDER BY v) + (count() OVER (PARTITION BY attr_id, v) - 1) / 2 AS r, "+
				"toFloat64(count() OVER (PARTITION BY attr_id, v)) AS t, uniqExact(v) OVER (PARTITION BY attr_id) AS nd FROM (%s)"+
				") GROUP BY attr_id, grp ORDER BY attr_id, grp",
			withGroups, groupValues(func(col string) string { return "toFloat64(" + col + ")" },
				func(col string) string { return "match(" + col + ", " + args.Str(decimalCreatable) + ")" }, idsByLevel(numeric))), args)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		for _, rw := range rows {
			numRanks[rw.AttrID] = append(numRanks[rw.AttrID], rw)
		}
	}
	for _, a := range numeric {
		groupRanks := numRanks[a.ClinicalAttributeID]
		if len(groupRanks) < 2 || groupRanks[0].Distinct < 2 {
			continue
		}
		counts := make([]uint64, len(groupRanks))
		sums := make([]float64, len(groupRanks))
		ties := 0.0
		for i, g := range groupRanks {
			counts[i], sums[i] = g.N, g.RankSum
			ties += g.TieSum
		}
		p := stats.DatumboxKruskalWallisFromRanks(counts, sums, ties)
		if math.IsNaN(p) {
			continue
		}
		out = append(out, ClinicalDataEnrichment{
			ClinicalAttribute: a,
			Score:             stats.DatumboxChisquareInverseCdf(p, len(groupRanks)-1),
			Method:            testName,
			PValue:            p,
		})
	}

	core.WriteJSON(w, out)
}

// decimalCreatable matches the decimal numbers NumberUtils.isCreatable
// accepts, the values ClinicalDataEnrichmentUtil tests ("nan", "inf" and
// blank-padded values are not): decimal and scientific notation, and integers
// with a leading zero only as octal literals. Hexadecimal literals and Java
// type suffixes, which it also accepts, are left out.
const decimalCreatable = `^[+-]?(0[0-7]+|(0|[1-9][0-9]*)([eE][+-]?[0-9]+)?|([0-9]+\.[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?)$`

// ---------- Clinical data table (study-view Clinical tab) ----------

// tableClinicalData is a clinical-table cell as LegacyClinicalDataMapper
// .toDtoForTable writes it: no unique keys, no attribute metadata.
type tableClinicalData struct {
	SampleID            string `json:"sampleId"`
	PatientID           string `json:"patientId"`
	StudyID             string `json:"studyId"`
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	Value               string `json:"value"`
}

type sampleClinicalDataCollection struct {
	ByUniqueSampleKey map[string][]tableClinicalData `json:"byUniqueSampleKey"`
}

// clinicalDataTableFetch serves POST /api/clinical-data-table/fetch as
// StudyViewController does: samples whose sample or patient attribute values
// or ids contain searchTerm (case-sensitively, LIKE wildcards included),
// ordered by sampleId, patientId or an attribute (numerically for NUMBER
// attributes, samples without a value last), then paged.
func (s Server) clinicalDataTableFetch(w http.ResponseWriter, r *http.Request) {
	var f studyViewFilter
	core.ReadBody(r, &f)

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

	empty := sampleClinicalDataCollection{ByUniqueSampleKey: map[string][]tableClinicalData{}}

	args := core.NewArgs()
	inner := "SELECT study_id, sample_id, patient_id FROM " + s.sampleScopeFrom(args, f, core.TSample)
	if strings.TrimSpace(searchTerm) != "" {
		// ClinicalDataMapper.searchedAttributeData: a case-sensitive LIKE on
		// the sample and patient ids and attribute values.
		like := args.Str("%" + searchTerm + "%")
		inner += fmt.Sprintf(
			" AND (sample_id LIKE %[1]s OR patient_id LIKE %[1]s"+
				" OR (study_id, sample_id) IN (SELECT study_id, sample_id FROM %[2]s AND attr_value LIKE %[1]s)"+
				" OR (study_id, patient_id) IN (SELECT study_id, patient_id FROM %[3]s AND attr_value LIKE %[1]s))",
			like, f.scope().From(args, core.TClinicalSample), f.scope().From(args, core.TClinicalPatient))
	}

	// Total number of matching samples (before paging) -> total-count header.
	type countRow struct {
		N uint64 `json:"n"`
	}
	total := uint64(0)
	if cr, _ := core.QueryA[countRow](s.CH, fmt.Sprintf("SELECT count() AS n FROM (%s)", inner), args); len(cr) > 0 {
		total = cr[0].N
	}
	w.Header().Set("total-count", strconv.FormatUint(total, 10))
	if total == 0 {
		core.WriteJSON(w, empty)
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
		// Samples without the attribute sort last; NUMBER attributes sort
		// numerically (non-numbers as missing), as ClinicalDataMapper does.
		view, keyCol := "clinical_sample", "sample_id"
		if s.attrIsPatientLevel(f.studyIDs(), sortBy) {
			view, keyCol = "clinical_patient", "patient_id"
		}
		sortExpr := "v.sortval"
		if s.attrIsNumber(f.studyIDs(), sortBy) {
			sortExpr = "toFloat64OrNull(v.sortval)"
		}
		pageSQL = fmt.Sprintf(
			"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM (%s) AS s "+
				"LEFT JOIN (SELECT study_id, %s, any(attr_value) AS sortval, 1 AS present FROM %s WHERE attr_id = %s GROUP BY study_id, %s) AS v "+
				"ON s.study_id = v.study_id AND s.%s = v.%s "+
				"ORDER BY if(v.present = 1, %s, NULL) %s NULLS LAST, sample_id ASC LIMIT %d OFFSET %d",
			inner, keyCol, view, args.Str(sortBy), keyCol, keyCol, keyCol, sortExpr, dir, pageSize, offset)
	}

	pageRows, err := core.QueryA[studies.IDRow](s.CH, pageSQL, args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(pageRows) == 0 {
		core.WriteJSON(w, empty)
		return
	}

	samplePairs := make([][2]string, 0, len(pageRows))
	patientSet := map[string]bool{}
	var patientPairs [][2]string
	patientToSamples := map[string][]studies.IDRow{}
	byKey := map[string][]tableClinicalData{}
	for _, pr := range pageRows {
		samplePairs = append(samplePairs, [2]string{pr.StudyID, pr.SampleID})
		pk := pr.StudyID + "\x00" + pr.PatientID
		patientToSamples[pk] = append(patientToSamples[pk], pr)
		if !patientSet[pk] {
			patientSet[pk] = true
			patientPairs = append(patientPairs, [2]string{pr.StudyID, pr.PatientID})
		}
	}

	// Sample-level clinical data for the page.
	sampleData, _ := ch.QueryP[studies.ClinicalRow](s.CH,
		"SELECT study_id AS studyId, sample_id AS id, attr_id AS clinicalAttributeId, attr_value AS value "+
			"FROM clinical_sample WHERE (study_id, sample_id) IN {ids:Array(Tuple(String, String))} ORDER BY attr_id",
		ch.Params{"ids": samplePairs})
	sampleToPatient := map[string]string{}
	for _, pr := range pageRows {
		sampleToPatient[pr.StudyID+"\x00"+pr.SampleID] = pr.PatientID
	}
	for _, cr := range sampleData {
		patientID := sampleToPatient[cr.StudyID+"\x00"+cr.ID]
		key := core.UniqueKey(cr.StudyID, cr.ID)
		byKey[key] = append(byKey[key], tableClinicalData{
			ClinicalAttributeID: cr.ClinicalAttributeID, Value: cr.Value,
			SampleID: cr.ID, PatientID: patientID, StudyID: cr.StudyID,
		})
	}

	// Patient-level clinical data, mapped onto each of the patient's page samples.
	if len(patientPairs) > 0 {
		patientData, _ := ch.QueryP[studies.ClinicalRow](s.CH,
			"SELECT study_id AS studyId, patient_id AS id, attr_id AS clinicalAttributeId, attr_value AS value "+
				"FROM clinical_patient WHERE (study_id, patient_id) IN {ids:Array(Tuple(String, String))} ORDER BY attr_id",
			ch.Params{"ids": patientPairs})
		for _, cr := range patientData {
			for _, sm := range patientToSamples[cr.StudyID+"\x00"+cr.ID] {
				key := core.UniqueKey(sm.StudyID, sm.SampleID)
				byKey[key] = append(byKey[key], tableClinicalData{
					ClinicalAttributeID: cr.ClinicalAttributeID, Value: cr.Value,
					SampleID: sm.SampleID, PatientID: sm.PatientID, StudyID: sm.StudyID,
				})
			}
		}
	}

	core.WriteJSON(w, sampleClinicalDataCollection{ByUniqueSampleKey: byKey})
}

// attrIsPatientLevel reports whether a clinical attribute is patient-level in any
// of the given studies.
func (s Server) attrIsPatientLevel(studyIDs []string, attrID string) bool {
	if len(studyIDs) == 0 || attrID == "" {
		return false
	}
	type r struct {
		Flag uint8 `json:"flag"`
	}
	rows, _ := ch.QueryP[r](s.CH,
		"SELECT max(patient_attribute) AS flag FROM clinical_attribute_meta WHERE study_id IN {studies:Array(String)} "+
			"AND clinical_attribute_id = {attr:String}",
		ch.Params{"studies": studyIDs, "attr": attrID})
	return len(rows) > 0 && rows[0].Flag != 0
}

// attrIsNumber reports whether a clinical attribute has the NUMBER datatype
// in any of the given studies.
func (s Server) attrIsNumber(studyIDs []string, attrID string) bool {
	if len(studyIDs) == 0 || attrID == "" {
		return false
	}
	type r struct {
		N uint64 `json:"n"`
	}
	rows, _ := ch.QueryP[r](s.CH,
		"SELECT count() AS n FROM clinical_attribute_meta WHERE study_id IN {studies:Array(String)} "+
			"AND clinical_attribute_id = {attr:String} AND datatype = 'NUMBER'",
		ch.Params{"studies": studyIDs, "attr": attrID})
	return len(rows) > 0 && rows[0].N > 0
}

// customDataCountsFetch counts the values of user-defined (custom chart) data
// stored as custom_data sessions over the filtered samples — or their patients
// for patient-level data — with the remainder reported as NA.
func (s Server) customDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Attributes []struct {
			AttributeID string `json:"attributeId"`
		} `json:"attributes"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	core.ReadBody(r, &f)
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
	args := core.NewArgs()
	samples, _ := core.QueryA[sample](s.CH,
		"SELECT cancer_study_identifier AS studyId, sample_stable_id AS sampleId, patient_stable_id AS patientId "+
			"FROM "+s.filterFrom(args, svf), args)
	if len(samples) == 0 {
		core.WriteJSON(w, out)
		return
	}
	filtered := map[string]bool{}
	patients := map[string]bool{}
	for _, sm := range samples {
		filtered[sm.StudyID+sm.SampleID] = true
		patients[sm.StudyID+sm.PatientID] = true
	}
	for _, a := range f.Attributes {
		raw, ok := s.SessionData("custom_data", a.AttributeID)
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
	core.WriteJSON(w, out)
}
