package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scopeAllowlist names the functions that may name a keyed table in SQL
// without a scope ("file:function:table"), each with the reason.
var scopeAllowlist = map[string]string{
	"access.go:State.studyIndex:sample_list":                                                  unmigrated,
	"availability.go:State.unavailableStudyIdentifiers:sample_list":                           unmigrated,
	"contract.go:State.missing:patient":                                                       unmigrated,
	"contract.go:State.missing:sample":                                                        unmigrated,
	"handlers_alterations.go:State.geneCounts:genomic_event_derived":                          unmigrated,
	"handlers_alterations.go:State.populateProfiled:sample_to_gene_panel_derived":             unmigrated,
	"handlers_alterations.go:State.populateQValues:sample_derived":                            unmigrated,
	"handlers_alterations.go:State.svCountsFetch:sample_to_gene_panel_derived":                unmigrated,
	"handlers_bins.go:State.customDataBinCountsFetch:sample_derived":                          unmigrated,
	"handlers_bins.go:State.scopedValueCounts:sample_derived":                                 unmigrated,
	"handlers_bulklist.go:State.allPatients:patient":                                          unmigrated,
	"handlers_bulklist.go:State.allPatients:sample":                                           unmigrated,
	"handlers_bulklist.go:State.sampleCopyNumberSegments:sample":                              unmigrated,
	"handlers_bulklist.go:sampleFullSelectFrom:sample":                                        unmigrated,
	"handlers_bulklist.go:sampleFullSelectFrom:sample_derived":                                unmigrated,
	"handlers_clinical.go:clinicalSQL:sample":                                                 unmigrated,
	"handlers_coexpression.go:State.coExpressionsFetch:genetic_alteration_derived":            unmigrated,
	"handlers_coexpression.go:State.scopedAttrPairs:sample":                                   unmigrated,
	"handlers_config.go:State.customDriverAnnotationReport:genomic_event_derived":             unmigrated,
	"handlers_core.go:State.patients:patient":                                                 unmigrated,
	"handlers_core.go:State.patientsFetch:patient":                                            unmigrated,
	"handlers_core.go:State.withResourceCounts:sample":                                        unmigrated,
	"handlers_core.go:studyCountJoins:clinical_event_data_derived":                            unmigrated,
	"handlers_core.go:studyCountJoins:genomic_event_derived":                                  unmigrated,
	"handlers_core.go:studyCountJoins:sample_list":                                            unmigrated,
	"handlers_enrichment.go:State.alterationEnrichmentsFetch:genomic_event_derived":           unmigrated,
	"handlers_enrichment.go:State.profiledPerGene:sample_derived":                             unmigrated,
	"handlers_enrichment.go:State.profiledPerGene:sample_to_gene_panel_derived":               unmigrated,
	"handlers_enrichment2.go:State.patientCasesToSamples:sample":                              unmigrated,
	"handlers_export.go:studyExport.caseLists:sample":                                         unmigrated,
	"handlers_export.go:studyExport.caseLists:sample_list":                                    unmigrated,
	"handlers_export.go:studyExport.loadCases:patient":                                        unmigrated,
	"handlers_export.go:studyExport.loadCases:sample":                                         unmigrated,
	"handlers_export.go:studyExport.structuralVariants:sample":                                unmigrated,
	"handlers_ga_enrichment.go:State.gaCaseMatrixKeys:sample":                                 unmigrated,
	"handlers_ga_enrichment.go:State.gaOneCasePerPatient:sample":                              unmigrated,
	"handlers_ga_enrichment.go:State.gaValueSequences:cbioportal_raw.generic_assay_matrix":    unmigrated,
	"handlers_genericassay.go:State.genericAssayDataEach:cbioportal_raw.generic_assay_matrix": unmigrated,
	"handlers_genericassay.go:State.genericAssayDataEach:sample":                              unmigrated,
	"handlers_genericassay2.go:State.genericAssayDataCountsFetch:generic_assay_data_derived":  unmigrated,
	"handlers_genericassay2.go:State.genericAssayDataCountsFetch:sample_derived":              unmigrated,
	"handlers_genesets.go:State.genesetScores:sample":                                         unmigrated,
	"handlers_misc.go:State.clinicalAttributeCountsFetch:sample":                              unmigrated,
	"handlers_misc.go:State.copyNumberSegmentsFetch:sample":                                   unmigrated,
	"handlers_namespace.go:State.namespaceDataCountsFetch:sample_derived":                     unmigrated,
	"handlers_namespace.go:State.namespaceDataFetch:sample":                                   unmigrated,
	"handlers_patientview.go:State.patient:patient":                                           unmigrated,
	"handlers_resources.go:State.patientResourceData:patient":                                 unmigrated,
	"handlers_resources.go:State.sampleResourceData:sample":                                   unmigrated,
	"handlers_status.go:State.sitemapIndex:patient":                                           unmigrated,
	"handlers_status.go:State.sitemapStudy:patient":                                           unmigrated,
	"handlers_studyview.go:State.clinicalDataCounts:clinical_data_derived":                    unmigrated,
	"handlers_studyview.go:State.clinicalDataCounts:sample_derived":                           unmigrated,
	"handlers_studyview.go:State.clinicalDataCountsWithMissing:sample_derived":                unmigrated,
	"handlers_studyview.go:State.filteredSamplesFetch:sample":                                 unmigrated,
	"handlers_studyview2.go:State.genomicDataCountsFetch:genetic_alteration_derived":          unmigrated,
	"handlers_studyview2.go:State.mutationDataCountsFetch:genomic_event_derived":              unmigrated,
	"handlers_studyview2.go:State.mutationDataCountsFetch:sample_derived":                     unmigrated,
	"handlers_studyview2.go:State.mutationDataCountsFetch:sample_to_gene_panel_derived":       unmigrated,
	"handlers_studyview2.go:State.sampleListsCountsFetch:sample_list":                         unmigrated,
	"handlers_studyview3.go:State.clinicalDataEnrichmentsFetch:sample":                        unmigrated,
	"handlers_studyview3.go:State.clinicalDataTableFetch:sample":                              unmigrated,
	"handlers_studyview3.go:State.customDataCountsFetch:sample_derived":                       unmigrated,
	"handlers_survival.go:State.clinicalEventsMetaFetch:clinical_event_data_derived":          unmigrated,
	"handlers_survival.go:State.survivalSpans:clinical_event_data_derived":                    unmigrated,
	"handlers_treatments.go:State.clinicalEventTypeCountsFetch:clinical_event_data_derived":   unmigrated,
	"handlers_treatments.go:State.clinicalEventTypeCountsFetch:sample_derived":                unmigrated,
	"handlers_treatments.go:State.sampleTreatmentCountsFetch:clinical_event_data_derived":     unmigrated,
	"handlers_treatments.go:State.sampleTreatmentRows:clinical_event_data_derived":            unmigrated,
	"handlers_treatments.go:State.studiesWithTreatments:clinical_event_data_derived":          unmigrated,
	"handlers_treatments.go:State.treatmentEventsByTier:clinical_event_data_derived":          unmigrated,
	"handlers_treatments.go:State.treatmentEventsByTier:sample_derived":                       unmigrated,
	"handlers_treatments.go:State.treatmentsDisplaySample:clinical_event_data_derived":        unmigrated,
	"handlers_treatments.go:State.treatmentsPatient:sample_derived":                           unmigrated,
	"importorder.go:State.sampleInternalIDs:sample":                                           unmigrated,
	"studyviewfilter.go:State.filterSQL:sample_derived":                                       unmigrated,
	"studyviewfilter.go:State.filterWhere:clinical_event_data_derived":                        unmigrated,
	"studyviewfilter.go:State.sampleDerivedScope:sample_derived":                              unmigrated,
	"studyviewfilter.go:State.scopeSampleCount:sample_derived":                                unmigrated,
	"studyviewfilter.go:filterCompiler.caseLists:sample_list":                                 unmigrated,
	"studyviewfilter.go:filterCompiler.clinicalData:clinical_data_derived":                    unmigrated,
	"studyviewfilter.go:filterCompiler.geneFilters:genomic_event_derived":                     unmigrated,
	"studyviewfilter.go:filterCompiler.genericAssayData:generic_assay_data_derived":           unmigrated,
	"studyviewfilter.go:filterCompiler.genericAssaySelection:generic_assay_data_derived":      unmigrated,
	"studyviewfilter.go:filterCompiler.genericAssaySelection:sample_derived":                  unmigrated,
	"studyviewfilter.go:filterCompiler.genomicData:genetic_alteration_derived":                unmigrated,
	"studyviewfilter.go:filterCompiler.genomicProfiles:sample_to_gene_panel_derived":          unmigrated,
	"studyviewfilter.go:filterCompiler.mutationData:genomic_event_derived":                    unmigrated,
	"studyviewfilter.go:filterCompiler.mutationData:sample_to_gene_panel_derived":             unmigrated,
	"studyviewfilter.go:filterCompiler.patientTreatments:clinical_event_data_derived":         unmigrated,
	"studyviewfilter.go:filterCompiler.sampleTreatments:clinical_event_data_derived":          unmigrated,
}

// unmigrated marks reads that predate the scope layer.
const unmigrated = "not yet read through a scope"

// TestKeyedTablesReadThroughScope fails when SQL in internal/api names a
// keyedTable (FROM or JOIN it) outside scope.go, so every read of the big
// study-partitioned tables gets its key predicate from a scope. Justified
// exceptions go in scopeAllowlist.
func TestKeyedTablesReadThroughScope(t *testing.T) {
	var names []string
	for _, kt := range keyedTables {
		names = append(names, regexp.QuoteMeta(kt.name))
	}
	ref := regexp.MustCompile(`\b(?:FROM|JOIN)\s+(` + strings.Join(names, "|") + `)\b`)

	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := map[string][]string{} // allowlist key -> positions
	for _, e := range files {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "scope.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
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
	a := newArgs()
	if got, want := byStudy("s1").from(a, tSample), "sample WHERE study_id = {a0:String}"; got != want {
		t.Errorf("byStudy from = %q, want %q", got, want)
	}
	a = newArgs()
	if got, want := byStudies([]string{"s1", "s2"}).samples(a), "(SELECT * FROM sample WHERE study_id IN {a0:Array(String)})"; got != want {
		t.Errorf("byStudies samples = %q, want %q", got, want)
	}
	a = newArgs()
	sc := scope{study: anyOf([]string{"s1"}), list: oneOf("s1_all")}
	if got, want := sc.listMembers(a), "(SELECT sample_id FROM sample_list WHERE study_id IN {a0:Array(String)} AND list_id = {a1:String})"; got != want {
		t.Errorf("listMembers = %q, want %q", got, want)
	}
	// A list scope joins the samples of its studies.
	a = newArgs()
	if got, want := sc.samples(a), "(SELECT * FROM sample WHERE study_id IN {a0:Array(String)})"; got != want {
		t.Errorf("list scope samples = %q, want %q", got, want)
	}
	// Without resolved studies a list scope keys on the list alone.
	a = newArgs()
	if got, want := (scope{list: oneOf("x")}).key(a, tSampleList, "sl"), "sl.list_id = {a0:String} AND "; got != want {
		t.Errorf("unkeyed list key = %q, want %q", got, want)
	}
	if got := (scope{}).rows(a, tSample); got != "sample" {
		t.Errorf("empty scope rows = %q", got)
	}
	a = newArgs()
	if got, want := bySamples("s1", []string{"a", "b"}).cond(a, tSample, "s"), "s.study_id = {a0:String} AND s.sample_id IN {a1:Array(String)}"; got != want {
		t.Errorf("bySamples cond = %q, want %q", got, want)
	}
	p := paramsBinder{"k0": "taken"}
	if got, want := byStudy("s1").cond(p, tPatient, ""), "study_id = {k1:String}"; got != want || p["k1"] != "s1" {
		t.Errorf("paramsBinder cond = %q (params %v), want %q", got, p, want)
	}
	defer func() {
		if recover() == nil {
			t.Error("a study scope over a table without a study column did not panic")
		}
	}()
	byStudy("s1").cond(newArgs(), tGenericAssayDataDerived, "")
}
