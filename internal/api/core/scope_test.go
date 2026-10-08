package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scopeAllowlist names the functions that may name a keyed table in SQL
// without a scope ("file:function:table"), each with the reason.
var scopeAllowlist = map[string]string{
	// Indexes and listings over every study.
	"core/availability.go:State.unavailableStudyIdentifiers:sample_list": "cached index of the unavailable studies' lists, keyed by a study subquery",
	"studies/studies.go:studyCountJoins:sample_list":                     "per-study counts of the study listing, aggregated over every study (cached)",
	"studies/studies.go:studyCountJoins:clinical_event_data_derived":     "per-study counts of the study listing, aggregated over every study (cached)",
	"studies/studies.go:studyCountJoins:genomic_event_derived":           "per-study counts of the study listing, aggregated over every study (cached)",

	// sample_to_gene_panel_derived is sorted by panel, alteration type and
	// profile, without the study.
	"studyview/alterations.go:Server.populateProfiled:sample_to_gene_panel_derived": "reads name the profiles, the WES panel or the alteration type its key holds",
	"studyview/alterations.go:Server.SVCountsFetch:sample_to_gene_panel_derived":    "reads name the profiles or the WES panel its key holds",

	// generic_assay_data_derived has no study column; its key leads with the
	// profile type and entity.
	"studyview/genericassaycounts.go:Server.GenericAssayDataCountsFetch:generic_assay_data_derived": "reads name the profile type its key leads with",
	"studyview/studyviewfilter.go:filterCompiler.genericAssayData:generic_assay_data_derived":       "reads name the profile type and entity its key leads with",
	"studyview/studyviewfilter.go:filterCompiler.genericAssaySelection:generic_assay_data_derived":  "reads name the profile type its key leads with",

	// The studies are a subquery: those of the filter defining the attribute.
	"studyview/studyviewfilter.go:filterCompiler.clinicalData:clinical_data_derived": "keyed by the filter's studies that define the attribute (a subquery)",
}

// TestKeyedTablesReadThroughScope fails when SQL in internal/api (any of its
// packages) names a keyedTable (FROM or JOIN it) outside core/scope.go, so
// every read of the big study-partitioned tables gets its key predicate from
// a scope. Justified exceptions go in scopeAllowlist, keyed by the file's
// path below internal/api.
func TestKeyedTablesReadThroughScope(t *testing.T) {
	var names []string
	for _, kt := range keyedTables {
		names = append(names, regexp.QuoteMeta(kt.Name))
	}
	ref := regexp.MustCompile(`\b(?:FROM|JOIN)\s+(` + strings.Join(names, "|") + `)\b`)

	// Every package of internal/api, keyed by the path below it.
	var files []string
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := filepath.ToSlash(strings.TrimPrefix(p, ".."+string(filepath.Separator)))
		if !d.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && name != "core/scope.go" {
			files = append(files, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := map[string][]string{} // allowlist key -> positions
	for _, name := range files {
		f, err := parser.ParseFile(fset, filepath.Join("..", filepath.FromSlash(name)), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			owner := declName(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				for _, m := range ref.FindAllStringSubmatch(s, -1) {
					k := name + ":" + owner + ":" + m[1]
					found[k] = append(found[k], fset.Position(lit.Pos()).String())
				}
				return true
			})
		}
	}

	var bad []string
	for k, pos := range found {
		if _, ok := scopeAllowlist[k]; !ok {
			bad = append(bad, k+"  ("+strings.Join(pos, ", ")+")")
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("SQL names a keyed table without a scope: %s", b)
	}
	var stale []string
	for k := range scopeAllowlist {
		if _, ok := found[k]; !ok {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		t.Errorf("scopeAllowlist entry no longer needed: %s", k)
	}
}

// declName names a top-level declaration: a function (Type.method for
// methods) or the first name of a var/const group.
func declName(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) > 0 {
			t := d.Recv.List[0].Type
			if st, ok := t.(*ast.StarExpr); ok {
				t = st.X
			}
			if id, ok := t.(*ast.Ident); ok {
				return id.Name + "." + d.Name.Name
			}
		}
		return d.Name.Name
	case *ast.GenDecl:
		for _, sp := range d.Specs {
			if vs, ok := sp.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
				return vs.Names[0].Name
			}
		}
	}
	return "?"
}

func TestScopeRendering(t *testing.T) {
	a := NewArgs()
	if got, want := ByStudy("s1").From(a, TSample), "sample WHERE study_id = {a0:String}"; got != want {
		t.Errorf("byStudy from = %q, want %q", got, want)
	}
	a = NewArgs()
	if got, want := ByStudies([]string{"s1", "s2"}).Samples(a), "(SELECT * FROM sample WHERE study_id IN {a0:Array(String)})"; got != want {
		t.Errorf("byStudies samples = %q, want %q", got, want)
	}
	a = NewArgs()
	sc := Scope{study: anyOf([]string{"s1"}), list: oneOf("s1_all")}
	if got, want := sc.ListMembers(a), "(SELECT sample_id FROM sample_list WHERE study_id IN {a0:Array(String)} AND list_id = {a1:String})"; got != want {
		t.Errorf("listMembers = %q, want %q", got, want)
	}
	// A list scope joins the samples of its studies.
	a = NewArgs()
	if got, want := sc.Samples(a), "(SELECT * FROM sample WHERE study_id IN {a0:Array(String)})"; got != want {
		t.Errorf("list scope samples = %q, want %q", got, want)
	}
	// Without resolved studies a list scope keys on the list alone.
	a = NewArgs()
	if got, want := (Scope{list: oneOf("x")}).Key(a, TSampleList, "sl"), "sl.list_id = {a0:String} AND "; got != want {
		t.Errorf("unkeyed list key = %q, want %q", got, want)
	}
	if got := (Scope{}).Rows(a, TSample); got != "sample" {
		t.Errorf("empty scope rows = %q", got)
	}
	a = NewArgs()
	if got, want := BySamples("s1", []string{"a", "b"}).Cond(a, TSample, "s"), "s.study_id = {a0:String} AND s.sample_id IN {a1:Array(String)}"; got != want {
		t.Errorf("bySamples cond = %q, want %q", got, want)
	}
	// A joined table carries the scope's samples, not only its study.
	a = NewArgs()
	if got, want := BySample("s1", "a").Cases().Rows(a, TSampleDerived),
		"(SELECT * FROM sample_derived WHERE cancer_study_identifier = {a0:String} AND sample_stable_id = {a1:String})"; got != want {
		t.Errorf("bySample derived rows = %q, want %q", got, want)
	}
	a = NewArgs()
	if got, want := ByProfiles([]string{"s1_mutations"}).From(a, TGenomicEventDerived),
		"genomic_event_derived WHERE genetic_profile_stable_id IN {a0:Array(String)}"; got != want {
		t.Errorf("byProfiles from = %q, want %q", got, want)
	}
	a = NewArgs()
	ids := []Identifier{{StudyID: "s2", SampleID: "x"}, {StudyID: "s1", SampleID: "y"}, {StudyID: "s2", SampleID: "z"}}
	if got, want := IdentifiersCond(a, TSeg, "g", ids),
		"((g.study_id = {a0:String} AND g.sample_id IN {a1:Array(String)}) OR (g.study_id = {a2:String} AND g.sample_id IN {a3:Array(String)}))"; got != want {
		t.Errorf("identifiersCond = %q, want %q", got, want)
	}
	if got := IdentifiersCond(NewArgs(), TSample, "", nil); got != FalseClause {
		t.Errorf("identifiersCond of none = %q", got)
	}
	p := ParamsBinder{"k0": "taken"}
	if got, want := ByStudy("s1").Cond(p, TPatient, ""), "study_id = {k1:String}"; got != want || p["k1"] != "s1" {
		t.Errorf("paramsBinder cond = %q (params %v), want %q", got, p, want)
	}
	defer func() {
		if recover() == nil {
			t.Error("a study scope over a table without a study column did not panic")
		}
	}()
	ByStudy("s1").Cond(NewArgs(), TGenericAssayDataDerived, "")
}
