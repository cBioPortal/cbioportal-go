package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// Namespace attributes are the custom MAF / SV / discrete-CNA columns a study
// declares with `namespaces:` in its meta file. The importer stores them per
// record as {"<namespace>": {"<column>": value}} (ANNOTATION_JSON in Java,
// namespace_columns here); NamespaceMapper reads them from the mutation,
// structural_variant and sample_cna_event tables. sample_cna_event holds only
// the amplification and deep-deletion events.

func (s *State) routeNamespace(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/namespace-attributes/fetch", s.namespaceAttributesFetch)
	mux.HandleFunc("POST /api/namespace-attributes/counts/fetch", s.namespaceAttributeCountsFetch)
	mux.HandleFunc("POST /api/namespace-data-counts/fetch", s.namespaceDataCountsFetch)
	mux.HandleFunc("POST /api/namespace-data/fetch", s.namespaceDataFetch)
}

type NamespaceAttribute struct {
	OuterKey string `json:"outerKey"`
	InnerKey string `json:"innerKey"`
}

type NamespaceAttributeCount struct {
	OuterKey string `json:"outerKey"`
	InnerKey string `json:"innerKey"`
	Count    uint64 `json:"count"`
}

type NamespaceDataCount struct {
	Value      string `json:"value"`
	Count      uint64 `json:"count"`
	TotalCount uint64 `json:"totalCount"`
}

type NamespaceDataCountItem struct {
	OuterKey string               `json:"outerKey"`
	InnerKey string               `json:"innerKey"`
	Counts   []NamespaceDataCount `json:"counts"`
}

type NamespaceData struct {
	OuterKey  string `json:"outerKey"`
	InnerKey  string `json:"innerKey"`
	SampleID  string `json:"sampleId"`
	StudyID   string `json:"studyId"`
	PatientID string `json:"patientId,omitempty"`
	AttrValue string `json:"attrValue"`
}

// namespaceTables are the record tables in NamespaceMapper's UNION ALL order,
// each with the predicate selecting the rows Java stores.
var namespaceTables = []struct{ table, where string }{
	{"mutation", "1"},
	{"structural_variant", "1"},
	{"cna", "alteration IN ('-2', '2')"},
}

// namespaceRows is the UNION ALL of the namespace-bearing records matching
// scope, selecting cols (expressions over study_id, sample_id and
// namespace_columns) plus the table's position as t.
func namespaceRows(cols, scope string) string {
	parts := make([]string, len(namespaceTables))
	for i, t := range namespaceTables {
		parts[i] = fmt.Sprintf("SELECT %s, %d AS t FROM %s WHERE %s AND %s AND namespace_columns NOT IN ('', '{}')",
			cols, i, t.table, t.where, scope)
	}
	return strings.Join(parts, " UNION ALL ")
}

// nsValue is JSON_UNQUOTE(JSON_EXTRACT(ANNOTATION_JSON, '$.outer.inner')):
// strings unquoted, other JSON values (numbers, booleans, null) as written.
func nsValue(a *sqlArgs, outer, inner string) string {
	o, i := a.str(outer), a.str(inner)
	return fmt.Sprintf("if(JSONType(namespace_columns, %s, %s) = 'String', "+
		"JSONExtractString(namespace_columns, %s, %s), JSONExtractRaw(namespace_columns, %s, %s))",
		o, i, o, i, o, i)
}

// nsHas is JSON_EXTRACT(ANNOTATION_JSON, '$.outer.inner') IS NOT NULL.
func nsHas(a *sqlArgs, outer, inner string) string {
	return fmt.Sprintf("JSONHas(namespace_columns, %s, %s)", a.str(outer), a.str(inner))
}

// samplePairsScope restricts (study_id, sample_id) to the given samples.
func samplePairsScope(a *sqlArgs, ids []identifier) string {
	if len(ids) == 0 {
		return falseClause
	}
	pairs := make([][2]string, len(ids))
	for i, id := range ids {
		pairs[i] = [2]string{id.StudyID, id.id()}
	}
	return "(study_id, sample_id) IN " + a.pairs(pairs)
}

// mysqlJSONKeyLess orders object keys as MySQL stores them in a JSON column
// (and json_keys lists them): shorter keys first, then bytewise.
func mysqlJSONKeyLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// namespaceAttributesFetch serves POST /api/namespace-attributes/fetch
// (NamespaceAttributeServiceImpl.fetchNamespaceAttributes): every outer key
// of the studies' records, and for each the inner keys under it. Keys come in
// NamespaceMapper's order — tables in UNION ALL order, keys within a record in
// MySQL's JSON key order, first occurrence wins. Java reads one arbitrary
// record per table and study (LIMIT 1, no ORDER BY); this reads them all, so a
// namespace is found whichever record carries it.
func (s *State) namespaceAttributesFetch(w http.ResponseWriter, r *http.Request) {
	var studyIDs []string
	readBody(r, &studyIDs)
	out := []NamespaceAttribute{}
	if len(studyIDs) == 0 {
		writeJSON(w, out)
		return
	}
	type keyRow struct {
		T     uint8  `json:"t"`
		Outer string `json:"outer"`
		Inner string `json:"inner"`
	}
	rows, err := ch.QueryP[keyRow](s.CH, fmt.Sprintf(
		"SELECT DISTINCT t, outer, arrayJoin(JSONExtractKeys(namespace_columns, outer)) AS inner FROM ("+
			"SELECT t, namespace_columns, arrayJoin(JSONExtractKeys(namespace_columns)) AS outer FROM (%s))",
		namespaceRows("namespace_columns", "study_id IN {studies:Array(String)}")), ch.Params{"studies": studyIDs})
	if err != nil {
		fail(w, r, err)
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.T != b.T {
			return a.T < b.T
		}
		if a.Outer != b.Outer {
			return mysqlJSONKeyLess(a.Outer, b.Outer)
		}
		return mysqlJSONKeyLess(a.Inner, b.Inner)
	})
	var outers []string
	inners := map[string][]string{}
	seen := map[[2]string]bool{}
	for _, row := range rows {
		if _, ok := inners[row.Outer]; !ok {
			outers = append(outers, row.Outer)
			inners[row.Outer] = nil
		}
		k := [2]string{row.Outer, row.Inner}
		if !seen[k] {
			seen[k] = true
			inners[row.Outer] = append(inners[row.Outer], row.Inner)
		}
	}
	for _, o := range outers {
		for _, i := range inners[o] {
			out = append(out, NamespaceAttribute{OuterKey: o, InnerKey: i})
		}
	}
	writeJSON(w, out)
}

// namespaceAttributeCountsFetch serves POST /api/namespace-attributes/counts/fetch:
// per requested attribute, the number of the given samples with a non-null
// value in any of their records. Attributes no sample has are left out.
func (s *State) namespaceAttributeCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers   []identifier         `json:"sampleIdentifiers"`
		NamespaceAttributes []NamespaceAttribute `json:"namespaceAttributes"`
	}
	readBody(r, &f)
	out := []NamespaceAttributeCount{}
	if len(f.SampleIdentifiers) == 0 || len(f.NamespaceAttributes) == 0 {
		writeJSON(w, out)
		return
	}
	args := newArgs()
	scope := samplePairsScope(args, f.SampleIdentifiers)
	parts := make([]string, len(f.NamespaceAttributes))
	for i, a := range f.NamespaceAttributes {
		parts[i] = fmt.Sprintf("SELECT %d AS a, uniqExact(study_id, sample_id) AS count FROM (%s) WHERE has AND value != 'null'",
			i, namespaceRows(fmt.Sprintf("study_id, sample_id, %s AS has, %s AS value",
				nsHas(args, a.OuterKey, a.InnerKey), nsValue(args, a.OuterKey, a.InnerKey)), scope))
	}
	type countRow struct {
		A     int    `json:"a"`
		Count uint64 `json:"count"`
	}
	rows, err := queryA[countRow](s.CH, "SELECT * FROM ("+strings.Join(parts, " UNION ALL ")+") ORDER BY a", args)
	if err != nil {
		fail(w, r, err)
		return
	}
	for _, row := range rows {
		if row.Count == 0 {
			continue
		}
		a := f.NamespaceAttributes[row.A]
		out = append(out, NamespaceAttributeCount{OuterKey: a.OuterKey, InnerKey: a.InnerKey, Count: row.Count})
	}
	writeJSON(w, out)
}

// namespaceDataFilter is a study-view selection on a namespace attribute:
// each inner list of values is a union, the lists are intersected.
type namespaceDataFilter struct {
	OuterKey string              `json:"outerKey"`
	InnerKey string              `json:"innerKey"`
	Values   [][]dataFilterValue `json:"values"`
}

// namespaceDataCountsFetch serves POST /api/namespace-data-counts/fetch
// (StudyViewController.fetchNamespaceDataCounts): for each attribute, the
// distinct-sample count and record count of every value among the filtered
// samples. An attribute without values yields a null entry, as Java's
// stream maps it to null.
func (s *State) namespaceDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Attributes      []NamespaceAttribute `json:"attributes"`
		StudyViewFilter struct {
			studyViewFilter
			NamespaceDataFilters []namespaceDataFilter `json:"namespaceDataFilters"`
		} `json:"studyViewFilter"`
	}
	readBody(r, &f)
	type sample struct {
		StudyID  string `json:"studyId"`
		SampleID string `json:"sampleId"`
	}
	args := newArgs()
	samples, err := queryA[sample](s.CH, fmt.Sprintf(
		"SELECT cancer_study_identifier AS studyId, sample_stable_id AS sampleId FROM %s "+
			"ORDER BY cancer_study_identifier, sample_stable_id", s.filterFrom(args, f.StudyViewFilter.studyViewFilter)), args)
	if err != nil {
		fail(w, r, err)
		return
	}
	ids := make([]identifier, len(samples))
	for i, sm := range samples {
		ids[i] = identifier{StudyID: sm.StudyID, SampleID: sm.SampleID}
	}
	if len(f.StudyViewFilter.NamespaceDataFilters) > 0 {
		if ids, err = s.applyNamespaceDataFilters(ids, f.StudyViewFilter.NamespaceDataFilters); err != nil {
			fail(w, r, err)
			return
		}
	}
	out := []*NamespaceDataCountItem{}
	if len(ids) == 0 {
		writeJSON(w, out)
		return
	}
	scope := samplePairsScope(args, ids)
	for _, a := range f.Attributes {
		counts, err := queryA[NamespaceDataCount](s.CH, fmt.Sprintf(
			"SELECT value, uniqExact(study_id, sample_id) AS count, count() AS totalCount FROM (%s) "+
				"WHERE has AND value != 'null' GROUP BY value ORDER BY value",
			namespaceRows(fmt.Sprintf("study_id, sample_id, %s AS has, %s AS value",
				nsHas(args, a.OuterKey, a.InnerKey), nsValue(args, a.OuterKey, a.InnerKey)), scope)), args)
		if err != nil {
			fail(w, r, err)
			return
		}
		if len(counts) == 0 {
			out = append(out, nil)
			continue
		}
		out = append(out, &NamespaceDataCountItem{OuterKey: a.OuterKey, InnerKey: a.InnerKey, Counts: counts})
	}
	writeJSON(w, out)
}

// applyNamespaceDataFilters ports StudyViewFilterApplier.filterNamespaceData:
// a sample passes one list of values when any of its (upper-cased) values for
// the attribute is among them, or it has none and "NA" is selected. Each list
// is applied to the full sample set; the first non-empty result seeds the
// selection and later results are intersected with it.
func (s *State) applyNamespaceDataFilters(ids []identifier, filters []namespaceDataFilter) ([]identifier, error) {
	if len(ids) == 0 {
		return ids, nil
	}
	args := newArgs()
	scope := samplePairsScope(args, ids)
	type valueRow struct {
		StudyID  string `json:"study_id"`
		SampleID string `json:"sample_id"`
		Value    string `json:"value"`
	}
	values := map[string]map[string][]string{}
	for _, nf := range filters {
		attr := nf.OuterKey + nf.InnerKey
		if values[attr] != nil {
			continue
		}
		rows, err := queryA[valueRow](s.CH, fmt.Sprintf(
			"SELECT DISTINCT study_id, sample_id, value FROM (%s) WHERE has",
			namespaceRows(fmt.Sprintf("study_id, sample_id, %s AS has, %s AS value",
				nsHas(args, nf.OuterKey, nf.InnerKey), nsValue(args, nf.OuterKey, nf.InnerKey)), scope)), args)
		if err != nil {
			return nil, err
		}
		m := map[string][]string{}
		for _, row := range rows {
			k := row.StudyID + "\x00" + row.SampleID
			m[k] = append(m[k], strings.ToUpper(row.Value))
		}
		values[attr] = m
	}
	var selected []identifier
	for _, nf := range filters {
		byCase := values[nf.OuterKey+nf.InnerKey]
		for _, list := range nf.Values {
			want := map[string]bool{}
			for _, v := range list {
				if v.Value != nil {
					want[strings.ToUpper(*v.Value)] = true
				}
			}
			var pass []identifier
			for _, id := range ids {
				vals, ok := byCase[id.StudyID+"\x00"+id.SampleID]
				hit := !ok && want["NA"]
				for _, v := range vals {
					hit = hit || want[v]
				}
				if hit {
					pass = append(pass, id)
				}
			}
			if len(selected) == 0 {
				selected = pass
				continue
			}
			keep := map[identifier]bool{}
			for _, id := range pass {
				keep[id] = true
			}
			kept := selected[:0]
			for _, id := range selected {
				if keep[id] {
					kept = append(kept, id)
				}
			}
			selected = kept
		}
	}
	return selected, nil
}

// namespaceDataFetch serves POST /api/namespace-data/fetch
// (NamespaceDataServiceImpl.fetchNamespaceDataForComparison): for each
// requested value, the samples among the given ones with a record carrying
// that value, one entry per sample.
func (s *State) namespaceDataFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers  []identifier       `json:"sampleIdentifiers"`
		NamespaceAttribute NamespaceAttribute `json:"namespaceAttribute"`
		Values             []string           `json:"values"`
	}
	readBody(r, &f)
	out := []NamespaceData{}
	if len(f.SampleIdentifiers) == 0 {
		writeJSON(w, out)
		return
	}
	a := f.NamespaceAttribute
	args := newArgs()
	scope := samplePairsScope(args, f.SampleIdentifiers)
	studies := make([]string, len(f.SampleIdentifiers))
	for i, id := range f.SampleIdentifiers {
		studies[i] = id.StudyID
	}
	type row struct {
		StudyID   string `json:"studyId"`
		SampleID  string `json:"sampleId"`
		PatientID string `json:"patientId"`
	}
	for _, v := range f.Values {
		rows, err := queryA[row](s.CH, fmt.Sprintf(
			"SELECT DISTINCT n.study_id AS studyId, n.sample_id AS sampleId, sm.patient_id AS patientId FROM (%s) n "+
				"JOIN %s sm ON sm.study_id = n.study_id AND sm.sample_id = n.sample_id "+
				"WHERE n.value = %s ORDER BY studyId, sampleId",
			namespaceRows(fmt.Sprintf("study_id, sample_id, %s AS value", nsValue(args, a.OuterKey, a.InnerKey)), scope),
			byStudies(studies).samples(args), args.str(v)), args)
		if err != nil {
			fail(w, r, err)
			return
		}
		for _, rw := range rows {
			out = append(out, NamespaceData{OuterKey: a.OuterKey, InnerKey: a.InnerKey, SampleID: rw.SampleID,
				StudyID: rw.StudyID, PatientID: rw.PatientID, AttrValue: v})
		}
	}
	writeJSON(w, out)
}
