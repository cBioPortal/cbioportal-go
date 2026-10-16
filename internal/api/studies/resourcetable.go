package studies

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// routeResourceTable registers the resource table endpoints
// (ResourceTableController): the tabs of a cohort's resources, one page of
// a resource's rows, and that resource's columns, filter options and
// counts. All read the resource view, restricted to the request's studyIds.
func (s Server) routeResourceTable(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/resource-table/tabs/fetch", s.resourceTableTabs)
	mux.HandleFunc("POST /api/resource-table/query/fetch", s.resourceTableRows)
	mux.HandleFunc("POST /api/resource-table/metadata/fetch", s.resourceTableMetadata)
}

const (
	// maxFacetValues is the most distinct values a categorical metadata
	// column offers as a filter list; a column with more offers none.
	maxFacetValues = 500
	// keyDiscoveryRows is how many rows key discovery and numeric
	// detection read.
	keyDiscoveryRows = 100_000
	// keyDiscoveryMaxMemory caps the memory of the key discovery query.
	keyDiscoveryMaxMemory = 2 << 30
	// metadataColumnPrefix marks a column (or filter, or sortBy) that is a
	// key inside a row's metadata.
	metadataColumnPrefix = "metadata:"
)

// resourceIdentifier is a PatientIdentifier or SampleIdentifier: an id
// with its study. Java skips a null identifier and a null id.
type resourceIdentifier struct {
	StudyID   *string `json:"studyId"`
	PatientID *string `json:"patientId"`
	SampleID  *string `json:"sampleId"`
}

// resourceColumnFilter is a ResourceColumnFilter.
type resourceColumnFilter struct {
	ColumnID *string  `json:"columnId"`
	Operator *string  `json:"operator"`
	Values   []string `json:"values"`
}

// resourceTableQuery is the body of both table endpoints (ResourceTableQuery);
// the tabs endpoint reads its cohort fields only (ResourceTabsRequest).
type resourceTableQuery struct {
	StudyIDs           []string                `json:"studyIds"`
	ResourceID         *string                 `json:"resourceId"`
	PatientIdentifiers []*resourceIdentifier   `json:"patientIdentifiers"`
	SampleIdentifiers  []*resourceIdentifier   `json:"sampleIdentifiers"`
	Search             *string                 `json:"search"`
	PageNumber         int                     `json:"pageNumber"`
	PageSize           int                     `json:"pageSize"`
	SortBy             *string                 `json:"sortBy"`
	Direction          *string                 `json:"direction"`
	Filters            []*resourceColumnFilter `json:"filters"`
}

// answerable: a query naming no study or no resource answers nothing.
func (q *resourceTableQuery) answerable() bool {
	return len(q.StudyIDs) > 0 && q.ResourceID != nil && strings.TrimSpace(*q.ResourceID) != ""
}

// distinctIDs are the identifiers' ids in first-seen order, without nulls
// (ResourceCohort).
func distinctIDs(ids []*resourceIdentifier, id func(*resourceIdentifier) *string) []string {
	out := []string{}
	for _, x := range ids {
		if x == nil {
			continue
		}
		if v := id(x); v != nil && !slices.Contains(out, *v) {
			out = append(out, *v)
		}
	}
	return out
}

// distinctTuples are the identifiers as "<studyId>_<id>", as Java joins
// them (a null part reads "null").
func distinctTuples(ids []*resourceIdentifier, id func(*resourceIdentifier) *string) []string {
	str := func(p *string) string {
		if p == nil {
			return "null"
		}
		return *p
	}
	out := []string{}
	for _, x := range ids {
		if x == nil {
			continue
		}
		if v := str(x.StudyID) + "_" + str(id(x)); !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func patientOf(x *resourceIdentifier) *string { return x.PatientID }
func sampleOf(x *resourceIdentifier) *string  { return x.SampleID }

// cohortWhere restricts rdata (the resource view) to the request's studies
// and cohort. A row matches at the level it lives at: a sample-level row by
// its sample, a patient-level row by its patient. Stable ids are unique
// only within a study, so with more than one study the ids are matched
// with their study too.
func cohortWhere(q *resourceTableQuery, p ch.Params) []string {
	p["studies"] = q.StudyIDs
	where := []string{"rdata.study_id IN {studies:Array(String)}"}
	multiStudy := len(distinctStrings(q.StudyIDs)) > 1
	level := func(col, ids, tuples string, list []*resourceIdentifier, id func(*resourceIdentifier) *string) string {
		p[ids], p[tuples] = distinctIDs(list, id), distinctTuples(list, id)
		cond := fmt.Sprintf("rdata.%s IN {%s:Array(String)}", col, ids)
		if multiStudy {
			cond += fmt.Sprintf(" AND concat(rdata.study_id, '_', rdata.%s) IN {%s:Array(String)}", col, tuples)
		}
		return cond
	}
	hasPatients, hasSamples := len(q.PatientIdentifiers) > 0, len(q.SampleIdentifiers) > 0
	switch {
	case hasPatients && hasSamples:
		where = append(where, "((rdata.sample_id IS NOT NULL AND "+
			level("sample_id", "sids", "stuples", q.SampleIdentifiers, sampleOf)+
			") OR (rdata.sample_id IS NULL AND "+
			level("patient_id", "pids", "ptuples", q.PatientIdentifiers, patientOf)+"))")
	case hasPatients:
		where = append(where, level("patient_id", "pids", "ptuples", q.PatientIdentifiers, patientOf))
	case hasSamples:
		where = append(where, level("sample_id", "sids", "stuples", q.SampleIdentifiers, sampleOf))
	}
	return where
}

func distinctStrings(ss []string) []string {
	out := []string{}
	for _, s := range ss {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// tableWhere is ResourceTableBaseFilters: the resource, the cohort, the
// search box and, unless withoutFilters, the column filters.
func tableWhere(q *resourceTableQuery, p ch.Params, withoutFilters bool) string {
	p["resource"] = *q.ResourceID
	where := append([]string{"rdata.resource_id = {resource:String}"}, cohortWhere(q, p)...)
	if q.Search != nil && *q.Search != "" {
		p["search"] = *q.Search
		where = append(where, "(rdata.patient_id LIKE concat('%', {search:String}, '%') "+
			"OR rdata.sample_id LIKE concat('%', {search:String}, '%') "+
			"OR rdata.url LIKE concat('%', {search:String}, '%') "+
			"OR (rdata.metadata IS NOT NULL AND position(rdata.metadata, {search:String}) > 0))")
	}
	if !withoutFilters {
		for i, f := range q.Filters {
			if f == nil || f.ColumnID == nil || f.Operator == nil {
				continue
			}
			where = append(where, columnFilterSQL(f, i, p))
		}
	}
	return strings.Join(where, " AND ")
}

// metadataValueSQL is the text of one metadata key of a row.
func metadataValueSQL(keyParam string) string {
	return "JSONExtractString(ifNull(rdata.metadata, '{}'), {" + keyParam + ":String})"
}

// columnFilterSQL is one column filter's condition (ApplyStringFilterTemplate
// and, for a metadata key, ApplyStringFilterOnMetadata, which also has the
// range operator). A column without a filter matches every row.
func columnFilterSQL(f *resourceColumnFilter, i int, p ch.Params) string {
	id, op := *f.ColumnID, *f.Operator
	var col string
	metadata := strings.HasPrefix(id, metadataColumnPrefix)
	switch {
	case id == "patientId":
		col = "rdata.patient_id"
	case id == "sampleId":
		col = "rdata.sample_id"
	case id == "url":
		col = "rdata.url"
	case id == "displayName":
		col = "rdata.display_name"
	case metadata:
		key := fmt.Sprintf("f%dk", i)
		p[key] = strings.TrimPrefix(id, metadataColumnPrefix)
		col = metadataValueSQL(key)
	default:
		return "1 = 1"
	}
	value := func(j int) string {
		name := fmt.Sprintf("f%dv%d", i, j)
		p[name] = f.Values[j]
		return "{" + name + ":String}"
	}
	list := func() string {
		vals := make([]string, len(f.Values))
		for j := range f.Values {
			vals[j] = value(j)
		}
		return "(" + strings.Join(vals, ", ") + ")"
	}
	in := op == "in" || op == "equalsAny"
	n := len(f.Values)
	switch {
	case in && n == 0:
		return "1 = 0"
	case op == "contains" && n > 0:
		return col + " LIKE concat('%', " + value(0) + ", '%')"
	case in:
		return col + " IN " + list()
	case (op == "ne" || op == "notEquals") && n > 0:
		return col + " != " + value(0)
	case op == "notIn" && n > 0:
		return col + " NOT IN " + list()
	case metadata && op == "between" && n == 2:
		return "toFloat64OrNull(" + col + ") BETWEEN toFloat64(" + value(0) + ") AND toFloat64(" + value(1) + ")"
	case n > 0:
		return col + " = " + value(0)
	}
	return "1 = 1"
}

// tableOrder is ResourceTableOrderBy: the requested column, or patient then
// sample, and then the file order, so paging is stable. A metadata key
// sorts blanks last, then by its number, then by its text.
func tableOrder(q *resourceTableQuery, p ch.Params) string {
	dir := " ASC"
	if q.Direction != nil && (*q.Direction == "desc" || *q.Direction == "DESC") {
		dir = " DESC"
	}
	sortBy := ""
	if q.SortBy != nil {
		sortBy = *q.SortBy
	}
	var order string
	switch {
	case sortBy == "studyId":
		order = "rdata.study_id" + dir
	case sortBy == "patientId":
		order = "rdata.patient_id" + dir
	case sortBy == "sampleId":
		order = "rdata.sample_id" + dir
	case sortBy == "url":
		order = "rdata.url" + dir
	case sortBy == "displayName":
		order = "rdata.display_name" + dir
	case sortBy == "resourceDisplayName":
		order = "rdef.display_name" + dir
	case strings.HasPrefix(sortBy, metadataColumnPrefix):
		p["sortkey"] = strings.TrimPrefix(sortBy, metadataColumnPrefix)
		v := metadataValueSQL("sortkey")
		order = v + " = '' ASC, toFloat64OrNull(" + v + ")" + dir + ", " + v + dir
	default:
		order = "rdata.patient_id ASC, rdata.sample_id ASC"
	}
	return " ORDER BY " + order + ", rdata.study_id ASC, rdata.source_file ASC, rdata.row_num ASC"
}

const resourceTableFrom = " FROM resource rdata LEFT JOIN resource_definition rdef " +
	"ON rdef.resource_id = rdata.resource_id AND rdef.study_id = rdata.study_id"

// readResourceTableQuery reads the request body; a missing body is a query
// that answers nothing.
func readResourceTableQuery(r *http.Request) *resourceTableQuery {
	var q *resourceTableQuery
	core.ReadBody(r, &q)
	if q == nil {
		q = &resourceTableQuery{}
	}
	return q
}

// ResourceTableTab is a ResourceTableTab.
type ResourceTableTab struct {
	ResourceID   string `json:"resourceId"`
	Label        string `json:"label"`
	TotalCount   uint64 `json:"totalCount"`
	PatientCount uint64 `json:"patientCount"`
	SampleCount  uint64 `json:"sampleCount"`
}

// resourceTableTabs — POST /api/resource-table/tabs/fetch: each resource the
// cohort has rows of, labelled with the display name of the first study (by
// id) that names it, with its row, patient and sample counts.
func (s Server) resourceTableTabs(w http.ResponseWriter, r *http.Request) {
	q := readResourceTableQuery(r)
	if len(q.StudyIDs) == 0 {
		core.WriteJSON(w, []ResourceTableTab{})
		return
	}
	p := ch.Params{}
	where := strings.Join(cohortWhere(q, p), " AND ")
	rows, err := QueryView[ResourceTableTab](s.CH,
		"SELECT rdata.resource_id AS resourceId, "+
			"coalesce(nullIf(argMinIf(rdef.display_name, rdata.study_id, rdef.display_name != ''), ''), rdata.resource_id) AS label, "+
			"count() AS totalCount, "+
			"count(DISTINCT if(rdata.patient_id IS NULL, NULL, concat(rdata.study_id, '_', rdata.patient_id))) AS patientCount, "+
			"count(DISTINCT if(rdata.sample_id IS NULL, NULL, concat(rdata.study_id, '_', rdata.sample_id))) AS sampleCount"+
			resourceTableFrom+" WHERE "+where+" GROUP BY rdata.resource_id ORDER BY resourceId", p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, rows)
}

// ResourceTableRow is a ResourceTableRow, nulls left out.
type ResourceTableRow struct {
	StudyID             string          `json:"studyId"`
	ResourceID          string          `json:"resourceId"`
	ResourceDisplayName string          `json:"resourceDisplayName"`
	ResourceType        string          `json:"resourceType"`
	PatientID           *string         `json:"patientId,omitempty"`
	SampleID            *string         `json:"sampleId,omitempty"`
	URL                 string          `json:"url"`
	DisplayName         *string         `json:"displayName,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

type resourceTableDBRow struct {
	StudyID             string  `json:"studyId"`
	ResourceID          string  `json:"resourceId"`
	ResourceDisplayName string  `json:"resourceDisplayName"`
	ResourceType        string  `json:"resourceType"`
	PatientID           *string `json:"patientId"`
	SampleID            *string `json:"sampleId"`
	URL                 string  `json:"url"`
	DisplayName         *string `json:"displayName"`
	Metadata            *string `json:"metadata"`
}

// resourceTableRows — POST /api/resource-table/query/fetch: one page of the
// resource's rows.
func (s Server) resourceTableRows(w http.ResponseWriter, r *http.Request) {
	q := readResourceTableQuery(r)
	if !q.answerable() {
		core.WriteJSON(w, []ResourceTableRow{})
		return
	}
	p := ch.Params{}
	where := tableWhere(q, p, false)
	order := tableOrder(q, p)
	limit := max(q.PageSize, 0)
	p["limit"], p["offset"] = int64(limit), int64(max(q.PageNumber, 0))*int64(limit)
	rows, err := QueryView[resourceTableDBRow](s.CH,
		"SELECT rdata.study_id AS studyId, rdata.resource_id AS resourceId, "+
			"coalesce(nullIf(rdef.display_name, ''), rdata.resource_id) AS resourceDisplayName, "+
			"rdata.resource_scope AS resourceType, rdata.patient_id AS patientId, rdata.sample_id AS sampleId, "+
			"rdata.url AS url, rdata.display_name AS displayName, rdata.metadata AS metadata"+
			resourceTableFrom+" WHERE "+where+order+" LIMIT {limit:Int64} OFFSET {offset:Int64}", p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]ResourceTableRow, len(rows))
	for i, row := range rows {
		out[i] = ResourceTableRow{
			StudyID: row.StudyID, ResourceID: row.ResourceID, ResourceDisplayName: row.ResourceDisplayName,
			ResourceType: row.ResourceType, PatientID: row.PatientID, SampleID: row.SampleID, URL: row.URL,
			DisplayName: row.DisplayName, Metadata: metadataJSON(row.Metadata),
		}
	}
	core.WriteJSON(w, out)
}

// ResourceColumnInfo is a ResourceColumnInfo, nulls left out.
type ResourceColumnInfo struct {
	ID               string  `json:"id"`
	Label            string  `json:"label"`
	Source           string  `json:"source"`
	DataType         string  `json:"dataType"`
	Filterable       bool    `json:"filterable"`
	Sortable         bool    `json:"sortable"`
	VisibleByDefault bool    `json:"visibleByDefault"`
	Description      *string `json:"description,omitempty"`
}

// builtinResourceColumns are the fixed columns every resource table has.
func builtinResourceColumns() []ResourceColumnInfo {
	col := func(id, label, typ string, filter bool) ResourceColumnInfo {
		return ResourceColumnInfo{ID: id, Label: label, Source: "builtin", DataType: typ,
			Filterable: filter, Sortable: filter, VisibleByDefault: true}
	}
	return []ResourceColumnInfo{
		col("patientId", "Patient ID", "string", true),
		col("sampleId", "Sample ID", "string", true),
		col("url", "Link", "link", false),
		col("displayName", "Display Name", "string", true),
	}
}

// ResourceFacetOption is a ResourceFacetOption.
type ResourceFacetOption struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}

// ResourceNumericRange is a ResourceNumericRange.
type ResourceNumericRange struct {
	Min core.JavaDouble `json:"min"`
	Max core.JavaDouble `json:"max"`
}

// orderedJSON writes a LinkedHashMap: its members in insertion order.
type orderedJSON[V any] struct {
	keys []string
	vals map[string]V
}

func (m *orderedJSON[V]) put(k string, v V) {
	if m.vals == nil {
		m.vals = map[string]V{}
	}
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

func (m orderedJSON[V]) MarshalJSON() ([]byte, error) {
	var o core.JSONObject
	for _, k := range m.keys {
		o.Add(k, m.vals[k])
	}
	return o.Bytes()
}

// ResourceTableMetadataResult is a ResourceTableMetadataResult.
type ResourceTableMetadataResult struct {
	Columns              []ResourceColumnInfo               `json:"columns"`
	TotalRowCount        uint64                             `json:"totalRowCount"`
	FilteredPatientCount uint64                             `json:"filteredPatientCount"`
	FilteredSampleCount  uint64                             `json:"filteredSampleCount"`
	Facets               orderedJSON[[]ResourceFacetOption] `json:"facets"`
	FacetRanges          orderedJSON[ResourceNumericRange]  `json:"facetRanges"`
	DistinctValueCounts  orderedJSON[uint64]                `json:"distinctValueCounts"`
}

type resourceTableCounts struct {
	RowCount                     uint64 `json:"rowCount"`
	PatientCount                 uint64 `json:"patientCount"`
	SampleCount                  uint64 `json:"sampleCount"`
	DistinctResourceDisplayNames uint64 `json:"distinctResourceDisplayNames"`
	DistinctScopes               uint64 `json:"distinctScopes"`
	DistinctDisplayNames         uint64 `json:"distinctDisplayNames"`
}

// resourceTableMetadata — POST /api/resource-table/metadata/fetch: the
// columns, their filter options and the counts over the whole filtered set.
// The counts apply the column filters; the columns and filter options do
// not, so a filter never hides its own options.
func (s Server) resourceTableMetadata(w http.ResponseWriter, r *http.Request) {
	q := readResourceTableQuery(r)
	out := ResourceTableMetadataResult{Columns: []ResourceColumnInfo{}}
	if !q.answerable() {
		core.WriteJSON(w, out)
		return
	}
	p := ch.Params{}
	counts, err := QueryView[resourceTableCounts](s.CH,
		"SELECT count() AS rowCount, "+
			"countDistinctIf(concat(rdata.study_id, '_', rdata.patient_id), rdata.patient_id IS NOT NULL AND rdata.resource_scope IN ('PATIENT', 'SAMPLE')) AS patientCount, "+
			"countDistinctIf(concat(rdata.study_id, '_', rdata.sample_id), rdata.sample_id IS NOT NULL AND rdata.resource_scope = 'SAMPLE') AS sampleCount, "+
			"uniqExact(coalesce(nullIf(rdef.display_name, ''), rdata.resource_id)) AS distinctResourceDisplayNames, "+
			"uniqExact(rdata.resource_scope) AS distinctScopes, "+
			"uniqExact(ifNull(rdata.display_name, '')) AS distinctDisplayNames"+
			resourceTableFrom+" WHERE "+tableWhere(q, p, false), p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	var c resourceTableCounts
	if len(counts) > 0 {
		c = counts[0]
	}
	meta, err := s.resourceMetadataView(q)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out.Columns = append(builtinResourceColumns(), meta.columns...)
	out.TotalRowCount, out.FilteredPatientCount, out.FilteredSampleCount = c.RowCount, c.PatientCount, c.SampleCount
	out.Facets, out.FacetRanges = meta.facets, meta.ranges
	out.DistinctValueCounts.put("resourceDisplayName", c.DistinctResourceDisplayNames)
	out.DistinctValueCounts.put("resourceType", c.DistinctScopes)
	out.DistinctValueCounts.put("displayName", c.DistinctDisplayNames)
	core.WriteJSON(w, out)
}

type resourceMetadataView struct {
	columns []ResourceColumnInfo
	facets  orderedJSON[[]ResourceFacetOption]
	ranges  orderedJSON[ResourceNumericRange]
}

type resourceKeyStats struct {
	MetaKey       string `json:"metaKey"`
	NonBlankCount uint64 `json:"nonBlankCount"`
	NumericCount  uint64 `json:"numericCount"`
}

// resourceMetadataView builds the metadata columns, the value lists of the
// categorical ones and the ranges of the numeric ones
// (ClickhouseResourceDataRepository.getResourceTableMetadata), over the
// query without its column filters.
func (s Server) resourceMetadataView(q *resourceTableQuery) (resourceMetadataView, error) {
	var view resourceMetadataView
	scoped := func(p ch.Params) string { return tableWhere(q, p, true) }

	// The custom_metadata contracts in scope, one row per distinct
	// contract, a blank one for studies that declare none.
	p := ch.Params{}
	contracts, err := QueryView[struct {
		CustomMetadata string `json:"customMetadata"`
		FirstStudy     string `json:"firstStudy"`
	}](s.CH, "SELECT customMetadata, firstStudy FROM (SELECT ifNull(rdef.custom_metadata, '') AS customMetadata, "+
		"min(rdata.study_id) AS firstStudy"+resourceTableFrom+" WHERE "+scoped(p)+" GROUP BY customMetadata) "+
		"ORDER BY firstStudy ASC", p)
	if err != nil {
		return view, err
	}
	var schemas []resourceMetadataSchema
	var undeclared []string
	coversEveryStudy := len(contracts) > 0
	for _, c := range contracts {
		if strings.TrimSpace(c.CustomMetadata) == "" {
			coversEveryStudy = false
			undeclared = append(undeclared, c.FirstStudy)
			continue
		}
		if sc := parseResourceMetadataSchema(c.CustomMetadata); len(sc.fields) > 0 {
			schemas = append(schemas, sc)
		}
	}
	schema := mergeResourceMetadataSchemas(schemas)
	if len(schemas) > 0 && len(undeclared) > 0 {
		slices.Sort(undeclared)
		slog.Warn("resource declares custom_metadata in only some of the selected studies; "+
			"showing the declared columns alongside the keys found in the data",
			slog.String("resource", *q.ResourceID), slog.Any("undeclared", undeclared))
	}
	if len(schemas) > 1 {
		slog.Warn("resource has differing custom_metadata contracts across the selected studies; showing the union of their fields",
			slog.String("resource", *q.ResourceID), slog.Int("contracts", len(schemas)),
			slog.Any("conflictingTypes", conflictingTypeKeys(schemas)))
	}
	declared := schema.byKey()

	// Key discovery reads a sample of the rows, unless every study declares
	// a contract that types every column.
	stats := map[string]resourceKeyStats{}
	var statKeys []string
	if !(coversEveryStudy && schema.fullyTyped()) {
		p := ch.Params{"sample": int64(keyDiscoveryRows)}
		rows, err := QueryView[resourceKeyStats](s.CH,
			"SELECT metaKey, countIf(rawValue != '') AS nonBlankCount, "+
				"countIf(rawValue != '' AND parsedValue IS NOT NULL) AS numericCount FROM ("+
				"SELECT kv.1 AS metaKey, JSONExtractString(kv.2) AS rawValue, toFloat64OrNull(rawValue) AS parsedValue FROM ("+
				"SELECT arrayJoin(JSONExtractKeysAndValuesRaw(metadataJson)) AS kv FROM ("+
				"SELECT ifNull(rdata.metadata, '{}') AS metadataJson"+resourceTableFrom+" WHERE "+scoped(p)+
				" AND rdata.metadata IS NOT NULL AND rdata.metadata != '' AND rdata.metadata != '{}' "+
				"LIMIT {sample:Int64}) sampled) pairs) vals GROUP BY metaKey ORDER BY metaKey ASC "+
				fmt.Sprintf("SETTINGS max_memory_usage = %d", keyDiscoveryMaxMemory), p)
		if err != nil {
			return view, err
		}
		for _, st := range rows {
			if _, ok := stats[st.MetaKey]; !ok {
				statKeys = append(statKeys, st.MetaKey)
			}
			stats[st.MetaKey] = st
		}
	}

	// The column keys: the contract's when every study declares it, the
	// data's when none does, and otherwise the contract's followed by the
	// data's other keys.
	var keys []string
	switch {
	case len(schema.fields) == 0:
		keys = slices.Clone(statKeys)
		slices.SortStableFunc(keys, javaCompareIgnoreCase)
	case coversEveryStudy:
		keys = schema.keys()
	default:
		keys = schema.keys()
		extra := slices.Clone(statKeys)
		slices.SortStableFunc(extra, javaCompareIgnoreCase)
		for _, k := range extra {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	numeric := func(k string) bool {
		if f, ok := declared[k]; ok && f.typ != nil {
			switch *f.typ {
			case "string":
				return false
			case "number":
				return true
			}
		}
		st, ok := stats[k]
		return ok && st.NonBlankCount > 0 && st.NumericCount == st.NonBlankCount
	}
	filterable := func(k string) bool {
		f, ok := declared[k]
		return !ok || f.filterable == nil || *f.filterable
	}

	var categorical, numericKeys []string
	for _, k := range keys {
		if !filterable(k) {
			continue
		}
		if numeric(k) {
			numericKeys = append(numericKeys, k)
		} else {
			categorical = append(categorical, k)
		}
	}
	pairs := func(p ch.Params) string {
		return "SELECT arrayJoin(JSONExtractKeysAndValuesRaw(ifNull(rdata.metadata, '{}'))) AS kv" + resourceTableFrom +
			" WHERE " + scoped(p) + " AND rdata.metadata IS NOT NULL AND rdata.metadata != '' AND rdata.metadata != '{}'"
	}
	if len(numericKeys) > 0 {
		p := ch.Params{"keys": numericKeys}
		rows, err := QueryView[struct {
			MetaKey  string   `json:"metaKey"`
			MinValue *float64 `json:"minValue"`
			MaxValue *float64 `json:"maxValue"`
		}](s.CH, "SELECT metaKey, min(parsedValue) AS minValue, max(parsedValue) AS maxValue FROM ("+
			"SELECT kv.1 AS metaKey, toFloat64OrNull(JSONExtractString(kv.2)) AS parsedValue FROM ("+pairs(p)+") pairs "+
			"WHERE kv.1 IN {keys:Array(String)}) vals WHERE parsedValue IS NOT NULL GROUP BY metaKey ORDER BY metaKey ASC", p)
		if err != nil {
			return view, err
		}
		for _, rg := range rows {
			if rg.MinValue != nil && rg.MaxValue != nil {
				view.ranges.put(metadataColumnPrefix+rg.MetaKey,
					ResourceNumericRange{Min: core.JavaDouble(*rg.MinValue), Max: core.JavaDouble(*rg.MaxValue)})
			}
		}
	}
	if len(categorical) > 0 {
		p := ch.Params{"keys": categorical, "perkey": int64(maxFacetValues + 1)}
		rows, err := QueryView[struct {
			MetaKey string `json:"metaKey"`
			Value   string `json:"value"`
			Count   uint64 `json:"count"`
		}](s.CH, "SELECT metaKey, value, count() AS count FROM (SELECT kv.1 AS metaKey, JSONExtractString(kv.2) AS value FROM ("+
			pairs(p)+") pairs) vals WHERE value != '' AND metaKey IN {keys:Array(String)} "+
			"GROUP BY metaKey, value ORDER BY metaKey ASC, value ASC LIMIT {perkey:Int64} BY metaKey", p)
		if err != nil {
			return view, err
		}
		byKey := map[string][]ResourceFacetOption{}
		for _, row := range rows {
			byKey[row.MetaKey] = append(byKey[row.MetaKey], ResourceFacetOption{Value: row.Value, Count: row.Count})
		}
		for _, k := range categorical {
			if vals := byKey[k]; len(vals) > 0 && len(vals) <= maxFacetValues {
				view.facets.put(metadataColumnPrefix+k, vals)
			}
		}
	}

	view.columns = []ResourceColumnInfo{}
	for _, k := range keys {
		col := ResourceColumnInfo{ID: metadataColumnPrefix + k, Label: k, Source: "metadata", DataType: "string",
			Filterable: filterable(k), Sortable: true}
		if numeric(k) {
			col.DataType = "number"
		}
		if f, ok := declared[k]; ok {
			if f.label != nil && strings.TrimSpace(*f.label) != "" {
				col.Label = *f.label
			}
			col.VisibleByDefault = f.visibleByDefault != nil && *f.visibleByDefault
			col.Description = f.description
		}
		view.columns = append(view.columns, col)
	}
	return view, nil
}

// resourceMetadataField is one field of a custom_metadata contract
// (ResourceMetadataField); nil is "not declared".
type resourceMetadataField struct {
	key                     string
	typ, label, description *string
	filterable              *bool
	visibleByDefault        *bool
}

// resourceMetadataSchema is a resource_definition.custom_metadata contract
// (ResourceMetadataSchema), e.g.
//
//	{"version": 1, "fields": [{"key": "stain", "type": "string", "label": "Stain"}]}
type resourceMetadataSchema struct {
	fields []resourceMetadataField
}

// parseResourceMetadataSchema reads a contract leniently: unknown members are
// ignored, a field without a key is skipped, and a document that is not an
// object with a fields array is no contract.
func parseResourceMetadataSchema(text string) resourceMetadataSchema {
	var root any
	if err := newNumberDecoder(text).Decode(&root); err != nil {
		return resourceMetadataSchema{}
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return resourceMetadataSchema{}
	}
	fields, ok := obj["fields"].([]any)
	if !ok {
		return resourceMetadataSchema{}
	}
	text1 := func(m map[string]any, k string) *string {
		if s, ok := m[k].(string); ok {
			return &s
		}
		return nil
	}
	bool1 := func(m map[string]any, k string) *bool {
		if b, ok := m[k].(bool); ok {
			return &b
		}
		return nil
	}
	var out resourceMetadataSchema
	for _, f := range fields {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		key := text1(m, "key")
		if key == nil || strings.TrimSpace(*key) == "" {
			continue
		}
		out.fields = append(out.fields, resourceMetadataField{
			key: *key, typ: text1(m, "type"), label: text1(m, "label"), description: text1(m, "description"),
			filterable: bool1(m, "filterable"), visibleByDefault: bool1(m, "visibleByDefault"),
		})
	}
	return out
}

func newNumberDecoder(text string) *json.Decoder {
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	return d
}

// mergeResourceMetadataSchemas unions the contracts of a cohort's studies:
// each key keeps its first declaration, except that a key two contracts type
// differently is left untyped.
func mergeResourceMetadataSchemas(schemas []resourceMetadataSchema) resourceMetadataSchema {
	switch len(schemas) {
	case 0:
		return resourceMetadataSchema{}
	case 1:
		return schemas[0]
	}
	var out resourceMetadataSchema
	at := map[string]int{}
	for _, sc := range schemas {
		for _, f := range sc.fields {
			i, seen := at[f.key]
			if !seen {
				at[f.key] = len(out.fields)
				out.fields = append(out.fields, f)
				continue
			}
			if first := out.fields[i]; first.typ != nil && f.typ != nil && *first.typ != *f.typ {
				out.fields[i].typ = nil
			}
		}
	}
	return out
}

// conflictingTypeKeys are the keys two contracts type differently.
func conflictingTypeKeys(schemas []resourceMetadataSchema) []string {
	typeOf := map[string]string{}
	var out []string
	for _, sc := range schemas {
		for _, f := range sc.fields {
			if f.typ == nil {
				continue
			}
			seen, ok := typeOf[f.key]
			if !ok {
				typeOf[f.key] = *f.typ
			} else if seen != *f.typ && !slices.Contains(out, f.key) {
				out = append(out, f.key)
			}
		}
	}
	return out
}

// byKey is the declared fields by key, the first declaration of a key
// winning.
func (sc resourceMetadataSchema) byKey() map[string]resourceMetadataField {
	out := map[string]resourceMetadataField{}
	for _, f := range sc.fields {
		if _, ok := out[f.key]; !ok {
			out[f.key] = f
		}
	}
	return out
}

// keys is the declared keys in declaration order.
func (sc resourceMetadataSchema) keys() []string {
	var out []string
	for _, f := range sc.fields {
		if !slices.Contains(out, f.key) {
			out = append(out, f.key)
		}
	}
	return out
}

// fullyTyped: the contract declares columns and types every one.
func (sc resourceMetadataSchema) fullyTyped() bool {
	if len(sc.fields) == 0 {
		return false
	}
	for _, f := range sc.byKey() {
		if f.typ == nil {
			return false
		}
	}
	return true
}

// javaCompareIgnoreCase orders strings as String.CASE_INSENSITIVE_ORDER.
func javaCompareIgnoreCase(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		x, y := ra[i], rb[i]
		if x == y {
			continue
		}
		x, y = unicode.ToUpper(x), unicode.ToUpper(y)
		if x == y {
			continue
		}
		x, y = unicode.ToLower(x), unicode.ToLower(y)
		if x != y {
			return int(x) - int(y)
		}
	}
	return len(ra) - len(rb)
}

// metadataJSON is a row's metadata as Jackson reads it into a
// LinkedHashMap and writes it back: {} for none, for text that is not a
// JSON object or that does not parse; nil (left out) for a JSON null.
// Members keep their order, a repeated key its first position and last
// value; an integer is written as its value and a decimal as Double.toString
// writes it.
func metadataJSON(text *string) json.RawMessage {
	empty := json.RawMessage("{}")
	if text == nil {
		return empty
	}
	t := strings.TrimFunc(*text, func(r rune) bool { return r <= ' ' })
	if t == "" {
		return empty
	}
	d := newNumberDecoder(t)
	tok, err := d.Token()
	if err != nil {
		return empty
	}
	if tok == nil {
		return nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return empty
	}
	var buf bytes.Buffer
	if err := writeJacksonObject(d, &buf); err != nil {
		return empty
	}
	return buf.Bytes()
}

// writeJacksonObject writes the rest of an object whose '{' d has read.
func writeJacksonObject(d *json.Decoder, out *bytes.Buffer) error {
	var m orderedJSON[json.RawMessage]
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("object key %v", tok)
		}
		var v bytes.Buffer
		if err := writeJacksonValue(d, &v); err != nil {
			return err
		}
		m.put(key, v.Bytes())
	}
	if _, err := d.Token(); err != nil { // '}'
		return err
	}
	b, err := m.MarshalJSON()
	if err != nil {
		return err
	}
	out.Write(b)
	return nil
}

// writeJacksonValue writes the next value of d.
func writeJacksonValue(d *json.Decoder, out *bytes.Buffer) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		if v == '{' {
			return writeJacksonObject(d, out)
		}
		out.WriteByte('[')
		for first := true; d.More(); first = false {
			if !first {
				out.WriteByte(',')
			}
			if err := writeJacksonValue(d, out); err != nil {
				return err
			}
		}
		if _, err := d.Token(); err != nil { // ']'
			return err
		}
		out.WriteByte(']')
	case json.Number:
		out.WriteString(jacksonNumber(string(v)))
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out.Write(b)
	}
	return nil
}

// jacksonNumber is a JSON number as Jackson writes it back after reading it
// untyped: an integer (Integer, Long or BigInteger) as its value, a decimal
// (Double) as Double.toString writes it, infinity as a quoted string.
func jacksonNumber(n string) string {
	if !strings.ContainsAny(n, ".eE") {
		if i, ok := new(big.Int).SetString(n, 10); ok {
			return i.String()
		}
	}
	f, err := strconv.ParseFloat(n, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return n
	}
	switch {
	case math.IsInf(f, 1):
		return `"Infinity"`
	case math.IsInf(f, -1):
		return `"-Infinity"`
	}
	b, _ := core.JavaDouble(f).MarshalJSON()
	return string(b)
}
