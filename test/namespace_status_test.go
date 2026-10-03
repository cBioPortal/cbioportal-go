package contract

import (
	"net/http"
	"strings"
	"testing"
)

// Namespace attributes: study_es_0 declares Zygosity (MAF columns
// Zygosity.name / Zygosity.code, set on TEST_SAMPLE_SOMATIC_HETEROZYGOUS = 1 /
// Heterozygous and TEST_SAMPLE_SOMATIC_HOMOZYGOUS = 2 / Homozygous),
// StructVarNs / StructVarNs2 (two SV records of TCGA-A2-A04P-01) and
// MyNamespace / MyNamespace2 (the ACAP3 amplification of TCGA-A2-A04U-01).
// NamespaceMapper reads the mutation, structural_variant and sample_cna_event
// tables in that order; MySQL lists a JSON object's keys shortest first.

var nsSamples = []map[string]string{
	{"studyId": "study_es_0", "sampleId": "TCGA-A2-A04P-01"},
	{"studyId": "study_es_0", "sampleId": "TEST_SAMPLE_SOMATIC_HOMOZYGOUS"},
	{"studyId": "study_es_0", "sampleId": "TEST_SAMPLE_SOMATIC_HETEROZYGOUS"},
	{"studyId": "study_es_0", "sampleId": "TCGA-A2-A04U-01"},
	{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SB-01"},
}

func nsKeys(rows []map[string]any) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r["outerKey"].(string) + "." + r["innerKey"].(string)
	}
	return out
}

func TestContractNamespaceAttributes(t *testing.T) {
	requireAPI(t)
	var attrs []map[string]any
	post(t, "/api/namespace-attributes/fetch", []string{"study_es_0"}, &attrs)
	wantEq(t, nsKeys(attrs), []string{
		"Zygosity.code", "Zygosity.name",
		"StructVarNs.column1", "StructVarNs.column2", "StructVarNs2.lorem",
		"MyNamespace.column1", "MyNamespace.column2", "MyNamespace2.blarp",
	})
	post(t, "/api/namespace-attributes/fetch", []string{"lgg_ucsf_2014_test_generic_assay"}, &attrs)
	wantEq(t, len(attrs), 0)
}

func TestContractNamespaceAttributeCounts(t *testing.T) {
	requireAPI(t)
	var counts []map[string]any
	post(t, "/api/namespace-attributes/counts/fetch", map[string]any{
		"sampleIdentifiers": nsSamples,
		"namespaceAttributes": []map[string]string{
			{"outerKey": "Zygosity", "innerKey": "name"},
			{"outerKey": "StructVarNs", "innerKey": "column1"},
			{"outerKey": "MyNamespace", "innerKey": "column2"},
			{"outerKey": "NoSuchNamespace", "innerKey": "x"},
		},
	}, &counts)
	got := map[string]float64{}
	for _, c := range counts {
		got[c["outerKey"].(string)+"."+c["innerKey"].(string)] = c["count"].(float64)
	}
	wantEq(t, got, map[string]float64{"Zygosity.name": 2, "StructVarNs.column1": 1, "MyNamespace.column2": 1})
}

func TestContractNamespaceDataCounts(t *testing.T) {
	requireAPI(t)
	attrs := []map[string]string{{"outerKey": "Zygosity", "innerKey": "code"}, {"outerKey": "NoSuchNamespace", "innerKey": "x"}}
	var items []map[string]any
	post(t, "/api/namespace-data-counts/fetch", map[string]any{
		"attributes": attrs, "studyViewFilter": map[string]any{"studyIds": []string{"study_es_0"}},
	}, &items)
	// An attribute without data maps to null (Java's stream maps it to null).
	if len(items) != 2 || items[1] != nil {
		t.Fatalf("want [item, null], got %v", items)
	}
	wantEq(t, items[0]["counts"], []any{
		map[string]any{"value": "1", "count": 1.0, "totalCount": 1.0},
		map[string]any{"value": "2", "count": 1.0, "totalCount": 1.0},
	})

	// A namespace selection (values compared upper-cased) narrows the samples.
	post(t, "/api/namespace-data-counts/fetch", map[string]any{
		"attributes": attrs[:1],
		"studyViewFilter": map[string]any{"studyIds": []string{"study_es_0"}, "namespaceDataFilters": []map[string]any{{
			"outerKey": "Zygosity", "innerKey": "name", "values": [][]map[string]string{{{"value": "homozygous"}}},
		}}},
	}, &items)
	wantEq(t, len(items), 1)
	wantEq(t, items[0]["counts"], []any{map[string]any{"value": "2", "count": 1.0, "totalCount": 1.0}})
}

func TestContractNamespaceDataForComparison(t *testing.T) {
	requireAPI(t)
	var rows []map[string]any
	post(t, "/api/namespace-data/fetch", map[string]any{
		"sampleIdentifiers":  nsSamples,
		"namespaceAttribute": map[string]string{"outerKey": "Zygosity", "innerKey": "name"},
		"values":             []string{"Homozygous", "heterozygous", "Heterozygous"},
	}, &rows)
	wantEq(t, rows, []map[string]any{
		{"outerKey": "Zygosity", "innerKey": "name", "sampleId": "TEST_SAMPLE_SOMATIC_HOMOZYGOUS", "studyId": "study_es_0",
			"patientId": "TEST_PATIENT_NAMESPACE", "attrValue": "Homozygous"},
		{"outerKey": "Zygosity", "innerKey": "name", "sampleId": "TEST_SAMPLE_SOMATIC_HETEROZYGOUS", "studyId": "study_es_0",
			"patientId": "TEST_PATIENT_NAMESPACE", "attrValue": "Heterozygous"},
	})
}

// ------------------------------------------------------------ status ----

func TestContractServerStatus(t *testing.T) {
	requireAPI(t)
	var st map[string]any
	get(t, "/api/health", &st)
	wantEq(t, st, map[string]any{"status": "UP"})

	var ts map[string]string
	get(t, "/api/timestamps", &ts)
	if len(ts) != 2 || ts["gene"] == "" || ts["reference_genome_gene"] == "" {
		t.Fatalf("timestamps %v", ts)
	}
	for k, v := range ts {
		// MySQL DATETIME as JDBC renders it.
		if len(v) != len("2006-01-02 15:04:05") || v[4] != '-' || v[10] != ' ' {
			t.Errorf("%s: %q", k, v)
		}
	}

	resp, body := rawDo(t, http.MethodGet, "/api/hello", "")
	wantEq(t, resp.StatusCode, http.StatusOK)
	wantEq(t, string(body), "Greetings from Spring Boot!")
}

// The SEO endpoints exist only with the sitemaps flag, which the e2e
// properties leave off: 404 with no body.
func TestContractSitemapsDisabled(t *testing.T) {
	requireAPI(t)
	for _, p := range []string{"/robots.txt", "/sitemap_index.xml", "/sitemap_study.xml?studyId=study_es_0"} {
		resp, body := rawDo(t, http.MethodGet, p, "")
		wantEq(t, resp.StatusCode, http.StatusNotFound)
		if strings.TrimSpace(string(body)) != "" {
			t.Errorf("%s: body %q", p, body)
		}
	}
}
