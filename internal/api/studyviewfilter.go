package api

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// studyViewFilter mirrors cBioPortal's StudyViewFilter. The sample set it
// selects is compiled to ClickHouse SQL over the prod-aligned *_derived views by
// filterSQL, following the column-store ClickhouseStudyViewFilterMapper: every
// sub-filter contributes one predicate over sample_derived and the predicates
// are AND-ed (the mapper's INTERSECT).
type studyViewFilter struct {
	StudyIDs                     []string                      `json:"studyIds"`
	SampleIdentifiers            []identifier                  `json:"sampleIdentifiers"`
	AlterationFilter             *alterationFilter             `json:"alterationFilter"`
	ClinicalDataFilters          []clinicalDataFilter          `json:"clinicalDataFilters"`
	CustomDataFilters            []clinicalDataFilter          `json:"customDataFilters"`
	GeneFilters                  []svGeneFilter                `json:"geneFilters"`
	GenomicDataFilters           []genomicDataFilter           `json:"genomicDataFilters"`
	GenericAssayDataFilters      []genericAssayDataFilter      `json:"genericAssayDataFilters"`
	GenericAssaySelectionFilters []genericAssaySelectionFilter `json:"genericAssaySelectionFilters"`
	MutationDataFilters          []mutationDataFilter          `json:"mutationDataFilters"`
	ClinicalEventFilters         []dataFilter                  `json:"clinicalEventFilters"`
	CaseLists                    [][]string                    `json:"caseLists"`
	GenomicProfiles              [][]string                    `json:"genomicProfiles"`
	PatientTreatmentFilters      *andedTreatmentFilters        `json:"patientTreatmentFilters"`
	SampleTreatmentFilters       *andedTreatmentFilters        `json:"sampleTreatmentFilters"`
}

// dataFilterValue is one selected chart value: a categorical value, or a
// numeric (start, end] range (start == end selects exactly that number).
type dataFilterValue struct {
	Start *json.Number `json:"start"`
	End   *json.Number `json:"end"`
	Value *string      `json:"value"`
}

func (v dataFilterValue) isNA() bool { return v.Value != nil && *v.Value == "NA" }

type dataFilter struct {
	Values []dataFilterValue `json:"values"`
}

type clinicalDataFilter struct {
	AttributeID string            `json:"attributeId"`
	Values      []dataFilterValue `json:"values"`
	// Custom-data filters may carry their data inline instead of a session id.
	Datatype    string             `json:"datatype"`
	DisplayName string             `json:"displayName"`
	Samples     []customDataSample `json:"samples"`
}

type customDataSample struct {
	StudyID   string `json:"studyId"`
	SampleID  string `json:"sampleId"`
	PatientID string `json:"patientId"`
	Value     string `json:"value"`
}

type svGeneFilter struct {
	MolecularProfileIDs []string              `json:"molecularProfileIds"`
	GeneQueries         [][]svGeneFilterQuery `json:"geneQueries"`
}

type svGeneFilterQuery struct {
	HugoGeneSymbol string   `json:"hugoGeneSymbol"`
	Alterations    []string `json:"alterations"`
}

type genomicDataFilter struct {
	HugoGeneSymbol string            `json:"hugoGeneSymbol"`
	ProfileType    string            `json:"profileType"`
	Values         []dataFilterValue `json:"values"`
}

type genericAssayDataFilter struct {
	StableID    string            `json:"stableId"`
	ProfileType string            `json:"profileType"`
	Values      []dataFilterValue `json:"values"`
}

type genericAssaySelectionFilter struct {
	ProfileType  string `json:"profileType"`
	PatientLevel bool   `json:"patientLevel"`
	Values       [][]struct {
		StableID string `json:"stableId"`
		Value    string `json:"value"`
	} `json:"values"`
}

type mutationDataFilter struct {
	HugoGeneSymbol string              `json:"hugoGeneSymbol"`
	ProfileType    string              `json:"profileType"`
	Categorization string              `json:"categorization"`
	Values         [][]dataFilterValue `json:"values"`
}

// andedTreatmentFilters is AND over groups of OR-ed treatment filters (shared by
// the patient and sample treatment filter shapes; time is sample-only).
type andedTreatmentFilters struct {
	Filters []struct {
		Filters []struct {
			Treatment string `json:"treatment"`
			Time      string `json:"time"`
		} `json:"filters"`
	} `json:"filters"`
}

// cnaCodes maps the CNA alteration names a gene filter carries to the stored
// discrete codes.
var cnaCodes = map[string]string{"AMP": "2", "GAIN": "1", "DIPLOID": "0", "HETLOSS": "-1", "HOMDEL": "-2"}

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

// hasSubFilters reports whether anything beyond the study/sample scope narrows
// the selection.
func (f studyViewFilter) hasSubFilters() bool {
	return len(f.ClinicalDataFilters) > 0 || len(f.CustomDataFilters) > 0 || len(f.GeneFilters) > 0 ||
		len(f.GenomicDataFilters) > 0 || len(f.GenericAssayDataFilters) > 0 ||
		len(f.GenericAssaySelectionFilters) > 0 || len(f.MutationDataFilters) > 0 ||
		len(f.ClinicalEventFilters) > 0 || len(f.CaseLists) > 0 || len(f.GenomicProfiles) > 0 ||
		(f.PatientTreatmentFilters != nil && len(f.PatientTreatmentFilters.Filters) > 0) ||
		(f.SampleTreatmentFilters != nil && len(f.SampleTreatmentFilters.Filters) > 0)
}

// baseScope is the study/sample-identifier scope over sample_derived.
func (f studyViewFilter) baseScope(a *sqlArgs) string {
	if len(f.SampleIdentifiers) > 0 {
		ids := make([]string, len(f.SampleIdentifiers))
		for i, id := range f.SampleIdentifiers {
			ids[i] = id.StudyID + "_" + id.id()
		}
		return "sample_unique_id IN " + a.strs(ids)
	}
	if len(f.StudyIDs) > 0 {
		return "cancer_study_identifier IN " + a.strs(f.StudyIDs)
	}
	return falseClause
}

// filterSQL compiles the filter to a SELECT of the matching sample_unique_ids.
func (s *State) filterSQL(a *sqlArgs, f studyViewFilter) string {
	return "SELECT sample_unique_id FROM sample_derived WHERE " + s.filterWhere(a, f)
}

// filterWhere compiles the filter to a predicate over sample_derived columns
// (sample_unique_id, patient_unique_id, cancer_study_identifier).
func (s *State) filterWhere(a *sqlArgs, f studyViewFilter) string {
	preds := []string{f.baseScope(a)}
	if !f.hasSubFilters() {
		return preds[0]
	}
	studies := f.studyIDs()
	c := filterCompiler{s: s, a: a, studies: studies, studyIn: a.strs(studies)}
	preds = append(preds, c.caseLists(f.CaseLists)...)
	preds = append(preds, c.genomicProfiles(f.GenomicProfiles)...)
	preds = append(preds, c.customData(f.CustomDataFilters)...)
	preds = append(preds, c.geneFilters(f.GeneFilters)...)
	for _, m := range f.MutationDataFilters {
		preds = append(preds, c.mutationData(m))
	}
	preds = append(preds, c.sampleTreatments(f.SampleTreatmentFilters)...)
	for _, g := range mergeDataFilters(f.GenomicDataFilters, func(g *genomicDataFilter) *[]dataFilterValue { return &g.Values }) {
		preds = append(preds, c.genomicData(g))
	}
	for _, g := range mergeDataFilters(f.GenericAssayDataFilters, func(g *genericAssayDataFilter) *[]dataFilterValue { return &g.Values }) {
		preds = append(preds, c.genericAssayData(g))
	}
	for _, g := range f.GenericAssaySelectionFilters {
		preds = append(preds, c.genericAssaySelection(g)...)
	}
	for _, cd := range mergeDataFilters(f.ClinicalDataFilters, func(c *clinicalDataFilter) *[]dataFilterValue { return &c.Values }) {
		preds = append(preds, c.clinicalData(cd))
	}
	for _, e := range f.ClinicalEventFilters {
		var types []string
		for _, v := range e.Values {
			if v.Value != nil {
				types = append(types, *v.Value)
			}
		}
		preds = append(preds, fmt.Sprintf(
			"patient_unique_id IN (SELECT patient_unique_id FROM clinical_event_data_derived "+
				"WHERE cancer_study_identifier IN %s AND event_type IN %s)", c.studyIn, a.strs(types)))
	}
	preds = append(preds, c.patientTreatments(f.PatientTreatmentFilters)...)
	return "(" + strings.Join(preds, ") AND (") + ")"
}

// filterCompiler builds the predicates of one filter; studyIn is the bound
// array of its study ids.
type filterCompiler struct {
	s       *State
	a       *sqlArgs
	studies []string
	studyIn string
}

func (c filterCompiler) caseLists(groups [][]string) []string {
	var out []string
	for _, g := range groups {
		var ors []string
		for _, l := range g {
			ors = append(ors, fmt.Sprintf("endsWith(list_id, %s)", c.a.str("_"+l)))
		}
		if len(ors) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf(
			"sample_unique_id IN (SELECT concat(study_id, '_', sample_id) FROM sample_list "+
				"WHERE study_id IN %s AND (%s))", c.studyIn, strings.Join(ors, " OR ")))
	}
	return out
}

func (c filterCompiler) genomicProfiles(groups [][]string) []string {
	var out []string
	for _, g := range groups {
		var ids []string
		for _, st := range c.studies {
			for _, p := range g {
				ids = append(ids, st+"_"+p)
			}
		}
		out = append(out, fmt.Sprintf(
			"sample_unique_id IN (SELECT sample_unique_id FROM sample_to_gene_panel_derived "+
				"WHERE genetic_profile_id IN %s)", c.a.strs(ids)))
	}
	return out
}

func (c filterCompiler) geneFilters(filters []svGeneFilter) []string {
	var out []string
	for _, g := range filters {
		for _, queries := range g.GeneQueries {
			var ors []string
			for _, q := range queries {
				cond := "hugo_gene_symbol = " + c.a.str(q.HugoGeneSymbol)
				var codes []string
				for _, a := range q.Alterations {
					if code, ok := cnaCodes[a]; ok {
						codes = append(codes, code)
					}
				}
				if len(codes) > 0 {
					cond += fmt.Sprintf(" AND cna_alteration IN (%s)", strings.Join(codes, ", "))
				}
				ors = append(ors, "("+cond+")")
			}
			if len(ors) == 0 {
				continue
			}
			out = append(out, fmt.Sprintf(
				"sample_unique_id IN (SELECT sample_unique_id FROM genomic_event_derived "+
					"WHERE genetic_profile_stable_id IN %s AND (%s))",
				c.a.strs(g.MolecularProfileIDs), strings.Join(ors, " OR ")))
		}
	}
	return out
}

func (c filterCompiler) mutationData(m mutationDataFilter) string {
	gene := c.a.str(m.HugoGeneSymbol)
	mutated := fmt.Sprintf(
		"SELECT sample_unique_id FROM genomic_event_derived WHERE cancer_study_identifier IN %s "+
			"AND hugo_gene_symbol = %s AND variant_type = 'mutation'", c.studyIn, gene)
	if m.Categorization == "MUTATION_TYPE" {
		var ands []string
		for _, group := range m.Values {
			var types []string
			for _, v := range group {
				if v.Value != nil {
					types = append(types, *v.Value)
				}
			}
			ands = append(ands, fmt.Sprintf("sample_unique_id IN (%s AND mutation_type IN %s)", mutated, c.a.strs(types)))
		}
		if len(ands) == 0 {
			return "1 = 1"
		}
		return strings.Join(ands, " AND ")
	}
	profiled := fmt.Sprintf(
		"SELECT sgp.sample_unique_id FROM sample_to_gene_panel_derived sgp "+
			"JOIN gene_panel_to_gene_derived gpg ON sgp.gene_panel_id = gpg.gene_panel_id "+
			"WHERE sgp.cancer_study_identifier IN %s AND gpg.gene = %s AND sgp.alteration_type = 'MUTATION_EXTENDED'",
		c.studyIn, gene)
	var ors []string
	if len(m.Values) > 0 {
		for _, v := range m.Values[0] {
			if v.Value == nil {
				continue
			}
			switch *v.Value {
			case "MUTATED":
				ors = append(ors, fmt.Sprintf("sample_unique_id IN (%s)", mutated))
			case "NOT_MUTATED":
				ors = append(ors, fmt.Sprintf("(sample_unique_id IN (%s) AND sample_unique_id NOT IN (%s))", profiled, mutated))
			case "NOT_PROFILED":
				ors = append(ors, fmt.Sprintf("sample_unique_id NOT IN (%s)", profiled))
			}
		}
	}
	if len(ors) == 0 {
		return falseClause
	}
	return strings.Join(ors, " OR ")
}

func (c filterCompiler) patientTreatments(f *andedTreatmentFilters) []string {
	if f == nil {
		return nil
	}
	var out []string
	for _, ored := range f.Filters {
		var treatments []string
		for _, t := range ored.Filters {
			treatments = append(treatments, t.Treatment)
		}
		out = append(out, fmt.Sprintf(
			"patient_unique_id IN (SELECT patient_unique_id FROM clinical_event_data_derived "+
				"WHERE cancer_study_identifier IN %s AND lower(event_type) = 'treatment' AND key = 'AGENT' AND value IN %s)",
			c.studyIn, c.a.strs(treatments)))
	}
	return out
}

// sampleTreatments selects samples whose acquisition was before ("Pre") or
// after ("Post") the patient's first dose of the treatment.
func (c filterCompiler) sampleTreatments(f *andedTreatmentFilters) []string {
	if f == nil {
		return nil
	}
	var out []string
	for _, ored := range f.Filters {
		var ors []string
		for _, t := range ored.Filters {
			op := ">"
			if t.Time == "Pre" {
				op = "<="
			}
			ors = append(ors, fmt.Sprintf("(cedd_inner.treatment = %s AND cedd.time_taken %s cedd_inner.treatment_time_taken)",
				c.a.str(t.Treatment), op))
		}
		if len(ors) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf(
			"sample_unique_id IN (SELECT concat(cedd.cancer_study_identifier, '_', cedd.sample_id) FROM ("+
				"SELECT value AS sample_id, patient_unique_id, min(start_date) AS time_taken, cancer_study_identifier "+
				"FROM clinical_event_data_derived WHERE cancer_study_identifier IN %[1]s "+
				"AND (event_type ILIKE 'Sample Acquisition' OR event_type ILIKE 'SPECIMEN') AND key = 'SAMPLE_ID' "+
				"GROUP BY patient_unique_id, value, cancer_study_identifier) cedd "+
				"INNER JOIN (SELECT patient_unique_id, value AS treatment, min(start_date) AS treatment_time_taken "+
				"FROM clinical_event_data_derived WHERE cancer_study_identifier IN %[1]s "+
				"AND lower(event_type) = 'treatment' AND key = 'AGENT' GROUP BY patient_unique_id, value) cedd_inner "+
				"ON cedd_inner.patient_unique_id = cedd.patient_unique_id WHERE %[2]s)",
			c.studyIn, strings.Join(ors, " OR ")))
	}
	return out
}

// isNASQL matches the NA spellings (and the empty string) of a stored value.
func isNASQL(col string) string {
	return fmt.Sprintf("(%[1]s = '' OR upperUTF8(%[1]s) IN ('NA', 'NAN', 'N/A'))", col)
}

// castFloatSQL parses a stored numeric value, honouring the range operators
// (<, <=, >, >=) the way cBioPortal bins them; non-numeric values yield NULL.
func castFloatSQL(col string) string {
	return fmt.Sprintf("multiIf(NOT match(%[1]s, '^[><]?=?[-+]?[0-9]*[.,]?[0-9]+$'), NULL, "+
		"startsWith(%[1]s, '<=') OR startsWith(%[1]s, '>='), toFloat64OrNull(substr(%[1]s, 3)), "+
		"startsWith(%[1]s, '<'), toFloat64OrNull(substr(%[1]s, 2)) - exp(-10), "+
		"startsWith(%[1]s, '>'), toFloat64OrNull(substr(%[1]s, 2)) + exp(-10), "+
		"toFloat64OrNull(%[1]s))", col)
}

// numLit renders a JSON number as a SQL float literal.
func numLit(n *json.Number) string {
	if f, err := strconv.ParseFloat(string(*n), 64); err == nil {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return "nan"
}

// numericalValuesSQL is the OR over the selected values of a numeric chart:
// categorical special values match case-insensitively, ranges are (start, end].
func numericalValuesSQL(a *sqlArgs, col string, values []dataFilterValue) string {
	var ors []string
	for _, v := range values {
		conds := []string{}
		if v.isNA() {
			conds = append(conds, isNASQL(col))
		}
		if v.Value != nil {
			conds = append(conds, fmt.Sprintf("%s ILIKE %s", col, a.str(*v.Value)))
		}
		switch {
		case v.Start != nil && v.End == nil:
			conds = append(conds, fmt.Sprintf("match(%s, '^>?=?[-+]?[0-9]*[.,]?[0-9]+$')", col))
		case v.Start == nil && v.End != nil:
			conds = append(conds, fmt.Sprintf("match(%s, '^<?=?[-+]?[0-9]*[.,]?[0-9]+$')", col))
		case v.Start != nil && v.End != nil:
			conds = append(conds, fmt.Sprintf("match(%s, '^[-+]?[0-9]*[.,]?[0-9]+$')", col))
		}
		if v.Start != nil && v.End != nil && numLit(v.Start) == numLit(v.End) {
			conds = append(conds, fmt.Sprintf("abs(%s - %s) < exp(-11)", castFloatSQL(col), numLit(v.Start)))
		} else {
			if v.Start != nil {
				conds = append(conds, fmt.Sprintf("%s > %s", castFloatSQL(col), numLit(v.Start)))
			}
			if v.End != nil {
				conds = append(conds, fmt.Sprintf("%s <= %s", castFloatSQL(col), numLit(v.End)))
			}
		}
		if len(conds) == 0 {
			conds = append(conds, "1 = 1")
		}
		ors = append(ors, "("+strings.Join(conds, " AND ")+")")
	}
	if len(ors) == 0 {
		return falseClause
	}
	return strings.Join(ors, " OR ")
}

// isCategorical follows StudyViewFilterContext.isCategoricalClinicalDataFilter:
// the first value (or the last, if the first is all-null) decides.
func isCategorical(values []dataFilterValue) bool {
	if len(values) == 0 {
		return false
	}
	v := values[0]
	if v.Start == nil && v.End == nil && v.Value == nil {
		v = values[len(values)-1]
	}
	return v.Value != nil
}

func splitNA(values []dataFilterValue) (na bool, rest []dataFilterValue) {
	for _, v := range values {
		if v.isNA() {
			na = true
		} else {
			rest = append(rest, v)
		}
	}
	return
}

// clinicalData selects samples matching the attribute at sample level, or whose
// patient matches it at patient level. NA covers missing/NA-like values in
// studies that define the attribute, plus every sample of studies that do not.
func (c filterCompiler) clinicalData(f clinicalDataFilter) string {
	attr := c.a.str(f.AttributeID)
	categorical := isCategorical(f.Values)
	na, rest := splitNA(f.Values)
	var ors []string
	for _, lvl := range []struct{ typ, col, flag string }{
		{"sample", "sample_unique_id", "0"}, {"patient", "patient_unique_id", "1"},
	} {
		studiesWithAttr := fmt.Sprintf(
			"SELECT study_id FROM clinical_attribute_meta WHERE clinical_attribute_id = %s "+
				"AND patient_attribute = %s AND study_id IN %s", attr, lvl.flag, c.studyIn)
		data := fmt.Sprintf(
			"SELECT %s FROM clinical_data_derived WHERE attribute_name = %s AND type = '%s' "+
				"AND cancer_study_identifier IN (%s)", lvl.col, attr, lvl.typ, studiesWithAttr)
		var match string
		if categorical {
			var vals []string
			for _, v := range rest {
				if v.Value != nil {
					vals = append(vals, "attribute_value ILIKE "+c.a.str(*v.Value))
				}
			}
			if len(vals) > 0 {
				match = strings.Join(vals, " OR ")
			}
		} else if len(rest) > 0 {
			match = numericalValuesSQL(c.a, "attribute_value", rest)
		}
		if match != "" {
			ors = append(ors, fmt.Sprintf("%s IN (%s AND (%s))", lvl.col, data, match))
		}
		if na {
			ors = append(ors, fmt.Sprintf("(cancer_study_identifier IN (%s) AND %s NOT IN (%s AND NOT %s))",
				studiesWithAttr, lvl.col, data, isNASQL("attribute_value")))
		}
	}
	if na {
		ors = append(ors, fmt.Sprintf(
			"cancer_study_identifier IN %s AND cancer_study_identifier NOT IN "+
				"(SELECT study_id FROM clinical_attribute_meta WHERE clinical_attribute_id = %s)", c.studyIn, attr))
	}
	if len(ors) == 0 {
		return falseClause
	}
	return strings.Join(ors, " OR ")
}

func (c filterCompiler) genomicData(g genomicDataFilter) string {
	src := fmt.Sprintf(
		"SELECT sample_unique_id FROM genetic_alteration_derived WHERE profile_type = %s "+
			"AND hugo_gene_symbol = %s AND cancer_study_identifier IN %s",
		c.a.str(g.ProfileType), c.a.str(g.HugoGeneSymbol), c.studyIn)
	na, rest := splitNA(g.Values)
	var ors []string
	if na {
		ors = append(ors, fmt.Sprintf("sample_unique_id NOT IN (%s AND alteration_value IS NOT NULL)", src))
	}
	if g.ProfileType == "cna" || g.ProfileType == "gistic" {
		var vals []string
		for _, v := range rest {
			if v.Value != nil {
				vals = append(vals, *v.Value)
			}
		}
		if len(vals) > 0 {
			ors = append(ors, fmt.Sprintf("sample_unique_id IN (%s AND alteration_value IN %s)", src, c.a.strs(vals)))
		}
	} else if len(rest) > 0 {
		ors = append(ors, fmt.Sprintf("sample_unique_id IN (%s AND (%s))", src,
			numericalValuesSQL(c.a, "assumeNotNull(alteration_value)", rest)))
	}
	if len(ors) == 0 {
		return falseClause
	}
	return strings.Join(ors, " OR ")
}

// genericAssayData filters on one generic-assay entity: numeric (LIMIT-VALUE)
// profiles by range, categorical ones by value, at sample or patient level as
// the profile declares.
func (c filterCompiler) genericAssayData(g genericAssayDataFilter) string {
	type kind struct {
		Datatype     string `json:"datatype"`
		PatientLevel uint8  `json:"patientLevel"`
	}
	rows, _ := ch.QueryP[kind](c.s.CH,
		"SELECT datatype, toUInt8(patient_level) AS patientLevel FROM generic_assay_data_derived "+
			"WHERE profile_type = {profileType:String} AND entity_stable_id = {stableID:String} LIMIT 1",
		ch.Params{"profileType": g.ProfileType, "stableID": g.StableID})
	numeric, patient := true, false
	if len(rows) > 0 {
		numeric = rows[0].Datatype == "LIMIT-VALUE"
		patient = rows[0].PatientLevel == 1
	}
	col := "sample_unique_id"
	if patient && !numeric {
		col = "patient_unique_id"
	}
	src := fmt.Sprintf(
		"SELECT %s FROM generic_assay_data_derived WHERE profile_type = %s AND entity_stable_id = %s",
		col, c.a.str(g.ProfileType), c.a.str(g.StableID))
	na, rest := splitNA(g.Values)
	var ors []string
	if na {
		ors = append(ors, fmt.Sprintf("%s NOT IN (%s AND value IS NOT NULL AND NOT %s)", col, src, isNASQL("value")))
	}
	if numeric && len(rest) > 0 {
		ors = append(ors, fmt.Sprintf("%s IN (%s AND datatype = 'LIMIT-VALUE' AND NOT %s AND (%s))",
			col, src, isNASQL("value"), numericalValuesSQL(c.a, "value", rest)))
	} else if !numeric {
		var vals []string
		for _, v := range rest {
			if v.Value != nil {
				vals = append(vals, "value ILIKE "+c.a.str(*v.Value))
			}
		}
		if len(vals) > 0 {
			ors = append(ors, fmt.Sprintf("%s IN (%s AND datatype != 'LIMIT-VALUE' AND (%s))", col, src, strings.Join(vals, " OR ")))
		}
	}
	if len(ors) == 0 {
		return falseClause
	}
	return strings.Join(ors, " OR ")
}

func (c filterCompiler) genericAssaySelection(g genericAssaySelectionFilter) []string {
	col := "sample_unique_id"
	if g.PatientLevel {
		col = "patient_unique_id"
	}
	var out []string
	for _, group := range g.Values {
		var ors []string
		for _, sel := range group {
			cond := fmt.Sprintf("lower(multiIf(%s, 'NA', value)) = lower(%s)", isNASQL("value"), c.a.str(sel.Value))
			if sel.Value == "NA" {
				cond = fmt.Sprintf("(value IS NULL OR %s)", isNASQL("value"))
			}
			ors = append(ors, fmt.Sprintf("(entity_stable_id = %s AND %s)", c.a.str(sel.StableID), cond))
		}
		if len(ors) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s IN (SELECT %s FROM generic_assay_data_derived WHERE profile_type = %s AND datatype != 'LIMIT-VALUE' "+
				"AND sample_unique_id IN (SELECT sample_unique_id FROM sample_derived WHERE cancer_study_identifier IN %s) AND (%s))",
			col, col, c.a.str(g.ProfileType), c.studyIn, strings.Join(ors, " OR ")))
	}
	return out
}

// customData applies user-defined (custom chart) filters. Each custom sample is
// checked against every filter in Go; the passing samples, plus — when a filter
// selects NA — the samples absent from the custom data, are kept.
func (c filterCompiler) customData(filters []clinicalDataFilter) []string {
	if len(filters) == 0 {
		return nil
	}
	type key struct{ study, sample, attr string }
	values := map[key]string{}
	var samples []customDataSample
	seen := map[[2]string]bool{}
	add := func(attr string, d customDataSample) {
		v := strings.ToUpper(d.Value)
		if v == "NAN" || v == "N/A" {
			v = "NA"
		}
		values[key{d.StudyID, d.SampleID, attr}] = v
		k := [2]string{d.StudyID, d.SampleID}
		if !seen[k] {
			seen[k] = true
			samples = append(samples, d)
		}
	}
	datatypes := map[string]string{}
	inline := false
	for _, f := range filters {
		if len(f.Samples) > 0 {
			inline = true
		}
	}
	for _, f := range filters {
		if inline {
			datatypes[f.AttributeID] = f.Datatype
			for _, d := range f.Samples {
				add(f.AttributeID, d)
			}
			continue
		}
		raw, ok := c.s.sessionData("custom_data", f.AttributeID)
		if !ok {
			continue
		}
		var sess struct {
			Datatype string             `json:"datatype"`
			Data     []customDataSample `json:"data"`
		}
		if json.Unmarshal(raw, &sess) != nil {
			continue
		}
		datatypes[f.AttributeID] = sess.Datatype
		for _, d := range sess.Data {
			add(f.AttributeID, d)
		}
	}
	var pass, all []string
	for _, smp := range samples {
		uid := smp.StudyID + "_" + smp.SampleID
		all = append(all, uid)
		ok := true
		for _, f := range filters {
			dt, known := datatypes[f.AttributeID]
			if !known {
				continue
			}
			v, has := values[key{smp.StudyID, smp.SampleID, f.AttributeID}]
			if !customValueMatches(f, dt, v, has) {
				ok = false
				break
			}
		}
		if ok {
			pass = append(pass, uid)
		}
	}
	var out []string
	for _, f := range filters {
		pred := "sample_unique_id IN " + c.a.strs(pass)
		na, _ := splitNA(f.Values)
		if na {
			pred += " OR sample_unique_id NOT IN " + c.a.strs(all)
		}
		out = append(out, pred)
	}
	return out
}

func customValueMatches(f clinicalDataFilter, datatype, v string, has bool) bool {
	if !has {
		na, _ := splitNA(f.Values)
		return na
	}
	if datatype == "STRING" || datatype == "" {
		for _, fv := range f.Values {
			if fv.Value != nil && strings.ToUpper(*fv.Value) == v {
				return true
			}
		}
		return false
	}
	lo, hi, loInc, hiInc, ok := valueRange(v)
	if !ok {
		for _, fv := range f.Values {
			if fv.Value != nil && strings.ToUpper(*fv.Value) == v {
				return true
			}
		}
		return false
	}
	for _, fv := range f.Values {
		if fv.Start == nil && fv.End == nil {
			continue
		}
		if filterEncloses(fv, lo, hi, loInc, hiInc) {
			return true
		}
	}
	return false
}

// valueRange parses a stored numeric value (optionally with a range operator)
// into an interval; ±Inf marks an open side.
func valueRange(v string) (lo, hi float64, loInc, hiInc, ok bool) {
	v = strings.TrimSpace(v)
	inf := math.Inf(1)
	parse := func(s string) (float64, bool) {
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	switch {
	case strings.HasPrefix(v, "<="):
		hi, ok = parse(v[2:])
		return -inf, hi, true, true, ok
	case strings.HasPrefix(v, "<"):
		hi, ok = parse(v[1:])
		return -inf, hi, true, false, ok
	case strings.HasPrefix(v, ">="):
		lo, ok = parse(v[2:])
		return lo, inf, true, true, ok
	case strings.HasPrefix(v, ">"):
		lo, ok = parse(v[1:])
		return lo, inf, false, true, ok
	}
	lo, ok = parse(v)
	return lo, lo, true, true, ok
}

// filterEncloses reports whether the filter range — (start, end], or [x, x]
// when start == end — encloses the value interval.
func filterEncloses(fv dataFilterValue, lo, hi float64, loInc, hiInc bool) bool {
	inf := math.Inf(1)
	start, end := -inf, inf
	if fv.Start != nil {
		start, _ = strconv.ParseFloat(string(*fv.Start), 64)
	}
	if fv.End != nil {
		end, _ = strconv.ParseFloat(string(*fv.End), 64)
	}
	startInc, endInc := false, true
	if fv.Start != nil && fv.End != nil && start == end {
		startInc = true
	}
	if fv.Start == nil {
		startInc = true
	}
	lowerOK := lo > start || (lo == start && (startInc || !loInc))
	upperOK := hi < end || (hi == end && (endInc || !hiInc))
	return lowerOK && upperOK
}

// mergeDataFilters joins consecutive numeric ranges of each filter
// ((0,10] + (10,20] → (0,20]), keeping categorical values as-is
// (DataFilterUtil.mergeDataFilters).
func mergeDataFilters[T any](filters []T, values func(*T) *[]dataFilterValue) []T {
	out := make([]T, len(filters))
	copy(out, filters)
	for i := range out {
		vals := values(&out[i])
		for _, v := range *vals {
			if v.Value == nil && (v.Start == nil && v.End == nil) {
				return out
			}
		}
		var merged, special []dataFilterValue
		var cur *dataFilterValue
		numeric := false
		for _, v := range *vals {
			if v.Value != nil {
				special = append(special, v)
				continue
			}
			numeric = true
			if cur == nil {
				c := v
				cur = &c
			} else if cur.End != nil && v.Start != nil && numLit(cur.End) == numLit(v.Start) {
				cur.End = v.End
			} else {
				merged = append(merged, *cur)
				c := v
				cur = &c
			}
		}
		if numeric && cur != nil {
			merged = append(merged, *cur)
		}
		*vals = append(merged, special...)
	}
	return out
}

// withoutClinical returns a copy of the filter without the clinical filter on
// attrID (a chart's counts ignore its own selection).
func (f studyViewFilter) withoutClinical(attrID string) studyViewFilter {
	out := f
	out.ClinicalDataFilters = nil
	for _, c := range f.ClinicalDataFilters {
		if c.AttributeID != attrID {
			out.ClinicalDataFilters = append(out.ClinicalDataFilters, c)
		}
	}
	return out
}

func (f studyViewFilter) withoutCustom(attrID string) studyViewFilter {
	out := f
	out.CustomDataFilters = nil
	for _, c := range f.CustomDataFilters {
		if c.AttributeID != attrID {
			out.CustomDataFilters = append(out.CustomDataFilters, c)
		}
	}
	return out
}

func (f studyViewFilter) withoutGenomic(gene, profileType string) studyViewFilter {
	out := f
	out.GenomicDataFilters = nil
	for _, g := range f.GenomicDataFilters {
		if !(g.HugoGeneSymbol == gene && g.ProfileType == profileType) {
			out.GenomicDataFilters = append(out.GenomicDataFilters, g)
		}
	}
	return out
}

func (f studyViewFilter) withoutGenericAssay(stableID string) studyViewFilter {
	out := f
	out.GenericAssayDataFilters = nil
	for _, g := range f.GenericAssayDataFilters {
		if g.StableID != stableID {
			out.GenericAssayDataFilters = append(out.GenericAssayDataFilters, g)
		}
	}
	return out
}

func (f studyViewFilter) withoutMutation(gene, profileType, categorization string) studyViewFilter {
	out := f
	out.MutationDataFilters = nil
	for _, m := range f.MutationDataFilters {
		if !(m.HugoGeneSymbol == gene && m.ProfileType == profileType && m.Categorization == categorization) {
			out.MutationDataFilters = append(out.MutationDataFilters, m)
		}
	}
	return out
}

// sampleScope is the filter as a predicate over views keyed by
// (study_id, sample_id).
func (s *State) sampleScope(a *sqlArgs, f studyViewFilter) string {
	if !f.hasSubFilters() {
		if len(f.SampleIdentifiers) > 0 {
			pairs := make([][2]string, len(f.SampleIdentifiers))
			for i, id := range f.SampleIdentifiers {
				pairs[i] = [2]string{id.StudyID, id.id()}
			}
			return "(study_id, sample_id) IN " + a.pairs(pairs)
		}
		if len(f.StudyIDs) > 0 {
			return "study_id IN " + a.strs(f.StudyIDs)
		}
		return falseClause
	}
	return fmt.Sprintf("concat(study_id, '_', sample_id) IN (%s)", s.filterSQL(a, f))
}

// sampleDerivedScope is the filter as a predicate over a *_derived view's
// sample_unique_id or patient_unique_id column.
func (s *State) sampleDerivedScope(a *sqlArgs, f studyViewFilter, uniqueCol string) string {
	if !f.hasSubFilters() && len(f.SampleIdentifiers) == 0 {
		if len(f.StudyIDs) > 0 {
			return "cancer_study_identifier IN " + a.strs(f.StudyIDs)
		}
		return falseClause
	}
	if uniqueCol == "patient_unique_id" {
		return fmt.Sprintf("patient_unique_id IN (SELECT patient_unique_id FROM sample_derived WHERE %s)", s.filterWhere(a, f))
	}
	return fmt.Sprintf("%s IN (%s)", uniqueCol, s.filterSQL(a, f))
}

func (s *State) scopeSampleCount(f studyViewFilter) uint64 {
	type c struct {
		N uint64 `json:"n"`
	}
	a := newArgs()
	rows, _ := queryA[c](s.CH, "SELECT count() AS n FROM sample_derived WHERE "+s.filterWhere(a, f), a)
	if len(rows) > 0 {
		return rows[0].N
	}
	return 0
}
