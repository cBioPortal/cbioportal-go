package contract

import (
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Cases fixed while measuring parity with www.cbioportal.org
// (cmd/paritydiff, test/parity/corpus.txt), against the Java source.

// CancerStudyMapper: a keyword search without sortBy is ordered by name.
func TestContractStudyKeywordOrderedByName(t *testing.T) {
	requireAPI(t)
	var studies []map[string]any
	get(t, "/api/studies?keyword=study", &studies)
	names := ids(studies, "name")
	if len(names) < 2 || !sort.StringsAreSorted(names) {
		t.Errorf("keyword results not ordered by name: %v", names)
	}
}

// MolecularDataServiceImpl walks genetic_alteration rows in primary key order,
// so a sample's genes come in genetic entity id order, not entrez order.
func TestContractMolecularDataGeneticEntityOrder(t *testing.T) {
	requireAPI(t)
	var d []struct {
		EntrezGeneID int `json:"entrezGeneId"`
	}
	post(t, "/api/molecular-profiles/study_es_0_mrna_median_Zscores/molecular-data/fetch",
		map[string]any{"sampleIds": []string{"TCGA-A1-A0SD-01"}}, &d)
	got := make([]int, len(d))
	for i, r := range d {
		got[i] = r.EntrezGeneID
	}
	// MMP2, PNMA1, RPS11, ZHX3, ERCC5 by genetic entity id.
	wantEq(t, got, []int{4313, 9240, 6205, 23051, 2073})
}

// ColumnarStoreStudyViewController: filtered samples carry ids only.
func TestContractFilteredSamplesShape(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/filtered-samples/fetch", es0(nil), &d)
	if len(d) == 0 {
		t.Fatal("no filtered samples")
	}
	wantEq(t, keys(d[0]), []string{"patientId", "sampleId", "studyId", "uniquePatientKey", "uniqueSampleKey"})
	if got := ids(d, "sampleId"); !sort.StringsAreSorted(got) {
		t.Error("filtered samples not ordered by sample id")
	}
}

// ColumnarStoreStudyViewController.fetchFilteredSamples declares
// negateFilters but never reads it: the filter applies as given.
func TestContractFilteredSamplesNegateFiltersIgnored(t *testing.T) {
	requireAPI(t)
	f := es0(map[string]any{"clinicalDataFilters": []map[string]any{
		{"attributeId": "SUBTYPE", "values": []map[string]any{{"value": "Claudin low"}}}}})
	var plain, negated []map[string]any
	post(t, "/api/filtered-samples/fetch", f, &plain)
	post(t, "/api/filtered-samples/fetch?negateFilters=true", f, &negated)
	wantEq(t, ids(plain, "sampleId"), []string{"TCGA-AR-A1AO-01", "TCGA-E2-A108-01"})
	wantEq(t, ids(negated, "sampleId"), ids(plain, "sampleId"))
}

// clinicalTable fetches a clinical-data-table page.
func clinicalTable(t *testing.T, query string) map[string][]map[string]any {
	t.Helper()
	var body struct {
		ByUniqueSampleKey map[string][]map[string]any `json:"byUniqueSampleKey"`
	}
	post(t, "/api/clinical-data-table/fetch?"+query, es0(nil), &body)
	return body.ByUniqueSampleKey
}

// attrValues returns the numeric values of an attribute on a table page.
func attrValues(page map[string][]map[string]any, attr string) []float64 {
	var out []float64
	for _, data := range page {
		for _, d := range data {
			if d["clinicalAttributeId"] == attr {
				v, _ := strconv.ParseFloat(d["value"].(string), 64)
				out = append(out, v)
			}
		}
	}
	sort.Float64s(out)
	return out
}

// StudyViewController clinical table: records without unique keys or the
// patientAttribute flag; a NUMBER attribute sorts numerically with samples
// lacking it last; the search is case-sensitive.
func TestContractClinicalDataTablePages(t *testing.T) {
	requireAPI(t)
	all := attrValues(clinicalTable(t, "pageSize=10000&sortBy=sampleId"), "MUTATION_COUNT")
	if len(all) < 10 {
		t.Fatalf("want MUTATION_COUNT on at least 10 samples, got %d", len(all))
	}
	// The samples with a value fill the first pages, smallest first.
	withValue := clinicalTable(t, "pageSize="+strconv.Itoa(len(all))+"&sortBy=MUTATION_COUNT&direction=ASC")
	wantEq(t, attrValues(withValue, "MUTATION_COUNT"), all)
	first := clinicalTable(t, "pageSize=5&sortBy=MUTATION_COUNT&direction=ASC")
	wantEq(t, attrValues(first, "MUTATION_COUNT"), all[:5])
	last := clinicalTable(t, "pageSize=5&sortBy=MUTATION_COUNT&direction=DESC")
	wantEq(t, attrValues(last, "MUTATION_COUNT"), all[len(all)-5:])
	for _, data := range first {
		for _, d := range data {
			for _, f := range []string{"uniqueSampleKey", "uniquePatientKey", "patientAttribute"} {
				if _, has := d[f]; has {
					t.Fatalf("table record has %s: %v", f, d)
				}
			}
		}
	}

	if got := clinicalTable(t, "pageSize=5&searchTerm=TCGA-A1&sortBy=sampleId"); len(got) == 0 {
		t.Error("search TCGA-A1 found nothing")
	}
	if got := clinicalTable(t, "pageSize=5&searchTerm=tcga-a1&sortBy=sampleId"); len(got) != 0 {
		t.Errorf("search is case-sensitive, tcga-a1 found %d samples", len(got))
	}
}

// ClickhouseGenomicDataMapper.getCNACounts: one NA count, for the first gene,
// the filtered samples minus that gene's counts.
func TestContractGenomicDataCountsNA(t *testing.T) {
	requireAPI(t)
	type item struct {
		Gene   string `json:"hugoGeneSymbol"`
		Counts []struct {
			Value string `json:"value"`
			Count int    `json:"count"`
		} `json:"counts"`
	}
	var d []item
	post(t, "/api/genomic-data-counts/fetch", map[string]any{
		"genomicDataFilters": []map[string]string{{"hugoGeneSymbol": "ACAP3", "profileType": "gistic"}, {"hugoGeneSymbol": "AGRN", "profileType": "gistic"}},
		"studyViewFilter":    es0(nil),
	}, &d)
	total := filteredCount(t, es0(nil))
	nas := 0
	for _, it := range d {
		sum, na := 0, -1
		for _, c := range it.Counts {
			if c.Value == "NA" {
				na = c.Count
				nas++
			} else {
				sum += c.Count
			}
		}
		if na >= 0 {
			wantEq(t, na, total-sum)
		}
	}
	wantEq(t, nas, 1)
}

// Structural variants: site symbols are always written ("" without a gene),
// and the study-view counts carry AlterationCountByStructuralVariant's
// derived properties.
func TestContractStructuralVariantShapes(t *testing.T) {
	requireAPI(t)
	var svs []map[string]any
	post(t, "/api/structural-variant/fetch", map[string]any{"molecularProfileIds": []string{"study_es_0_structural_variants"}}, &svs)
	for _, sv := range svs {
		for _, k := range []string{"site1HugoSymbol", "site2HugoSymbol"} {
			if _, ok := sv[k].(string); !ok {
				t.Fatalf("%s missing: %v", k, sv)
			}
		}
	}
	var counts []map[string]any
	post(t, "/api/structuralvariant-counts/fetch", es0(nil), &counts)
	if len(counts) == 0 {
		t.Fatal("no structural variant counts")
	}
	c := counts[0]
	wantEq(t, c["uniqueEventKey"], c["gene1HugoGeneSymbol"].(string)+"::"+c["gene2HugoGeneSymbol"].(string))
	wantEq(t, c["hugoGeneSymbols"], []any{c["gene1HugoGeneSymbol"], c["gene2HugoGeneSymbol"]})
	wantEq(t, c["entrezGeneIds"], []any{c["gene1EntrezGeneId"], c["gene2EntrezGeneId"]})
}

// ExpressionEnrichmentUtil sets neither cytoband nor q-value on expression
// enrichments, nor name and q-value on generic-assay ones.
func TestContractContinuousEnrichmentShapes(t *testing.T) {
	requireAPI(t)
	group := func(name, profile string, samples ...string) map[string]any {
		var cs []map[string]string
		for _, s := range samples {
			cs = append(cs, map[string]string{"caseId": s, "molecularProfileId": profile})
		}
		return map[string]any{"name": name, "molecularProfileCaseIdentifiers": cs}
	}
	var samples []map[string]any
	get(t, "/api/studies/study_es_0/samples", &samples)
	ids := ids(samples, "sampleId")
	for _, c := range []struct{ path, profile string }{
		{"/api/expression-enrichments/fetch", "study_es_0_mrna"},
		{"/api/generic-assay-enrichments/fetch", "study_es_0_treatment_ic50"},
	} {
		var d []map[string]any
		post(t, c.path, []any{group("a", c.profile, ids[:400]...), group("b", c.profile, ids[400:]...)}, &d)
		if len(d) == 0 {
			t.Errorf("%s: no enrichments", c.path)
			continue
		}
		for _, k := range []string{"qValue", "cytoband", "name"} {
			if _, has := d[0][k]; has {
				t.Errorf("%s: record has %s: %v", c.path, k, d[0])
			}
		}
	}
}

// PatientMapper's keyword LIKE is case-sensitive on ClickHouse, and _ is a
// wildcard.
func TestContractPatientKeywordLike(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	get(t, "/api/patients?keyword=tcga-a1-a0sb", &d)
	wantEq(t, len(d), 0)
	get(t, "/api/patients?keyword=TCGA_A1_A0SB-02", &d)
	wantEq(t, ids(d, "patientId"), []string{"TCGA-A1-A0SB"})
}

// MutationMapper selects by molecular profile, not study: a study's other
// profiles (e.g. MUTATION_UNCALLED next to MUTATION_EXTENDED) have their own
// mutations.
func TestContractMutationsByProfile(t *testing.T) {
	requireAPI(t)
	ident := func(profile string) map[string]any {
		return map[string]any{"entrezGeneIds": []int{7157},
			"sampleMolecularIdentifiers": []map[string]string{{"molecularProfileId": profile, "sampleId": "TCGA-A1-A0SB-01"}}}
	}
	var d []map[string]any
	post(t, "/api/mutations/fetch", ident("study_es_0_mutations"), &d)
	if len(d) == 0 {
		t.Fatal("no TP53 mutation in TCGA-A1-A0SB-01")
	}
	post(t, "/api/mutations/fetch", ident("study_es_0_gistic"), &d)
	wantEq(t, len(d), 0)
	post(t, "/api/mutations/fetch", map[string]any{"molecularProfileIds": []string{"study_es_0_gistic"}, "entrezGeneIds": []int{7157}}, &d)
	wantEq(t, len(d), 0)
	post(t, "/api/molecular-profiles/study_es_0_gistic/mutations/fetch", map[string]any{"sampleListId": "study_es_0_all"}, &d)
	wantEq(t, len(d), 0)
}

// GenesetMapper.getGenesetVersion: an MSigDB version (loaded or the seed's).
func TestContractGenesetVersion(t *testing.T) {
	requireAPI(t)
	var v string
	get(t, "/api/genesets/version", &v)
	if !strings.HasPrefix(v, "msigdb_") {
		t.Errorf("geneset version %q", v)
	}
}
