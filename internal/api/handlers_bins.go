package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/databin"
)

// Study-view histogram endpoints. Values are gathered twice: over the study /
// sample-identifier scope only (bins come from this unfiltered data so a chart
// keeps its bins as filters change) and over the full filter minus the chart's
// own selection (which supplies the counts). Binning itself is the
// Java-equivalent databin package.

// binFilterOpts are the DataBinFilter fields shared by every bin request.
type binFilterOpts struct {
	DisableLogScale     bool                         `json:"disableLogScale"`
	CustomBins          []databin.Decimal            `json:"customBins"`
	BinMethod           *string                      `json:"binMethod"`
	BinsGeneratorConfig *databin.BinsGeneratorConfig `json:"binsGeneratorConfig"`
	Start               *databin.Decimal             `json:"start"`
	End                 *databin.Decimal             `json:"end"`
}

// binMethodField distinguishes an absent binMethod (CUSTOM) from JSON null (NONE).
type binMethodField struct {
	set   bool
	value *string
}

func (o binFilterOpts) options(method string, nonClinical bool, rawBinMethod binMethodField) databin.Options {
	opts := databin.Options{
		Method:              databin.Static,
		CustomBins:          o.CustomBins,
		BinsGeneratorConfig: o.BinsGeneratorConfig,
		DisableLogScale:     o.DisableLogScale,
		Start:               o.Start,
		End:                 o.End,
		NonClinical:         nonClinical,
	}
	if strings.EqualFold(method, "DYNAMIC") {
		opts.Method = databin.Dynamic
	}
	switch {
	case rawBinMethod.set && rawBinMethod.value == nil:
		opts.BinMethod = databin.BinMethodNone
	case rawBinMethod.value != nil:
		opts.BinMethod = databin.BinMethod(strings.ToUpper(*rawBinMethod.value))
	}
	return opts
}

// rawBinMethods reports, per filter index, whether binMethod was present and
// its value (null included), which encoding/json cannot distinguish directly.
func rawBinMethods(filters []json.RawMessage) []binMethodField {
	out := make([]binMethodField, len(filters))
	for i, raw := range filters {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		v, ok := m["binMethod"]
		if !ok {
			continue
		}
		out[i].set = true
		var s *string
		json.Unmarshal(v, &s)
		out[i].value = s
	}
	return out
}

// partialFilter keeps only the study / sample-identifier scope.
func (f studyViewFilter) partial() studyViewFilter {
	return studyViewFilter{StudyIDs: f.StudyIDs, SampleIdentifiers: f.SampleIdentifiers}
}

func toValues(counts []ClinicalDataCount) []databin.Value {
	out := make([]databin.Value, len(counts))
	for i, c := range counts {
		out[i] = databin.Value{Value: c.Value, Count: int(c.Count)}
	}
	return out
}

func countsByAttr(items []ClinicalDataCountItem) map[string][]databin.Value {
	out := map[string][]databin.Value{}
	for _, it := range items {
		out[it.AttributeID] = toValues(it.Counts)
	}
	return out
}

// decodeBinRequest decodes the body into v and returns the raw filter list
// under key so binMethod presence can be inspected.
func decodeBinRequest(r *http.Request, v any, key string) []json.RawMessage {
	var raw map[string]json.RawMessage
	body := json.RawMessage{}
	readBody(r, &body)
	json.Unmarshal(body, v)
	json.Unmarshal(body, &raw)
	var filters []json.RawMessage
	json.Unmarshal(raw[key], &filters)
	return filters
}

func (s *State) clinicalDataBinCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Attributes []struct {
			AttributeID string `json:"attributeId"`
			binFilterOpts
		} `json:"attributes"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	rawFilters := decodeBinRequest(r, &f, "attributes")
	methods := rawBinMethods(rawFilters)
	svf := f.StudyViewFilter
	if len(f.Attributes) == 1 {
		svf = svf.withoutClinical(f.Attributes[0].AttributeID)
	}
	ids := make([]string, len(f.Attributes))
	for i, a := range f.Attributes {
		ids[i] = a.AttributeID
	}
	method := r.URL.Query().Get("dataBinMethod")
	unfiltered := countsByAttr(s.clinicalDataCounts(svf.partial(), ids))
	filtered := countsByAttr(s.clinicalDataCounts(svf, ids))
	out := []databin.Bin{}
	for i, a := range f.Attributes {
		if _, known := unfiltered[a.AttributeID]; !known {
			if _, known := filtered[a.AttributeID]; !known {
				continue
			}
		}
		bins, err := databin.Calculate(a.AttributeID, unfiltered[a.AttributeID], filtered[a.AttributeID],
			a.binFilterOpts.options(method, false, methods[i]))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, bins...)
	}
	writeJSON(w, out)
}

// scopedValueCounts counts, for each (entity, profile type) key, the non-NA
// values of table over two study-view scopes in one query, and adds each
// scope's remaining cases as NA.
// keyCol names the entity column and valueCol the value column; naSQL is the
// predicate that marks a value as NA; unit is the case column counted
// (sample_unique_id, or patient_unique_id for a patient-level profile).
func (s *State) scopedValueCounts(table, keyCol, valueCol, naSQL, unit string, keys [][2]string,
	unfilteredScope, filteredScope studyViewFilter) (unfiltered, filtered map[[2]string][]databin.Value) {
	unfiltered, filtered = map[[2]string][]databin.Value{}, map[[2]string][]databin.Value{}
	if len(keys) == 0 {
		return
	}
	args := newArgs()
	scopes := fmt.Sprintf("WITH unfiltered AS (%s), filtered AS (%s) ",
		s.filterSQL(args, unfilteredScope), s.filterSQL(args, filteredScope))
	type totalRow struct {
		U uint64 `json:"u"`
		F uint64 `json:"f"`
	}
	total := "SELECT (SELECT count() FROM unfiltered) AS u, (SELECT count() FROM filtered) AS f"
	if unit == "patient_unique_id" {
		total = "SELECT (SELECT uniqExact(patient_unique_id) FROM sample_derived WHERE sample_unique_id IN (SELECT sample_unique_id FROM unfiltered)) AS u, " +
			"(SELECT uniqExact(patient_unique_id) FROM sample_derived WHERE sample_unique_id IN (SELECT sample_unique_id FROM filtered)) AS f"
	}
	totals, _ := queryA[totalRow](s.CH, scopes+total, args)
	var totalU, totalF uint64
	if len(totals) > 0 {
		totalU, totalF = totals[0].U, totals[0].F
	}
	type row struct {
		Key         string `json:"key"`
		ProfileType string `json:"profileType"`
		Value       string `json:"value"`
		U           uint64 `json:"u"`
		F           uint64 `json:"f"`
	}
	// Rows are counted per sample; a patient-level profile counts patients.
	countIf := "countIf("
	if unit == "patient_unique_id" {
		countIf = "uniqExactIf(patient_unique_id, "
	}
	rows, _ := queryA[row](s.CH, fmt.Sprintf(scopes+
		"SELECT %[1]s AS key, profile_type AS profileType, %[2]s AS value, "+
		"%[6]ssample_unique_id IN (SELECT sample_unique_id FROM unfiltered)) AS u, "+
		"%[6]ssample_unique_id IN (SELECT sample_unique_id FROM filtered)) AS f "+
		"FROM %[3]s WHERE (%[1]s, profile_type) IN %[4]s AND NOT %[5]s "+
		"AND (sample_unique_id IN (SELECT sample_unique_id FROM unfiltered) OR sample_unique_id IN (SELECT sample_unique_id FROM filtered)) "+
		"GROUP BY key, profileType, value",
		keyCol, valueCol, table, args.pairs(keys), naSQL, countIf), args)
	sumU, sumF := map[[2]string]uint64{}, map[[2]string]uint64{}
	for _, r := range rows {
		k := [2]string{r.Key, r.ProfileType}
		if r.U > 0 {
			unfiltered[k] = append(unfiltered[k], databin.Value{Value: r.Value, Count: int(r.U)})
			sumU[k] += r.U
		}
		if r.F > 0 {
			filtered[k] = append(filtered[k], databin.Value{Value: r.Value, Count: int(r.F)})
			sumF[k] += r.F
		}
	}
	seen := map[[2]string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		if totalU > sumU[k] {
			unfiltered[k] = append(unfiltered[k], databin.Value{Value: "NA", Count: int(totalU - sumU[k])})
		}
		if totalF > sumF[k] {
			filtered[k] = append(filtered[k], databin.Value{Value: "NA", Count: int(totalF - sumF[k])})
		}
	}
	return
}

type GenomicDataBin struct {
	HugoGeneSymbol string           `json:"hugoGeneSymbol"`
	ProfileType    string           `json:"profileType"`
	SpecialValue   *string          `json:"specialValue,omitempty"`
	Start          *databin.Decimal `json:"start,omitempty"`
	End            *databin.Decimal `json:"end,omitempty"`
	Count          int              `json:"count"`
}

func (s *State) genomicDataBinCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		GenomicDataBinFilters []struct {
			HugoGeneSymbol string `json:"hugoGeneSymbol"`
			ProfileType    string `json:"profileType"`
			binFilterOpts
		} `json:"genomicDataBinFilters"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	methods := rawBinMethods(decodeBinRequest(r, &f, "genomicDataBinFilters"))
	svf := f.StudyViewFilter
	if len(f.GenomicDataBinFilters) == 1 {
		g := f.GenomicDataBinFilters[0]
		svf = svf.withoutGenomic(g.HugoGeneSymbol, g.ProfileType)
	}
	keys := make([][2]string, len(f.GenomicDataBinFilters))
	for i, g := range f.GenomicDataBinFilters {
		keys[i] = [2]string{g.HugoGeneSymbol, g.ProfileType}
	}
	unfiltered, filtered := s.scopedValueCounts("genetic_alteration_derived", "hugo_gene_symbol", "alteration_value",
		isNASQL("assumeNotNull(alteration_value)"), "sample_unique_id", keys, svf.partial(), svf)
	method := r.URL.Query().Get("dataBinMethod")
	out := []GenomicDataBin{}
	for i, g := range f.GenomicDataBinFilters {
		bins, err := databin.Calculate(g.HugoGeneSymbol+g.ProfileType, unfiltered[keys[i]], filtered[keys[i]],
			g.binFilterOpts.options(method, true, methods[i]))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, b := range bins {
			out = append(out, GenomicDataBin{HugoGeneSymbol: g.HugoGeneSymbol, ProfileType: g.ProfileType,
				SpecialValue: b.SpecialValue, Start: b.Start, End: b.End, Count: b.Count})
		}
	}
	writeJSON(w, out)
}

type GenericAssayDataBin struct {
	StableID     string           `json:"stableId"`
	ProfileType  string           `json:"profileType"`
	SpecialValue *string          `json:"specialValue,omitempty"`
	Start        *databin.Decimal `json:"start,omitempty"`
	End          *databin.Decimal `json:"end,omitempty"`
	Count        int              `json:"count"`
}

func (s *State) genericAssayDataBinCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		GenericAssayDataBinFilters []struct {
			StableID    string `json:"stableId"`
			ProfileType string `json:"profileType"`
			binFilterOpts
		} `json:"genericAssayDataBinFilters"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	methods := rawBinMethods(decodeBinRequest(r, &f, "genericAssayDataBinFilters"))
	svf := f.StudyViewFilter
	if len(f.GenericAssayDataBinFilters) == 1 {
		svf = svf.withoutGenericAssay(f.GenericAssayDataBinFilters[0].StableID)
	}
	keys := make([][2]string, len(f.GenericAssayDataBinFilters))
	for i, g := range f.GenericAssayDataBinFilters {
		keys[i] = [2]string{g.StableID, g.ProfileType}
	}
	unit := "sample_unique_id"
	if len(f.GenericAssayDataBinFilters) > 0 {
		unit = s.gaCountUnit(f.GenericAssayDataBinFilters[0].ProfileType)
	}
	unfiltered, filtered := s.scopedValueCounts("generic_assay_data_derived", "entity_stable_id", "value",
		isNASQL("value"), unit, keys, svf.partial(), svf)
	method := r.URL.Query().Get("dataBinMethod")
	out := []GenericAssayDataBin{}
	for i, g := range f.GenericAssayDataBinFilters {
		bins, err := databin.Calculate(g.StableID+g.ProfileType, unfiltered[keys[i]], filtered[keys[i]],
			g.binFilterOpts.options(method, true, methods[i]))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, b := range bins {
			out = append(out, GenericAssayDataBin{StableID: g.StableID, ProfileType: g.ProfileType,
				SpecialValue: b.SpecialValue, Start: b.Start, End: b.End, Count: b.Count})
		}
	}
	writeJSON(w, out)
}

// customDataBinCountsFetch bins numeric custom-chart data (custom_data
// sessions) over the filtered samples.
func (s *State) customDataBinCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Attributes []struct {
			AttributeID string `json:"attributeId"`
			binFilterOpts
		} `json:"attributes"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	methods := rawBinMethods(decodeBinRequest(r, &f, "attributes"))
	svf := f.StudyViewFilter
	if len(f.Attributes) == 1 {
		svf = svf.withoutCustom(f.Attributes[0].AttributeID)
	}
	type sample struct {
		StudyID  string `json:"studyId"`
		SampleID string `json:"sampleId"`
	}
	inScope := func(scope studyViewFilter) map[string]bool {
		args := newArgs()
		rows, _ := queryA[sample](s.CH,
			"SELECT cancer_study_identifier AS studyId, sample_stable_id AS sampleId FROM sample_derived WHERE "+
				s.filterWhere(args, scope), args)
		m := map[string]bool{}
		for _, r := range rows {
			m[r.StudyID+"\x00"+r.SampleID] = true
		}
		return m
	}
	unfilteredSamples, filteredSamples := inScope(svf.partial()), inScope(svf)
	method := r.URL.Query().Get("dataBinMethod")
	out := []databin.Bin{}
	for i, a := range f.Attributes {
		raw, ok := s.sessionData("custom_data", a.AttributeID)
		if !ok {
			continue
		}
		var sess struct {
			Data []customDataSample `json:"data"`
		}
		if json.Unmarshal(raw, &sess) != nil {
			continue
		}
		values := func(samples map[string]bool) []databin.Value {
			var out []databin.Value
			n := 0
			for _, d := range sess.Data {
				if samples[d.StudyID+"\x00"+d.SampleID] {
					out = append(out, databin.Value{Value: d.Value, Count: 1})
					n++
				}
			}
			if len(samples) > n {
				out = append(out, databin.Value{Value: "NA", Count: len(samples) - n})
			}
			return out
		}
		bins, err := databin.Calculate(a.AttributeID, values(unfilteredSamples), values(filteredSamples),
			a.binFilterOpts.options(method, false, methods[i]))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, bins...)
	}
	writeJSON(w, out)
}
