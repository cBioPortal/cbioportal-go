package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/databin"
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

// valueCountsWithNA groups the values selected by sql (which must yield a
// `value` column over the given unit) and adds the unit total minus the
// counted cases as NA.
func (s *State) valueCountsWithNA(sql string, total uint64) []databin.Value {
	type row struct {
		Value string `json:"value"`
		C     uint64 `json:"c"`
	}
	rows, _ := ch.Query[row](s.CH, sql)
	out := make([]databin.Value, 0, len(rows)+1)
	var sum uint64
	for _, r := range rows {
		out = append(out, databin.Value{Value: r.Value, Count: int(r.C)})
		sum += r.C
	}
	if total > sum {
		out = append(out, databin.Value{Value: "NA", Count: int(total - sum)})
	}
	return out
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
	values := func(scope studyViewFilter, gene, profileType string) []databin.Value {
		return s.valueCountsWithNA(fmt.Sprintf(
			"SELECT alteration_value AS value, count() AS c FROM genetic_alteration_derived "+
				"WHERE profile_type = '%s' AND hugo_gene_symbol = '%s' AND NOT %s AND sample_unique_id IN (%s) GROUP BY alteration_value",
			sqlLit(profileType), sqlLit(gene), isNASQL("assumeNotNull(alteration_value)"), s.filterSQL(scope)),
			s.scopeSampleCount(scope))
	}
	method := r.URL.Query().Get("dataBinMethod")
	out := []GenomicDataBin{}
	for i, g := range f.GenomicDataBinFilters {
		bins, err := databin.Calculate(g.HugoGeneSymbol+g.ProfileType,
			values(svf.partial(), g.HugoGeneSymbol, g.ProfileType), values(svf, g.HugoGeneSymbol, g.ProfileType),
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
	values := func(scope studyViewFilter, stableID, profileType string) []databin.Value {
		return s.valueCountsWithNA(fmt.Sprintf(
			"SELECT value, count() AS c FROM generic_assay_data_derived "+
				"WHERE profile_type = '%s' AND entity_stable_id = '%s' AND NOT %s AND sample_unique_id IN (%s) GROUP BY value",
			sqlLit(profileType), sqlLit(stableID), isNASQL("value"), s.filterSQL(scope)),
			s.scopeSampleCount(scope))
	}
	method := r.URL.Query().Get("dataBinMethod")
	out := []GenericAssayDataBin{}
	for i, g := range f.GenericAssayDataBinFilters {
		bins, err := databin.Calculate(g.StableID+g.ProfileType,
			values(svf.partial(), g.StableID, g.ProfileType), values(svf, g.StableID, g.ProfileType),
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
		rows, _ := ch.Query[sample](s.CH, fmt.Sprintf(
			"SELECT cancer_study_identifier AS studyId, sample_stable_id AS sampleId FROM sample_derived WHERE %s",
			s.filterWhere(scope)))
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
