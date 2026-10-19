package core

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// Query scopes: the key-aware way to read the study-partitioned tables.
//
// The big tables are sorted by keys that lead with the study (sample_list,
// sample, patient, the raw matrices, the study-view *_derived tables), so a
// read that filters only on a list, profile or sample id scans the whole
// table. A scope names the rows a read is held to — studies, sample lists,
// molecular profiles, samples or patients — and renders the predicate over a
// table's key columns, the study first. Handlers name these tables only
// through a scope (scope_test.go enforces it):
//
//	sc := s.BySampleList(id)
//	"SELECT sample_id FROM " + sc.From(args, TSampleList) + " ORDER BY sample_id"
//	"... JOIN " + sc.Samples(args) + " s ON s.study_id = m.study_id AND ..."
//
// Values are bound as query parameters through a Binder: a *SQLArgs, or a
// ch.Params map wrapped as ParamsBinder.

// KeyedTable is a table partitioned by study, with the columns a scope can
// restrict. key lists the columns leading its sort key: the go transform's
// for the model tables, production's (which the mirror reads) for the
// *_derived tables, which the go transform leaves unsorted.
type KeyedTable struct {
	Name    string
	key     []string
	Study   string // the study id column
	list    string // the sample list id column
	profile string // the molecular profile id column
	sample  string // the sample id column (sample ids within the study)
	patient string // the patient id column (patient ids within the study)
}

// The keyed tables, with the columns their scopes filter on.
var (
	TSampleList = KeyedTable{Name: "sample_list", key: []string{"study_id", "list_id", "sample_id"},
		Study: "study_id", list: "list_id", sample: "sample_id"}
	TSample = KeyedTable{Name: "sample", key: []string{"study_id", "sample_id"},
		Study: "study_id", sample: "sample_id", patient: "patient_id"}
	TPatient = KeyedTable{Name: "patient", key: []string{"study_id", "patient_id"},
		Study: "study_id", patient: "patient_id"}

	TSampleDerived = KeyedTable{Name: "sample_derived", key: []string{"cancer_study_identifier", "sample_unique_id"},
		Study: "cancer_study_identifier", sample: "sample_stable_id"}
	TClinicalDataDerived = KeyedTable{Name: "clinical_data_derived",
		key: []string{"cancer_study_identifier", "type", "attribute_name", "sample_unique_id"}, Study: "cancer_study_identifier"}
	TClinicalEventDataDerived = KeyedTable{Name: "clinical_event_data_derived",
		key: []string{"cancer_study_identifier", "event_type", "patient_unique_id"}, Study: "cancer_study_identifier"}
	TGeneticAlterationDerived = KeyedTable{Name: "genetic_alteration_derived",
		key: []string{"cancer_study_identifier", "hugo_gene_symbol", "profile_type", "sample_unique_id"}, Study: "cancer_study_identifier"}
	// genomic_event_derived leads with the profile, then the study.
	TGenomicEventDerived = KeyedTable{Name: "genomic_event_derived",
		key:   []string{"genetic_profile_stable_id", "cancer_study_identifier", "variant_type"},
		Study: "cancer_study_identifier", profile: "genetic_profile_stable_id"}
	// sample_to_gene_panel_derived is sorted by panel and profile; it is
	// small and read by study or profile.
	TSampleToGenePanelDerived = KeyedTable{Name: "sample_to_gene_panel_derived",
		key:   []string{"gene_panel_id", "alteration_type", "genetic_profile_id"},
		Study: "cancer_study_identifier", profile: "genetic_profile_id"}
	// generic_assay_data_derived has no study column: reads name the profile
	// type and entity its key leads with.
	TGenericAssayDataDerived = KeyedTable{Name: "generic_assay_data_derived",
		key: []string{"profile_type", "entity_stable_id"}}

	// Clinical and profile tables a scope's predicates also apply to.
	TClinicalSample = KeyedTable{Name: "clinical_sample", key: []string{"study_id", "sample_id", "attr_id"},
		Study: "study_id", sample: "sample_id"}
	TClinicalPatient = KeyedTable{Name: "clinical_patient", key: []string{"study_id", "patient_id", "attr_id"},
		Study: "study_id", patient: "patient_id"}
	TSampleProfile = KeyedTable{Name: "sample_profile", key: []string{"study_id", "profile_stable_id", "sample_id"},
		Study: "study_id", profile: "molecular_profile_id", sample: "sample_id"}
	TSeg               = KeyedTable{Name: "seg", key: []string{"study_id", "sample_id"}, Study: "study_id", sample: "sample_id"}
	TStructuralVariant = KeyedTable{Name: "structural_variant", key: []string{"study_id", "profile_stable_id", "sample_id"},
		Study: "study_id", sample: "sample_id"}
)

// keyedTables are the tables only a scope may name in internal/api SQL
// (scope_test.go), with the raw matrices of the staging database.
var keyedTables = []KeyedTable{
	TSampleList, TSample, TPatient,
	TSampleDerived, TClinicalDataDerived, TClinicalEventDataDerived, TGeneticAlterationDerived,
	TGenomicEventDerived, TSampleToGenePanelDerived, TGenericAssayDataDerived,
	RawMatrix("cna_matrix"), RawMatrix("mrna_expression_matrix"), RawMatrix("methylation_matrix"),
	RawMatrix("protein_level_matrix"), RawMatrix("geneset_score_matrix"), RawMatrix("generic_assay_matrix"),
	RawMatrix("cna_discrete_long"),
}

// RawMatrix is a staged per-profile matrix, sorted by (study_id, source_file).
func RawMatrix(name string) KeyedTable {
	return KeyedTable{Name: "cbioportal_raw." + name, key: []string{"study_id", "source_file"},
		Study: "study_id", sample: "sample_id"}
}

// keyValues restricts one key column: to vals when set (none when empty),
// rendered `col = v` when built from one value and `col IN [...]` otherwise.
type keyValues struct {
	vals []string
	set  bool
	one  bool
}

func oneOf(v string) keyValues { return keyValues{vals: []string{v}, set: true, one: true} }

func anyOf(vals []string) keyValues {
	if vals == nil {
		vals = []string{}
	}
	return keyValues{vals: vals, set: true}
}

func (k keyValues) pred(b Binder, col string) string {
	if k.one {
		return col + " = " + b.Str(k.vals[0])
	}
	return col + " IN " + b.Strs(k.vals)
}

// Scope is the set of rows a read is held to. Each dimension restricts the
// matching key column of the table read; a dimension the table does not
// carry is left to the caller's own predicates. A scope without studies
// (unkeyed) arises only when a list's studies cannot be resolved.
type Scope struct {
	study, list, profile, sample, patient keyValues
}

// AcrossStudies reads every study: for the endpoints that list them all.
var AcrossStudies = Scope{}

// ByStudy holds a read to one study.
func ByStudy(id string) Scope { return Scope{study: oneOf(id)} }

// ByStudies holds a read to the given studies (none when empty).
func ByStudies(ids []string) Scope { return Scope{study: anyOf(ids)} }

// BySamples holds a read to samples of one study.
func BySamples(studyID string, sampleIDs []string) Scope {
	return Scope{study: oneOf(studyID), sample: anyOf(sampleIDs)}
}

// BySample holds a read to one sample.
func BySample(studyID, sampleID string) Scope {
	return Scope{study: oneOf(studyID), sample: oneOf(sampleID)}
}

// ByPatient holds a read to one patient.
func ByPatient(studyID, patientID string) Scope {
	return Scope{study: oneOf(studyID), patient: oneOf(patientID)}
}

// PairStudies returns the sorted distinct studies of (study, id) pairs, for
// a scope over them.
func PairStudies(pairs [][2]string) []string {
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p[0]
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// IdentifiersCond selects identified samples over t's study and sample key
// columns (qualified by alias if any): one (study = s AND sample IN [...])
// term per study, in study order; false when there are none.
func IdentifiersCond(b Binder, t KeyedTable, alias string, ids []Identifier) string {
	if len(ids) == 0 {
		return FalseClause
	}
	byStudy := map[string][]string{}
	for _, id := range ids {
		byStudy[id.StudyID] = append(byStudy[id.StudyID], id.ID())
	}
	terms := make([]string, 0, len(byStudy))
	for _, st := range SortedKeys(byStudy) {
		terms = append(terms, "("+BySamples(st, byStudy[st]).Cond(b, t, alias)+")")
	}
	return "(" + strings.Join(terms, " OR ") + ")"
}

// IdentifiersFrom is "<t> WHERE <identifiersCond>", a FROM source of the
// identified samples' rows.
func IdentifiersFrom(b Binder, t KeyedTable, ids []Identifier) string {
	return t.Name + " WHERE " + IdentifiersCond(b, t, "", ids)
}

// IdentifierStudies returns the sorted distinct studies of identifiers.
func IdentifierStudies(ids []Identifier) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.StudyID
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ByProfiles holds a read to molecular profiles, by their ids alone: for
// the tables keyed by profile across studies (genomic_event_derived,
// sample_to_gene_panel_derived).
func ByProfiles(ids []string) Scope { return Scope{profile: anyOf(ids)} }

// BySampleList holds a read to one sample list, within the studies whose
// lists carry its id (from the cached list index).
func (s *State) BySampleList(id string) Scope {
	sc := s.BySampleLists(id)
	sc.list = oneOf(id)
	return sc
}

// BySampleListIn holds a read to the members of one sample list that are
// samples of studyID: none when the list belongs to another study. Java
// joins such a list to a profile's rows by sample internal id, so a list of
// another study selects nothing even where its sample ids are the same.
func (s *State) BySampleListIn(id, studyID string) Scope {
	return Scope{study: oneOf(studyID), list: oneOf(id)}
}

// BySampleLists holds a read to the given sample lists, within their
// studies.
func (s *State) BySampleLists(ids ...string) Scope {
	sc := Scope{list: anyOf(ids)}
	if studies, ok := s.sampleListsStudies(ids); ok {
		sc.study = anyOf(studies)
	}
	return sc
}

// Cond is the scope's predicate over t's key columns, qualified by alias if
// any; empty when the scope restricts none of them.
func (sc Scope) Cond(b Binder, t KeyedTable, alias string) string {
	var preds []string
	for _, d := range []struct {
		col string
		k   keyValues
	}{{t.Study, sc.study}, {t.list, sc.list}, {t.profile, sc.profile}, {t.sample, sc.sample}, {t.patient, sc.patient}} {
		if !d.k.set || d.col == "" {
			continue
		}
		col := d.col
		if alias != "" {
			col = alias + "." + col
		}
		preds = append(preds, d.k.pred(b, col))
	}
	if sc.study.set && t.Study == "" {
		panic(fmt.Sprintf("scope: %s has no study column", t.Name))
	}
	return strings.Join(preds, " AND ")
}

// Key is Cond followed by " AND ", to lead a WHERE clause; empty when the
// scope restricts none of t's key columns.
func (sc Scope) Key(b Binder, t KeyedTable, alias string) string {
	if c := sc.Cond(b, t, alias); c != "" {
		return c + " AND "
	}
	return ""
}

// From is "<t> WHERE <cond>", a FROM source whose WHERE clause the caller
// may extend with " AND ...".
func (sc Scope) From(b Binder, t KeyedTable) string {
	c := sc.Cond(b, t, "")
	if c == "" {
		c = "1"
	}
	return t.Name + " WHERE " + c
}

// Rows is a subquery of t's rows in the scope, for a JOIN or an aliased
// FROM.
func (sc Scope) Rows(b Binder, t KeyedTable) string {
	c := sc.Cond(b, t, "")
	if c == "" {
		return t.Name
	}
	return "(SELECT * FROM " + t.Name + " WHERE " + c + ")"
}

// Samples is a subquery of the sample rows of the scope's studies (and of
// its samples or patients, if it names them).
func (sc Scope) Samples(b Binder) string { return sc.Cases().Rows(b, TSample) }

// Cases is the scope's study, sample and patient restrictions, those every
// sample-keyed table carries. A read joining a scoped subquery restricts it
// by all of them: a filter the outer query puts on the join key does not
// reach into the subquery.
func (sc Scope) Cases() Scope { return Scope{study: sc.study, sample: sc.sample, patient: sc.patient} }

// Patients is a subquery of the patient rows of the scope's studies (and of
// its patients, if it names them).
func (sc Scope) Patients(b Binder) string {
	return Scope{study: sc.study, patient: sc.patient}.Rows(b, TPatient)
}

// ListMembers is a subquery of the sample ids of the scope's sample lists.
func (sc Scope) ListMembers(b Binder) string {
	return "(SELECT sample_id FROM " + sc.From(b, TSampleList) + ")"
}

// Binder binds request values as query parameters, returning the
// placeholder to splice into the SQL.
type Binder interface {
	Str(string) string
	Strs([]string) string
}

// ParamsBinder binds into a ch.Params map, under names k0, k1, ... not
// already taken.
type ParamsBinder ch.Params

func (p ParamsBinder) bind(typ string, v any) string {
	for i := 0; ; i++ {
		name := fmt.Sprintf("k%d", i)
		if _, taken := p[name]; !taken {
			p[name] = v
			return "{" + name + ":" + typ + "}"
		}
	}
}

// Str binds a String parameter and returns its placeholder.
func (p ParamsBinder) Str(v string) string { return p.bind("String", v) }

// Strs binds an Array(String) parameter and returns its placeholder.
func (p ParamsBinder) Strs(v []string) string {
	if v == nil {
		v = []string{}
	}
	return p.bind("Array(String)", v)
}

// sampleListsStudies returns the sorted studies whose sample_list rows carry
// any of the list ids; ok is false when the list index cannot be read.
func (s *State) sampleListsStudies(listIDs []string) (studies []string, ok bool) {
	for _, id := range listIDs {
		st, ok := s.sampleListStudies(id)
		if !ok {
			return nil, false
		}
		studies = append(studies, st...)
	}
	slices.Sort(studies)
	return slices.Compact(studies), true
}

// sampleListStudies returns the studies whose sample_list rows carry a list
// id (normally one), from a cached index of all lists; ok is false when the
// index could not be read.
func (s *State) sampleListStudies(listID string) (studies []string, ok bool) {
	type listStudy struct {
		ListID  string `json:"listId"`
		StudyID string `json:"studyId"`
	}
	rows, err := CachedQuery[listStudy](s, sampleListIndexSQL)
	if err != nil {
		return nil, false
	}
	i, _ := slices.BinarySearchFunc(rows, listID, func(r listStudy, id string) int { return strings.Compare(r.ListID, id) })
	for ; i < len(rows) && rows[i].ListID == listID; i++ {
		studies = append(studies, rows[i].StudyID)
	}
	return studies, true
}

// sampleListIndexSQL reads every list's studies: the one read of sample_list
// across studies.
const sampleListIndexSQL = "SELECT DISTINCT list_id AS listId, study_id AS studyId FROM sample_list ORDER BY listId, studyId"

// Identifier is an element of a fetch body's identifier list: a study and a
// sample, patient or entity id.
type Identifier struct {
	StudyID  string `json:"studyId"`
	SampleID string `json:"sampleId"`
	// patient fetches use patientId; entity fetches use entityId.
	PatientID string `json:"patientId"`
	EntityID  string `json:"entityId"`
}

// ID returns the identifier's sample, patient or entity id, whichever is set.
func (i Identifier) ID() string {
	for _, v := range []string{i.SampleID, i.PatientID, i.EntityID} {
		if v != "" {
			return v
		}
	}
	return ""
}

// SortedKeys returns the map's keys in order; never nil, so the keys of an
// empty map encode as [].
func SortedKeys[V any](m map[string]V) []string {
	out := slices.AppendSeq(make([]string, 0, len(m)), maps.Keys(m))
	slices.Sort(out)
	return out
}
