package api

import (
	"fmt"
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
//	sc := s.bySampleList(id)
//	"SELECT sample_id FROM " + sc.from(args, tSampleList) + " ORDER BY sample_id"
//	"... JOIN " + sc.samples(args) + " s ON s.study_id = m.study_id AND ..."
//
// Values are bound as query parameters through a binder: a *sqlArgs, or a
// ch.Params map wrapped as paramsBinder.

// keyedTable is a table partitioned by study, with the columns a scope can
// restrict. key lists the columns leading its sort key: the go transform's
// for the model tables, production's (which the mirror reads) for the
// *_derived tables, which the go transform leaves unsorted.
type keyedTable struct {
	name    string
	key     []string
	study   string // the study id column
	list    string // the sample list id column
	profile string // the molecular profile id column
	sample  string // the sample id column (sample ids within the study)
	patient string // the patient id column (patient ids within the study)
}

var (
	tSampleList = keyedTable{name: "sample_list", key: []string{"study_id", "list_id", "sample_id"},
		study: "study_id", list: "list_id", sample: "sample_id"}
	tSample = keyedTable{name: "sample", key: []string{"study_id", "sample_id"},
		study: "study_id", sample: "sample_id", patient: "patient_id"}
	tPatient = keyedTable{name: "patient", key: []string{"study_id", "patient_id"},
		study: "study_id", patient: "patient_id"}

	tSampleDerived = keyedTable{name: "sample_derived",
		key: []string{"cancer_study_identifier", "sample_unique_id"}, study: "cancer_study_identifier"}
	tClinicalDataDerived = keyedTable{name: "clinical_data_derived",
		key: []string{"cancer_study_identifier", "type", "attribute_name", "sample_unique_id"}, study: "cancer_study_identifier"}
	tClinicalEventDataDerived = keyedTable{name: "clinical_event_data_derived",
		key: []string{"cancer_study_identifier", "event_type", "patient_unique_id"}, study: "cancer_study_identifier"}
	tGeneticAlterationDerived = keyedTable{name: "genetic_alteration_derived",
		key: []string{"cancer_study_identifier", "hugo_gene_symbol", "profile_type", "sample_unique_id"}, study: "cancer_study_identifier"}
	// genomic_event_derived leads with the profile, then the study.
	tGenomicEventDerived = keyedTable{name: "genomic_event_derived",
		key:   []string{"genetic_profile_stable_id", "cancer_study_identifier", "variant_type"},
		study: "cancer_study_identifier", profile: "genetic_profile_stable_id"}
	// sample_to_gene_panel_derived is sorted by panel and profile; it is
	// small and read by study or profile.
	tSampleToGenePanelDerived = keyedTable{name: "sample_to_gene_panel_derived",
		key:   []string{"gene_panel_id", "alteration_type", "genetic_profile_id"},
		study: "cancer_study_identifier", profile: "genetic_profile_id"}
	// generic_assay_data_derived has no study column: reads name the profile
	// type and entity its key leads with.
	tGenericAssayDataDerived = keyedTable{name: "generic_assay_data_derived",
		key: []string{"profile_type", "entity_stable_id"}}

	// Clinical and profile tables a scope's predicates also apply to.
	tClinicalSample = keyedTable{name: "clinical_sample", key: []string{"study_id", "sample_id", "attr_id"},
		study: "study_id", sample: "sample_id"}
	tClinicalPatient = keyedTable{name: "clinical_patient", key: []string{"study_id", "patient_id", "attr_id"},
		study: "study_id", patient: "patient_id"}
	tSampleProfile = keyedTable{name: "sample_profile", key: []string{"study_id", "profile_stable_id", "sample_id"},
		study: "study_id", profile: "molecular_profile_id", sample: "sample_id"}
)

// keyedTables are the tables only a scope may name in internal/api SQL
// (scope_test.go), with the raw matrices of the staging database.
var keyedTables = []keyedTable{
	tSampleList, tSample, tPatient,
	tSampleDerived, tClinicalDataDerived, tClinicalEventDataDerived, tGeneticAlterationDerived,
	tGenomicEventDerived, tSampleToGenePanelDerived, tGenericAssayDataDerived,
	rawMatrix("cna_matrix"), rawMatrix("mrna_expression_matrix"), rawMatrix("methylation_matrix"),
	rawMatrix("protein_level_matrix"), rawMatrix("geneset_score_matrix"), rawMatrix("generic_assay_matrix"),
	rawMatrix("cna_discrete_long"),
}

// rawMatrix is a staged per-profile matrix, sorted by (study_id, source_file).
func rawMatrix(name string) keyedTable {
	return keyedTable{name: "cbioportal_raw." + name, key: []string{"study_id", "source_file"},
		study: "study_id", sample: "sample_id"}
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

func (k keyValues) pred(b binder, col string) string {
	if k.one {
		return col + " = " + b.str(k.vals[0])
	}
	return col + " IN " + b.strs(k.vals)
}

// scope is the set of rows a read is held to. Each dimension restricts the
// matching key column of the table read; a dimension the table does not
// carry is left to the caller's own predicates. A scope without studies
// (unkeyed) arises only when a list's studies cannot be resolved.
type scope struct {
	study, list, profile, sample, patient keyValues
}

// acrossStudies reads every study: for the endpoints that list them all.
var acrossStudies = scope{}

// byStudy holds a read to one study.
func byStudy(id string) scope { return scope{study: oneOf(id)} }

// byStudies holds a read to the given studies (none when empty).
func byStudies(ids []string) scope { return scope{study: anyOf(ids)} }

// bySamples holds a read to samples of one study.
func bySamples(studyID string, sampleIDs []string) scope {
	return scope{study: oneOf(studyID), sample: anyOf(sampleIDs)}
}

// bySample holds a read to one sample.
func bySample(studyID, sampleID string) scope {
	return scope{study: oneOf(studyID), sample: oneOf(sampleID)}
}

// byPatient holds a read to one patient.
func byPatient(studyID, patientID string) scope {
	return scope{study: oneOf(studyID), patient: oneOf(patientID)}
}

// byProfiles holds a read to molecular profiles, by their ids alone: for
// the tables keyed by profile across studies (genomic_event_derived,
// sample_to_gene_panel_derived). inStudies adds the studies when known.
func byProfiles(ids []string) scope { return scope{profile: anyOf(ids)} }

// byProfile holds a read to one molecular profile within its study, false
// when the profile does not exist.
func (s *State) byProfile(id string) (scope, profileMeta, bool) {
	meta, ok := s.profileMeta(id)
	if !ok {
		return scope{}, meta, false
	}
	return scope{study: oneOf(meta.StudyID), profile: oneOf(id)}, meta, true
}

// inStudies restricts the scope to studies as well.
func (sc scope) inStudies(ids []string) scope {
	sc.study = anyOf(ids)
	return sc
}

// bySampleList holds a read to one sample list, within the studies whose
// lists carry its id (from the cached list index).
func (s *State) bySampleList(id string) scope {
	sc := s.bySampleLists(id)
	sc.list = oneOf(id)
	return sc
}

// bySampleLists holds a read to the given sample lists, within their
// studies.
func (s *State) bySampleLists(ids ...string) scope {
	sc := scope{list: anyOf(ids)}
	if studies, ok := s.sampleListsStudies(ids); ok {
		sc.study = anyOf(studies)
	}
	return sc
}

// studiesOnly is the scope's study restriction alone, for the tables a read
// joins.
func (sc scope) studiesOnly() scope { return scope{study: sc.study} }

// cond is the scope's predicate over t's key columns, qualified by alias if
// any; empty when the scope restricts none of them.
func (sc scope) cond(b binder, t keyedTable, alias string) string {
	var preds []string
	for _, d := range []struct {
		col string
		k   keyValues
	}{{t.study, sc.study}, {t.list, sc.list}, {t.profile, sc.profile}, {t.sample, sc.sample}, {t.patient, sc.patient}} {
		if !d.k.set || d.col == "" {
			continue
		}
		col := d.col
		if alias != "" {
			col = alias + "." + col
		}
		preds = append(preds, d.k.pred(b, col))
	}
	if sc.study.set && t.study == "" {
		panic(fmt.Sprintf("scope: %s has no study column", t.name))
	}
	return strings.Join(preds, " AND ")
}

// key is cond followed by " AND ", to lead a WHERE clause; empty when the
// scope restricts none of t's key columns.
func (sc scope) key(b binder, t keyedTable, alias string) string {
	if c := sc.cond(b, t, alias); c != "" {
		return c + " AND "
	}
	return ""
}

// from is "<t> WHERE <cond>", a FROM source whose WHERE clause the caller
// may extend with " AND ...".
func (sc scope) from(b binder, t keyedTable) string {
	c := sc.cond(b, t, "")
	if c == "" {
		c = "1"
	}
	return t.name + " WHERE " + c
}

// rows is a subquery of t's rows in the scope, for a JOIN or an aliased
// FROM.
func (sc scope) rows(b binder, t keyedTable) string {
	c := sc.cond(b, t, "")
	if c == "" {
		return t.name
	}
	return "(SELECT * FROM " + t.name + " WHERE " + c + ")"
}

// samples is a subquery of the sample rows of the scope's studies (and of
// its samples or patients, if it names them).
func (sc scope) samples(b binder) string {
	return scope{study: sc.study, sample: sc.sample, patient: sc.patient}.rows(b, tSample)
}

// patients is a subquery of the patient rows of the scope's studies (and of
// its patients, if it names them).
func (sc scope) patients(b binder) string {
	return scope{study: sc.study, patient: sc.patient}.rows(b, tPatient)
}

// listMembers is a subquery of the sample ids of the scope's sample lists.
func (sc scope) listMembers(b binder) string {
	return "(SELECT sample_id FROM " + sc.from(b, tSampleList) + ")"
}

// binder binds request values as query parameters, returning the
// placeholder to splice into the SQL.
type binder interface {
	str(string) string
	strs([]string) string
}

// paramsBinder binds into a ch.Params map, under names k0, k1, ... not
// already taken.
type paramsBinder ch.Params

func (p paramsBinder) bind(typ string, v any) string {
	for i := 0; ; i++ {
		name := fmt.Sprintf("k%d", i)
		if _, taken := p[name]; !taken {
			p[name] = v
			return "{" + name + ":" + typ + "}"
		}
	}
}

func (p paramsBinder) str(v string) string { return p.bind("String", v) }

func (p paramsBinder) strs(v []string) string {
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
	rows, err := cachedQuery[listStudy](s, sampleListIndexSQL)
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
