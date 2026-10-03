package contract

import (
	"net/http"
	"testing"
)

// Gene set contracts against the reference loaded from cbioportal-test's
// data/genesets (test/setup_test_db.sh: load-genesets of
// study_es_0_genesets.gmt, study_es_0_supp-genesets.txt and
// study_es_0_tree.yaml, version msigdb_7.5.1), as importGenesetData.pl and
// importGenesetHierarchy.pl load them for the Java e2e database. Scores are
// study_es_0's data_gsva_scores.txt / data_gsva_pvalues.txt, read as the
// molecular data mapper truncates them (4 decimals).

// requireGenesets skips unless the API serves the fixture gene set reference.
func requireGenesets(t *testing.T) {
	t.Helper()
	requireAPI(t)
	var v string
	get(t, "/api/genesets/version", &v)
	if v != "msigdb_7.5.1" {
		t.Skipf("gene set reference not loaded (version %q)", v)
	}
}

func gsIDs(rows []map[string]any) []string { return ids(rows, "genesetId") }

func TestContractGenesets(t *testing.T) {
	requireGenesets(t)
	all := []string{"GO_ACYLGLYCEROL_HOMEOSTASIS", "GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS",
		"GO_ANTEROGRADE_AXONAL_TRANSPORT", "GO_APICAL_PROTEIN_LOCALIZATION", "GO_ATP_DEPENDENT_CHROMATIN_REMODELING",
		"GO_CARBOHYDRATE_CATABOLIC_PROCESS", "GO_CARDIAC_CHAMBER_DEVELOPMENT"}
	var rows []map[string]any
	get(t, "/api/genesets", &rows)
	wantEq(t, gsIDs(rows), all)
	// Name and description from the supplementary file, the link from the GMT.
	wantEq(t, rows[0], map[string]any{
		"genesetId": "GO_ACYLGLYCEROL_HOMEOSTASIS", "name": "GO_ACYLGLYCEROL_HOMEOSTASIS",
		"description": "Genes associated with ACYLGLYCEROL_HOMEOSTASIS based on gene ontology from C5 (MSigDB)",
		"refLink":     "http://dummy",
	})
	get(t, "/api/genesets?pageSize=2&pageNumber=1", &rows)
	wantEq(t, gsIDs(rows), all[2:4])

	post(t, "/api/genesets/fetch", []string{"GO_CARDIAC_CHAMBER_DEVELOPMENT", "NOPE", "GO_ACYLGLYCEROL_HOMEOSTASIS"}, &rows)
	wantEq(t, gsIDs(rows), []string{"GO_ACYLGLYCEROL_HOMEOSTASIS", "GO_CARDIAC_CHAMBER_DEVELOPMENT"})
	resp, body := rawDo(t, http.MethodPost, "/api/genesets/fetch", "[]")
	wantEq(t, []any{resp.StatusCode, string(body)}, []any{400, `{"message":"genesetIds size must be between 1 and 10000000"}`})

	var one map[string]any
	get(t, "/api/genesets/GO_APICAL_PROTEIN_LOCALIZATION", &one)
	wantEq(t, one["description"], "Genes associated with APICAL_PROTEIN_LOCALIZATION based on gene ontology from C5 (MSigDB)")
	resp, body = rawDo(t, http.MethodGet, "/api/genesets/NOPE", "")
	wantEq(t, []any{resp.StatusCode, string(body)}, []any{404, `{"message":"Gene set not found: NOPE"}`})
}

type hierarchyNode struct {
	NodeID         int     `json:"nodeId"`
	NodeName       string  `json:"nodeName"`
	ParentID       int     `json:"parentId"`
	ParentNodeName *string `json:"parentNodeName"`
	Genesets       *[]struct {
		GenesetID            string   `json:"genesetId"`
		RepresentativeScore  *float64 `json:"representativeScore"`
		RepresentativePvalue *float64 `json:"representativePvalue"`
	} `json:"genesets"`
}

// GenesetHierarchyServiceImpl: the tree's nodes are numbered depth-first
// (Custom Gene sets 1, Different Gene sets from Random Institute 2 with
// subcategories 3 and 4). A gene set's representative score is the
// percentile-th (default 75) smallest positive or largest negative score,
// whichever is larger in magnitude, with that sample's p-value; gene sets
// under |0.4| or above p 0.05 are dropped, and nodes left without gene sets
// or descendants with gene sets are dropped.
func TestContractGenesetHierarchy(t *testing.T) {
	requireGenesets(t)
	var nodes []hierarchyNode
	post(t, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_gsva_scores", nil, &nodes)
	if len(nodes) != 2 {
		t.Fatalf("nodes %+v", nodes)
	}
	// A top-level node: no parent id and an empty parent name.
	wantEq(t, []any{nodes[0].NodeID, nodes[0].NodeName, nodes[0].ParentID, *nodes[0].ParentNodeName},
		[]any{2, "Different Gene sets from Random Institute", 0, ""})
	wantEq(t, []any{nodes[1].NodeID, nodes[1].NodeName, nodes[1].ParentID, *nodes[1].ParentNodeName},
		[]any{3, "Institutes Subcategory 1", 2, "Different Gene sets from Random Institute"})
	g0, g1 := *nodes[0].Genesets, *nodes[1].Genesets
	if len(g0) != 1 || len(g1) != 1 {
		t.Fatalf("gene sets %+v %+v", g0, g1)
	}
	// ACYLGLYCEROL: 4 negative scores, the 3rd largest (-0.442); ATP: 1 positive.
	wantEq(t, []any{g0[0].GenesetID, *g0[0].RepresentativeScore, *g0[0].RepresentativePvalue},
		[]any{"GO_ACYLGLYCEROL_HOMEOSTASIS", -0.442, 0.005})
	wantEq(t, []any{g1[0].GenesetID, *g1[0].RepresentativeScore}, []any{"GO_ATP_DEPENDENT_CHROMATIN_REMODELING", 0.5692})

	// Without thresholds every node keeps its gene sets, the gene sets of a
	// node ordered by name.
	post(t, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_gsva_scores&scoreThreshold=0&pvalueThreshold=1", nil, &nodes)
	got := map[int][]string{}
	for _, n := range nodes {
		for _, g := range *n.Genesets {
			got[n.NodeID] = append(got[n.NodeID], g.GenesetID)
		}
	}
	wantEq(t, got, map[int][]string{
		1: {"GO_CARDIAC_CHAMBER_DEVELOPMENT"},
		2: {"GO_ACYLGLYCEROL_HOMEOSTASIS", "GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS"},
		3: {"GO_ATP_DEPENDENT_CHROMATIN_REMODELING"},
		4: {"GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS"},
	})

	// ACYLGLYCEROL's scores: 0.230 (SH) and -0.059 (SE), -0.139, -0.442, -0.503.
	acyl := func(query string, body any) float64 {
		t.Helper()
		post(t, "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_gsva_scores&scoreThreshold=0&pvalueThreshold=1"+query, body, &nodes)
		for _, n := range nodes {
			if n.Genesets == nil {
				continue
			}
			for _, g := range *n.Genesets {
				if g.GenesetID == "GO_ACYLGLYCEROL_HOMEOSTASIS" {
					return *g.RepresentativeScore
				}
			}
		}
		t.Fatalf("%s: ACYLGLYCEROL not listed", query)
		return 0
	}
	wantEq(t, acyl("&percentile=100", nil), -0.5034)
	wantEq(t, acyl("&percentile=1", nil), 0.2301)
	wantEq(t, acyl("", []string{"TCGA-A1-A0SE-01", "TCGA-A1-A0SH-01"}), 0.2301)
	wantEq(t, acyl("&sampleListId=study_es_0_all", nil), -0.442)

	for path, want := range map[string]string{
		"?geneticProfileId=study_es_0_gsva_scores&percentile=101":    `{"message":"percentile must be less than or equal to 100"}`,
		"?geneticProfileId=study_es_0_gsva_scores&scoreThreshold=-1": `{"message":"scoreThreshold must be greater than or equal to 0"}`,
		"?geneticProfileId=no_such_profile":                          `{"message":"Molecular profile not found: no_such_profile"}`,
	} {
		resp, body := rawDo(t, http.MethodPost, "/api/geneset-hierarchy/fetch"+path, "")
		if string(body) != want || (resp.StatusCode != 400 && resp.StatusCode != 404) {
			t.Errorf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
}

// GenesetDataServiceImpl: per sample (by id), the values of each gene set in
// load order; GenesetMolecularDataDTO carries no gene set object or keys.
func TestContractGenesetGeneticDataShape(t *testing.T) {
	requireGenesets(t)
	var rows []map[string]any
	post(t, "/api/genetic-profiles/study_es_0_gsva_scores/geneset-genetic-data/fetch", map[string]any{
		"genesetIds": []string{"GO_CARDIAC_CHAMBER_DEVELOPMENT", "GO_ACYLGLYCEROL_HOMEOSTASIS"},
		"sampleIds":  []string{"TCGA-A2-A04U-01", "TCGA-A1-A0SB-01", "NO_SUCH_SAMPLE"},
	}, &rows)
	wantEq(t, rows, []map[string]any{
		{"geneticProfileId": "study_es_0_gsva_scores", "sampleId": "TCGA-A1-A0SB-01", "patientId": "TCGA-A1-A0SB", "studyId": "study_es_0",
			"value": "-0.1385", "genesetId": "GO_ACYLGLYCEROL_HOMEOSTASIS"},
		{"geneticProfileId": "study_es_0_gsva_scores", "sampleId": "TCGA-A1-A0SB-01", "patientId": "TCGA-A1-A0SB", "studyId": "study_es_0",
			"value": "0.0899", "genesetId": "GO_CARDIAC_CHAMBER_DEVELOPMENT"},
		{"geneticProfileId": "study_es_0_gsva_scores", "sampleId": "TCGA-A2-A04U-01", "patientId": "TCGA-A2-A04U", "studyId": "study_es_0",
			"value": "-0.5034", "genesetId": "GO_ACYLGLYCEROL_HOMEOSTASIS"},
		{"geneticProfileId": "study_es_0_gsva_scores", "sampleId": "TCGA-A2-A04U-01", "patientId": "TCGA-A2-A04U", "studyId": "study_es_0",
			"value": "-0.1033", "genesetId": "GO_CARDIAC_CHAMBER_DEVELOPMENT"},
	})
	resp, _ := rawDo(t, http.MethodPost, "/api/genetic-profiles/no_such_profile/geneset-genetic-data/fetch", `{}`)
	wantEq(t, resp.StatusCode, 404)
}
