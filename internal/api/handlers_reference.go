package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeReference registers geneset hierarchy/version/correlation, gene-alias
// resolution, and reference-genome-gene endpoints. Existing geneset endpoints
// (GET/POST /api/genesets/fetch, GET /api/genesets/{genesetId}, and
// geneset-genetic-data/fetch) are registered elsewhere and are not re-added.
func (s *State) routeReference(mux *http.ServeMux) {
	// Genesets.
	mux.HandleFunc("GET /api/genesets", s.genesetsList)
	mux.HandleFunc("GET /api/genesets/version", s.genesetVersion)
	mux.HandleFunc("POST /api/geneset-hierarchy/fetch", s.genesetHierarchyFetch)
	mux.HandleFunc("POST /api/genesets/{genesetId}/expression-correlation/fetch", s.genesetExpressionCorrelationFetch)

	// Gene aliases + reference genome genes.
	mux.HandleFunc("GET /api/genes/{geneId}/aliases", s.geneAliases)
	mux.HandleFunc("GET /api/reference-genome-genes/{genomeName}", s.referenceGenomeGenes)
	mux.HandleFunc("GET /api/reference-genome-genes/{genomeName}/{geneId}", s.referenceGenomeGeneGet)
	mux.HandleFunc("POST /api/reference-genome-genes/{genomeName}/fetch", s.referenceGenomeGenesFetch)
}

// genesetGenesetVersion is the GSVA/MSigDB collection version reported to the
// frontend. No versioned geneset reference is loaded, so this is a static tag.
const genesetGenesetVersion = "msigdb.v7.5.1"

// ---------- 1. GET /api/genesets ----------

// genesetsList returns all genesets synthesized from the distinct geneset ids in
// the GSVA score matrix (no separate geneset reference table exists). Shape: []Geneset.
func (s *State) genesetsList(w http.ResponseWriter, r *http.Request) {
	out, err := s.genesetsForIDs(nil)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// ---------- 2. GET /api/genesets/version ----------

// genesetVersion returns the geneset collection version as a bare JSON string,
// matching the client's Promise<string> contract.
func (s *State) genesetVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, genesetGenesetVersion)
}

// ---------- 3. POST /api/geneset-hierarchy/fetch ----------

type GenesetHierarchyInfo struct {
	Genesets       []Geneset `json:"genesets"`
	NodeID         int64     `json:"nodeId"`
	NodeName       string    `json:"nodeName"`
	ParentID       int64     `json:"parentId"`
	ParentNodeName string    `json:"parentNodeName"`
}

// genesetHierarchyFetch returns the geneset hierarchy tree for the GSVA score
// profile given by the geneticProfileId query param, over the samples in the
// optional body (a bare list of sample ids) or else the sampleListId sample
// list (else every sample). No hierarchy metadata (GMT node structure) is
// loaded, so the profile's genesets are exposed under a single root node.
// Shape: []GenesetHierarchyInfo.
func (s *State) genesetHierarchyFetch(w http.ResponseWriter, r *http.Request) {
	var sampleIDs []string
	readBody(r, &sampleIDs)
	q := r.URL.Query()
	meta, ok := s.profileMeta(q.Get("geneticProfileId"))
	if !ok || !strings.EqualFold(meta.AlterationType, "GENESET_SCORE") {
		writeJSON(w, []GenesetHierarchyInfo{})
		return
	}
	where := fmt.Sprintf("study_id = '%s' AND profile_stable_id = '%s'", sqlLit(meta.StudyID), sqlLit(meta.StableID))
	if len(sampleIDs) > 0 {
		where += fmt.Sprintf(" AND sample_id IN (%s)", inList(sampleIDs))
	} else {
		where += sampleListClause("sample_id", q.Get("sampleListId"))
	}
	type row struct {
		GenesetID string `json:"genesetId"`
	}
	rows, err := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT DISTINCT geneset_id AS genesetId FROM cbioportal_raw.geneset_score_matrix WHERE %s ORDER BY geneset_id", where))
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		writeJSON(w, []GenesetHierarchyInfo{})
		return
	}
	genesets := make([]Geneset, len(rows))
	for i, r := range rows {
		genesets[i] = Geneset{GenesetID: r.GenesetID, Name: r.GenesetID, Description: r.GenesetID}
	}
	writeJSON(w, []GenesetHierarchyInfo{{
		Genesets:       genesets,
		NodeID:         1,
		NodeName:       "Gene sets",
		ParentID:       0,
		ParentNodeName: "",
	}})
}

// ---------- 4. POST /api/genesets/{genesetId}/expression-correlation/fetch ----------

type GenesetCorrelation struct {
	CorrelationValue           float64 `json:"correlationValue"`
	EntrezGeneID               int64   `json:"entrezGeneId"`
	ExpressionGeneticProfileID string  `json:"expressionGeneticProfileId"`
	HugoGeneSymbol             string  `json:"hugoGeneSymbol"`
	ZScoreGeneticProfileID     string  `json:"zScoreGeneticProfileId"`
}

// genesetExpressionCorrelationFetch computes, for the given geneset, the Spearman
// correlation of its per-sample GSVA score against each gene's mRNA expression
// (like co-expression). The correlation is pushed into ClickHouse via rankCorr so
// only genes above the threshold cross the wire. Shape: []GenesetCorrelation.
func (s *State) genesetExpressionCorrelationFetch(w http.ResponseWriter, r *http.Request) {
	genesetID := r.PathValue("genesetId")

	// Optional body: a custom list of sample ids to restrict the correlation to.
	var sampleIDs []string
	readBody(r, &sampleIDs)

	gsvaProfile := r.URL.Query().Get("geneticProfileId")
	sampleListID := r.URL.Query().Get("sampleListId")
	threshold := 0.3
	if t := r.URL.Query().Get("correlationThreshold"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil {
			threshold = v
		}
	}

	meta, ok := s.profileMeta(gsvaProfile)
	if !ok || !strings.EqualFold(meta.AlterationType, "GENESET_SCORE") {
		writeJSON(w, []GenesetCorrelation{})
		return
	}

	// Resolve the study's mRNA expression profiles: prefer a continuous profile
	// for the correlation and a z-score profile for the reported z-score id.
	type exprProfile struct {
		ID       string `json:"id"`
		StableID string `json:"stableId"`
		Datatype string `json:"datatype"`
	}
	exprRows, _ := ch.Query[exprProfile](s.CH, fmt.Sprintf(
		"SELECT molecular_profile_id AS id, profile_stable_id AS stableId, datatype "+
			"FROM molecular_profile WHERE study_id = '%s' AND molecular_alteration_type = 'MRNA_EXPRESSION' "+
			"ORDER BY molecular_profile_id", sqlLit(meta.StudyID)))
	if len(exprRows) == 0 {
		writeJSON(w, []GenesetCorrelation{})
		return
	}
	expr := exprRows[0]
	zscore := exprRows[0]
	for _, p := range exprRows {
		if strings.Contains(strings.ToUpper(p.Datatype), "CONTINUOUS") {
			expr = p
		}
		if strings.Contains(strings.ToUpper(p.Datatype), "Z-SCORE") {
			zscore = p
		}
	}

	sampleFilter := ""
	if len(sampleIDs) > 0 {
		sampleFilter = fmt.Sprintf(" AND sample_id IN (%s)", inList(sampleIDs))
	} else if sampleListID != "" {
		sampleFilter = fmt.Sprintf(" AND sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(sampleListID))
	}

	querySub := fmt.Sprintf(
		"SELECT sample_id, toFloat64OrZero(value) AS qval FROM cbioportal_raw.geneset_score_matrix "+
			"WHERE study_id = '%s' AND profile_stable_id = '%s' AND geneset_id = '%s' "+
			"AND toFloat64OrNull(value) IS NOT NULL%s",
		sqlLit(meta.StudyID), sqlLit(meta.StableID), sqlLit(genesetID), sampleFilter)

	type corrRow struct {
		Entrez *int64   `json:"entrez"`
		Hugo   string   `json:"hugo"`
		N      int      `json:"n"`
		SC     *float64 `json:"sc"`
	}
	rows, err := ch.Query[corrRow](s.CH, fmt.Sprintf(
		"SELECT toInt64OrNull(b.entrez_gene_id) AS entrez, any(b.hugo_symbol) AS hugo, "+
			"toInt64(count()) AS n, rankCorr(toFloat64OrZero(b.value), q.qval) AS sc "+
			"FROM cbioportal_raw.mrna_expression_matrix AS b "+
			"INNER JOIN (%s) AS q USING (sample_id) "+
			"WHERE b.study_id = '%s' AND b.profile_stable_id = '%s' "+
			"AND toFloat64OrNull(b.value) IS NOT NULL "+
			"GROUP BY entrez "+
			"HAVING n > 2 AND isNotNull(sc) AND abs(sc) >= %g "+
			"ORDER BY sc DESC",
		querySub, sqlLit(meta.StudyID), sqlLit(expr.StableID), threshold))
	if err != nil {
		fail(w, err)
		return
	}

	out := []GenesetCorrelation{}
	for _, row := range rows {
		if row.SC == nil {
			continue
		}
		gc := GenesetCorrelation{
			CorrelationValue:           *row.SC,
			HugoGeneSymbol:             row.Hugo,
			ExpressionGeneticProfileID: expr.ID,
			ZScoreGeneticProfileID:     zscore.ID,
		}
		if row.Entrez != nil {
			gc.EntrezGeneID = *row.Entrez
		}
		out = append(out, gc)
	}
	writeJSON(w, out)
}

// ---------- 5. GET /api/genes/{geneId}/aliases ----------

// geneAliases returns the alias symbols for a gene. The geneId may be an Entrez
// id or a Hugo symbol; it is resolved via cbioportal_ref.gene. Shape: []string.
func (s *State) geneAliases(w http.ResponseWriter, r *http.Request) {
	geneID := r.PathValue("geneId")
	entrez, _, ok := s.resolveRefGene(geneID)
	if !ok {
		writeJSON(w, []string{})
		return
	}
	type aliasRow struct {
		Alias string `json:"alias"`
	}
	rows, err := ch.Query[aliasRow](s.CH, fmt.Sprintf(
		"SELECT gene_alias AS alias FROM cbioportal_ref.gene_alias WHERE entrez_gene_id = %d ORDER BY gene_alias", entrez))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Alias)
	}
	writeJSON(w, out)
}

// resolveRefGene resolves a gene identifier (Entrez id or Hugo symbol) to its
// canonical (entrez, hugo) pair from cbioportal_ref.gene.
func (s *State) resolveRefGene(geneID string) (int64, string, bool) {
	var where string
	if n, err := strconv.ParseInt(geneID, 10, 64); err == nil {
		where = fmt.Sprintf("entrez_gene_id = %d", n)
	} else {
		where = fmt.Sprintf("hugo_gene_symbol = '%s'", sqlLit(geneID))
	}
	type geneRow struct {
		Entrez int64  `json:"entrez"`
		Hugo   string `json:"hugo"`
	}
	rows, err := ch.Query[geneRow](s.CH, fmt.Sprintf(
		"SELECT entrez_gene_id AS entrez, hugo_gene_symbol AS hugo FROM cbioportal_ref.gene WHERE %s LIMIT 1", where))
	if err != nil || len(rows) == 0 {
		return 0, "", false
	}
	return rows[0].Entrez, rows[0].Hugo, true
}

// ---------- 6/7. reference-genome-genes ----------

type ReferenceGenomeGene struct {
	Chromosome        string `json:"chromosome"`
	Cytoband          string `json:"cytoband"`
	End               int64  `json:"end"`
	EntrezGeneID      int64  `json:"entrezGeneId"`
	HugoGeneSymbol    string `json:"hugoGeneSymbol"`
	ReferenceGenomeID int64  `json:"referenceGenomeId"`
	Start             int64  `json:"start"`
}

// referenceGenomeIDFor maps a genome name to the numeric reference-genome id used
// by cBioPortal (GRCh37/hg19 -> 1, GRCh38/hg38 -> 2).
func referenceGenomeIDFor(name string) int64 {
	switch strings.ToLower(name) {
	case "hg38", "grch38":
		return 2
	default:
		return 1
	}
}

// refGenomeGenesWhere loads genes from cbioportal_ref.gene into ReferenceGenomeGene
// rows. Position columns (chromosome/cytoband/start/end) are not present in the
// reference table, so they are returned empty. The where clause selects which genes.
func (s *State) refGenomeGenes(genomeName, where string) ([]ReferenceGenomeGene, error) {
	refID := referenceGenomeIDFor(genomeName)
	type geneRow struct {
		Entrez int64  `json:"entrez"`
		Hugo   string `json:"hugo"`
	}
	rows, err := ch.Query[geneRow](s.CH, fmt.Sprintf(
		"SELECT entrez_gene_id AS entrez, hugo_gene_symbol AS hugo FROM cbioportal_ref.gene WHERE %s ORDER BY entrez_gene_id", where))
	if err != nil {
		return nil, err
	}
	out := make([]ReferenceGenomeGene, 0, len(rows))
	for _, row := range rows {
		out = append(out, ReferenceGenomeGene{
			EntrezGeneID:      row.Entrez,
			HugoGeneSymbol:    row.Hugo,
			ReferenceGenomeID: refID,
		})
	}
	return out, nil
}

// referenceGenomeGenes handles GET /api/reference-genome-genes/{genomeName}.
func (s *State) referenceGenomeGenes(w http.ResponseWriter, r *http.Request) {
	genomeName := r.PathValue("genomeName")
	out, err := s.refGenomeGenes(genomeName, "type = 'protein-coding'")
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// referenceGenomeGeneGet handles GET /api/reference-genome-genes/{genomeName}/{geneId}.
func (s *State) referenceGenomeGeneGet(w http.ResponseWriter, r *http.Request) {
	genomeName := r.PathValue("genomeName")
	entrez, _, ok := s.resolveRefGene(r.PathValue("geneId"))
	if !ok {
		writeJSON(w, ReferenceGenomeGene{ReferenceGenomeID: referenceGenomeIDFor(genomeName)})
		return
	}
	out, err := s.refGenomeGenes(genomeName, fmt.Sprintf("entrez_gene_id = %d", entrez))
	if err != nil {
		fail(w, err)
		return
	}
	if len(out) == 0 {
		writeJSON(w, ReferenceGenomeGene{ReferenceGenomeID: referenceGenomeIDFor(genomeName)})
		return
	}
	writeJSON(w, out[0])
}

// referenceGenomeGenesFetch handles POST /api/reference-genome-genes/{genomeName}/fetch.
// Body is a JSON array of gene ids (Entrez ids or Hugo symbols).
func (s *State) referenceGenomeGenesFetch(w http.ResponseWriter, r *http.Request) {
	genomeName := r.PathValue("genomeName")
	var geneIDs []string
	readBody(r, &geneIDs)
	if len(geneIDs) == 0 {
		writeJSON(w, []ReferenceGenomeGene{})
		return
	}

	var entrez, hugo []string
	for _, id := range geneIDs {
		if _, err := strconv.ParseInt(id, 10, 64); err == nil {
			entrez = append(entrez, id)
		} else {
			hugo = append(hugo, id)
		}
	}
	var clauses []string
	if len(entrez) > 0 {
		clauses = append(clauses, fmt.Sprintf("toString(entrez_gene_id) IN (%s)", inList(entrez)))
	}
	if len(hugo) > 0 {
		clauses = append(clauses, fmt.Sprintf("hugo_gene_symbol IN (%s)", inList(hugo)))
	}
	out, err := s.refGenomeGenes(genomeName, strings.Join(clauses, " OR "))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}
