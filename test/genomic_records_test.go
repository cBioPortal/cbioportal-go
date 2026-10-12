package contract

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Genomic record shapes as the Java API writes them (Jackson NON_NULL, the
// importer's stored values, projection-dependent nested genes).

func TestContractDiscreteCopyNumberAllReadsProfileValues(t *testing.T) {
	requireAPI(t)
	body := map[string]any{"sampleIds": []string{"TCGA-A2-A04U-01", "TCGA-A1-A0SB-01"}, "entrezGeneIds": []int{116983, 375790}}
	var all []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch?discreteCopyNumberEventType=ALL", body, &all)
	var got [][3]any
	for _, d := range all {
		got = append(got, [3]any{d["sampleId"], d["entrezGeneId"], d["alteration"]})
		for _, k := range []string{"gene", "driverFilter", "driverTiersFilter", "namespaceColumns"} {
			if _, ok := d[k]; ok {
				t.Errorf("ALL (molecular-data path) SUMMARY record has %s: %v", k, d)
			}
		}
	}
	// Sample-major like molecular data; diploid cells included.
	wantEq(t, got, [][3]any{
		{"TCGA-A1-A0SB-01", 116983.0, 0.0}, {"TCGA-A1-A0SB-01", 375790.0, 0.0},
		{"TCGA-A2-A04U-01", 116983.0, 2.0}, {"TCGA-A2-A04U-01", 375790.0, 2.0},
	})

	var diploid []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch?discreteCopyNumberEventType=DIPLOID", body, &diploid)
	wantEq(t, len(diploid), 2)

	var events []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch?projection=DETAILED", body, &events)
	if len(events) != 2 {
		t.Fatalf("HOMDEL_AND_AMP events: %v", events)
	}
	// Event records: gene-major, nested gene with type, driver annotation.
	wantEq(t, []any{events[0]["entrezGeneId"], events[1]["entrezGeneId"]}, []any{116983.0, 375790.0})
	gene := events[0]["gene"].(map[string]any)
	wantEq(t, []any{gene["hugoGeneSymbol"], gene["type"]}, []any{"ACAP3", "protein-coding"})
	wantEq(t, events[1]["driverFilter"], "Putative_Driver")
	// A record of a profile declaring namespaces carries each of them, empty
	// without values (the importer stores the columns as null).
	wantEq(t, events[1]["namespaceColumns"], map[string]any{"MyNamespace": map[string]any{}, "MyNamespace2": map[string]any{}})
}

func TestContractDiscreteCopyNumberGetGeneMajor(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	get(t, "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number?sampleListId=study_es_0_all", &d)
	if len(d) == 0 {
		t.Fatal("no CNA events")
	}
	for i := 1; i < len(d); i++ {
		a, b := d[i-1], d[i]
		ka := [2]float64{a["entrezGeneId"].(float64), a["alteration"].(float64)}
		kb := [2]float64{b["entrezGeneId"].(float64), b["alteration"].(float64)}
		if ka[0] > kb[0] || (ka[0] == kb[0] && ka[1] > kb[1]) ||
			(ka == kb && a["sampleId"].(string) > b["sampleId"].(string)) {
			t.Fatalf("not ordered by entrezGeneId, alteration, sampleId at %d: %v / %v", i, a, b)
		}
		if _, ok := a["gene"]; ok {
			t.Fatalf("SUMMARY CNA record has gene: %v", a)
		}
	}
}

func TestContractMutationRecordShape(t *testing.T) {
	requireAPI(t)
	body := map[string]any{"sampleListId": "study_es_0_all", "entrezGeneIds": []int{7157, 3983}}
	var summary, detailed []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_mutations/mutations/fetch", body, &summary)
	post(t, "/api/molecular-profiles/study_es_0_mutations/mutations/fetch?projection=DETAILED", body, &detailed)
	if len(summary) == 0 || len(summary) != len(detailed) {
		t.Fatalf("summary %d, detailed %d", len(summary), len(detailed))
	}
	withDriver, withoutDriver := 0, 0
	for i, m := range summary {
		if _, ok := m["gene"]; ok {
			t.Fatalf("SUMMARY mutation has gene: %v", m)
		}
		if _, ok := m["namespaceColumns"]; ok && m["namespaceColumns"] == nil {
			t.Errorf("null namespaceColumns written: %v", m)
		}
		if m["validationStatus"] == "" || m["center"] == "" || m["mutationStatus"] == "" {
			t.Errorf("empty MAF text column not stored as NA: %v", m)
		}
		if _, ok := m["driverFilter"]; ok {
			withDriver++
		} else {
			withoutDriver++
			if _, ok := m["driverFilterAnnotation"]; ok {
				t.Errorf("driver annotation without driver filter: %v", m)
			}
		}
		g, _ := detailed[i]["gene"].(map[string]any)
		if g == nil || g["type"] != "protein-coding" || g["hugoGeneSymbol"] == "" {
			t.Errorf("DETAILED gene: %v", detailed[i]["gene"])
		}
	}
	if withDriver == 0 || withoutDriver == 0 {
		t.Errorf("driver fields: %d with, %d without", withDriver, withoutDriver)
	}
	keywords := map[string]bool{}
	for _, m := range summary {
		if k, ok := m["keyword"].(string); ok {
			keywords[k] = true
		}
	}
	for _, k := range []string{"TP53 G199 missense", "TP53 truncating", "ABLIM1 H333 missense"} {
		if !keywords[k] {
			t.Errorf("keyword %q missing from %v", k, keywords)
		}
	}
	for i := 1; i < len(summary); i++ {
		a, b := summary[i-1], summary[i]
		if a["sampleId"].(string) > b["sampleId"].(string) ||
			(a["sampleId"] == b["sampleId"] && a["entrezGeneId"].(float64) > b["entrezGeneId"].(float64)) {
			t.Fatalf("not ordered by sampleId, entrezGeneId at %d", i)
		}
	}

	var ids []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_mutations/mutations/fetch?projection=ID", body, &ids)
	wantEq(t, len(ids), len(summary))
	wantEq(t, len(ids[0]), 7)
}

func TestContractVariantCountKeyword(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/molecular-profiles/study_es_0_mutations/variant-counts/fetch",
		[]map[string]any{{"entrezGeneId": 7157, "keyword": "TP53 truncating"}}, &d)
	if len(d) != 1 || d[0]["numberOfSamplesWithKeyword"].(float64) < 1 ||
		d[0]["numberOfSamplesWithKeyword"].(float64) > d[0]["numberOfSamplesWithMutationInGene"].(float64) {
		t.Fatalf("variant count: %v", d)
	}
}

func TestContractMolecularDataNumericValue(t *testing.T) {
	requireAPI(t)
	body := map[string]any{"sampleIds": []string{"TCGA-A1-A0SD-01"}, "entrezGeneIds": []int{6205}}
	_, raw := rawDo(t, "POST", "/api/molecular-profiles/study_es_0_mrna_median_Zscores/molecular-data/fetch",
		mustJSON(t, body))
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var d []map[string]any
	if err := dec.Decode(&d); err != nil || len(d) != 1 {
		t.Fatalf("molecular data: %s", raw)
	}
	if _, ok := d[0]["value"].(json.Number); !ok {
		t.Errorf("value is not a JSON number: %s", raw)
	}
	if _, ok := d[0]["gene"]; ok {
		t.Errorf("SUMMARY molecular data has gene: %s", raw)
	}
	var detailed []map[string]any
	post(t, "/api/molecular-data/fetch?projection=DETAILED", map[string]any{
		"sampleMolecularIdentifiers": []map[string]string{{"molecularProfileId": "study_es_0_mrna_median_Zscores", "sampleId": "TCGA-A1-A0SD-01"}},
		"entrezGeneIds":              []int{6205},
	}, &detailed)
	if len(detailed) != 1 || detailed[0]["gene"].(map[string]any)["type"] != "protein-coding" {
		t.Errorf("DETAILED multi-profile molecular data: %v", detailed)
	}
}

func TestContractStructuralVariantStoredValues(t *testing.T) {
	requireAPI(t)
	var d []map[string]any
	post(t, "/api/structural-variant/fetch", map[string]any{
		"molecularProfileIds": []string{"study_es_0_structural_variants"}, "entrezGeneIds": []int{238}}, &d)
	if len(d) == 0 {
		t.Fatal("no ALK structural variants")
	}
	drivers := 0
	for i, sv := range d {
		if sv["site1Contig"] != "NA" || sv["normalReadCount"] != -1.0 {
			t.Errorf("empty SV columns not NA / -1: %v", sv)
		}
		if _, ok := sv["namespaceColumns"]; ok && sv["namespaceColumns"] == nil {
			t.Errorf("null namespaceColumns written: %v", sv)
		}
		if _, ok := sv["driverFilter"]; ok {
			drivers++
		} else if _, ok := sv["driverFilterAnn"]; ok {
			t.Errorf("driver annotation without driver filter: %v", sv)
		}
		if i > 0 && d[i-1]["site1HugoSymbol"].(string) > sv["site1HugoSymbol"].(string) {
			t.Errorf("not ordered by site1 gene symbol: %v then %v", d[i-1]["site1HugoSymbol"], sv["site1HugoSymbol"])
		}
	}
	if drivers == 0 || drivers == len(d) {
		t.Errorf("driver fields on %d of %d SVs", drivers, len(d))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// entrezGeneId on the copy-number GET is a parameter go adds (Java's
// controller has none and answers every gene): it gives the answer of the
// POST /fetch naming that gene, for each event type, and without it the
// answer is every gene's.
func TestContractDiscreteCopyNumberGetGeneParam(t *testing.T) {
	requireAPI(t)
	const list = "/api/molecular-profiles/study_es_0_gistic/discrete-copy-number"
	for _, ev := range []string{"HOMDEL_AND_AMP", "ALL"} {
		var all, one, fetched []map[string]any
		get(t, list+"?sampleListId=study_es_0_all&discreteCopyNumberEventType="+ev, &all)
		get(t, list+"?sampleListId=study_es_0_all&discreteCopyNumberEventType="+ev+"&entrezGeneId=116983", &one)
		post(t, list+"/fetch?discreteCopyNumberEventType="+ev,
			map[string]any{"sampleListId": "study_es_0_all", "entrezGeneIds": []int{116983}}, &fetched)
		if len(one) == 0 || len(one) >= len(all) {
			t.Errorf("%s: %d calls for one gene, %d for every gene", ev, len(one), len(all))
		}
		wantEq(t, one, fetched)
		for _, d := range one {
			if d["entrezGeneId"] != 116983.0 {
				t.Errorf("%s: call of another gene %v", ev, d)
			}
		}
	}
	wantError(t, "GET", list+"?sampleListId=study_es_0_all&entrezGeneId=TP53", "", 400,
		`Request parameter type mismatch: java.lang.NumberFormatException: For input string: "TP53"`)
}
