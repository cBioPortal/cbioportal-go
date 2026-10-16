package studies

import (
	"slices"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

func ptr[T any](v T) *T { return &v }

// A row's metadata is written as Jackson reads it into a LinkedHashMap and
// writes it back.
func TestResourceMetadataJSON(t *testing.T) {
	for _, c := range []struct {
		in   *string
		want string
	}{
		{nil, `{}`},
		{ptr("  "), `{}`},
		{ptr("not json"), `{}`},
		{ptr(`["a"]`), `{}`},
		{ptr(`"text"`), `{}`},
		{ptr(`null`), ``},
		{ptr(`{"stain": "HE", "n": 1, "x": 1.50, "e": 1e2, "big": 123456789012345678901234567890, "neg": -0}`),
			`{"stain":"HE","n":1,"x":1.5,"e":100.0,"big":123456789012345678901234567890,"neg":0}`},
		{ptr(`{"a": 1, "b": 2, "a": 3}`), `{"a":3,"b":2}`},
		{ptr(`{"nested": {"z": 1, "y": [1.0, true, null, "s"]}} trailing`), `{"nested":{"z":1,"y":[1.0,true,null,"s"]}}`},
		{ptr(`{"huge": 1e400}`), `{"huge":"Infinity"}`},
		{ptr(`{"broken": `), `{}`},
	} {
		if got := string(metadataJSON(c.in)); got != c.want {
			t.Errorf("metadataJSON(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

// A contract is read leniently; contracts of several studies are unioned,
// a key typed differently by two of them left untyped.
func TestResourceMetadataSchema(t *testing.T) {
	a := parseResourceMetadataSchema(`{"version": 1, "fields": [` +
		`{"key": "stain", "type": "string", "label": "Stain", "visibleByDefault": true},` +
		`{"label": "no key"}, "not an object", {"key": " "},` +
		`{"key": "n", "type": "number", "filterable": false, "extra": 1}]}`)
	if got := a.keys(); !slices.Equal(got, []string{"stain", "n"}) {
		t.Fatalf("keys %v", got)
	}
	if f := a.byKey()["n"]; f.filterable == nil || *f.filterable || *f.typ != "number" {
		t.Errorf("field n %+v", f)
	}
	if !a.fullyTyped() {
		t.Error("contract with every field typed is fully typed")
	}
	for _, bad := range []string{``, `[]`, `{"fields": {}}`, `{"fields": [`, `null`} {
		if sc := parseResourceMetadataSchema(bad); len(sc.fields) != 0 {
			t.Errorf("%q parsed as %+v", bad, sc)
		}
	}
	b := parseResourceMetadataSchema(`{"fields": [{"key": "n", "type": "string", "label": "Other"}, {"key": "mag"}]}`)
	m := mergeResourceMetadataSchemas([]resourceMetadataSchema{a, b})
	if got := m.keys(); !slices.Equal(got, []string{"stain", "n", "mag"}) {
		t.Fatalf("merged keys %v", got)
	}
	if n := m.byKey()["n"]; n.typ != nil || n.label != nil || n.filterable == nil {
		t.Errorf("merged n %+v", n)
	}
	if m.fullyTyped() {
		t.Error("merged contract with an untyped key is not fully typed")
	}
	if got := conflictingTypeKeys([]resourceMetadataSchema{a, b}); !slices.Equal(got, []string{"n"}) {
		t.Errorf("conflicts %v", got)
	}
}

func TestJavaCompareIgnoreCase(t *testing.T) {
	keys := []string{"b", "A", "a", "_x", "B10", "b2"}
	slices.SortStableFunc(keys, javaCompareIgnoreCase)
	if want := []string{"_x", "A", "a", "b", "B10", "b2"}; !slices.Equal(keys, want) {
		t.Errorf("got %v, want %v", keys, want)
	}
}

// The cohort matches rows at their own level, and with several studies
// matches the ids with their study.
func TestResourceCohortWhere(t *testing.T) {
	q := &resourceTableQuery{
		StudyIDs:           []string{"s1"},
		PatientIdentifiers: []*resourceIdentifier{{StudyID: ptr("s1"), PatientID: ptr("P1")}, nil, {StudyID: ptr("s1")}},
		SampleIdentifiers:  []*resourceIdentifier{{StudyID: ptr("s1"), SampleID: ptr("S1")}},
	}
	p := ch.Params{}
	where := strings.Join(cohortWhere(q, p), " AND ")
	if !strings.Contains(where, "(rdata.sample_id IS NOT NULL AND rdata.sample_id IN {sids:Array(String)}) OR (rdata.sample_id IS NULL AND rdata.patient_id IN {pids:Array(String)})") ||
		strings.Contains(where, "concat") {
		t.Errorf("single study: %s", where)
	}
	if !slices.Equal(p["pids"].([]string), []string{"P1"}) {
		t.Errorf("pids %v", p["pids"])
	}
	q.StudyIDs = []string{"s1", "s2"}
	q.SampleIdentifiers = nil
	p = ch.Params{}
	where = strings.Join(cohortWhere(q, p), " AND ")
	if !strings.Contains(where, "concat(rdata.study_id, '_', rdata.patient_id) IN {ptuples:Array(String)}") || strings.Contains(where, "sample_id") {
		t.Errorf("multi study: %s", where)
	}
	if !slices.Equal(p["ptuples"].([]string), []string{"s1_P1", "s1_null"}) {
		t.Errorf("tuples %v", p["ptuples"])
	}
}

// Column filters follow ResourceDataMapper's operator templates.
func TestResourceColumnFilterSQL(t *testing.T) {
	for _, c := range []struct {
		f    resourceColumnFilter
		want string
	}{
		{resourceColumnFilter{ptr("patientId"), ptr("in"), nil}, "1 = 0"},
		{resourceColumnFilter{ptr("sampleId"), ptr("contains"), []string{"x"}}, "rdata.sample_id LIKE concat('%', {f0v0:String}, '%')"},
		{resourceColumnFilter{ptr("url"), ptr("equalsAny"), []string{"a", "b"}}, "rdata.url IN ({f0v0:String}, {f0v1:String})"},
		{resourceColumnFilter{ptr("displayName"), ptr("notEquals"), []string{"a"}}, "rdata.display_name != {f0v0:String}"},
		{resourceColumnFilter{ptr("patientId"), ptr("notIn"), []string{"a"}}, "rdata.patient_id NOT IN ({f0v0:String})"},
		{resourceColumnFilter{ptr("patientId"), ptr("between"), []string{"1", "2"}}, "rdata.patient_id = {f0v0:String}"},
		{resourceColumnFilter{ptr("metadata:n"), ptr("between"), []string{"1", "2"}},
			"toFloat64OrNull(JSONExtractString(ifNull(rdata.metadata, '{}'), {f0k:String})) BETWEEN toFloat64({f0v0:String}) AND toFloat64({f0v1:String})"},
		{resourceColumnFilter{ptr("metadata:n"), ptr("whatever"), nil}, "1 = 1"},
		{resourceColumnFilter{ptr("resourceType"), ptr("in"), nil}, "1 = 1"},
	} {
		if got := columnFilterSQL(&c.f, 0, ch.Params{}); got != c.want {
			t.Errorf("%s %s: got %s, want %s", *c.f.ColumnID, *c.f.Operator, got, c.want)
		}
	}
}
