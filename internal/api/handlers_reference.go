package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

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
// (like co-expression). The correlation is pushed into ClickHouse (spearmanSQL) so
// only genes above the threshold cross the wire. Shape: []GenesetCorrelation.
func (s *State) genesetExpressionCorrelationFetch(w http.ResponseWriter, r *http.Request) {
	genesetID := r.PathValue("genesetId")

	// Optional body: a custom list of sample ids to restrict the correlation to.
	var sampleIDs []string
	core.ReadBody(r, &sampleIDs)

	gsvaProfile := r.URL.Query().Get("geneticProfileId")
	sampleListID := r.URL.Query().Get("sampleListId")
	threshold := 0.3
	if t := r.URL.Query().Get("correlationThreshold"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil {
			threshold = v
		}
	}

	meta, ok := s.ProfileMeta(gsvaProfile)
	if !ok {
		core.WriteError(w, http.StatusNotFound, core.MsgMolecularProfileNotFound(gsvaProfile))
		return
	}
	if !strings.EqualFold(meta.AlterationType, "GENESET_SCORE") {
		core.WriteJSON(w, []GenesetCorrelation{})
		return
	}

	// Resolve the study's mRNA expression profiles: prefer a continuous profile
	// for the correlation and a z-score profile for the reported z-score id.
	type exprProfile struct {
		ID       string `json:"id"`
		StableID string `json:"stableId"`
		Datatype string `json:"datatype"`
	}
	exprRows, _ := ch.QueryP[exprProfile](s.CH,
		"SELECT molecular_profile_id AS id, profile_stable_id AS stableId, datatype "+
			"FROM molecular_profile WHERE study_id = {study:String} AND molecular_alteration_type = 'MRNA_EXPRESSION' "+
			"ORDER BY molecular_profile_id", ch.Params{"study": meta.StudyID})
	if len(exprRows) == 0 {
		core.WriteJSON(w, []GenesetCorrelation{})
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

	if s.hasGenesetRef() {
		s.genesetMemberCorrelations(w, r, meta, genesetID, sampleListID, sampleIDs, threshold, expr.ID, expr.StableID, zscore.ID)
		return
	}

	p := ch.Params{"study": meta.StudyID, "profile": meta.StableID, "geneset": genesetID, "exprProfile": expr.StableID}
	sampleFilter := ""
	if len(sampleIDs) > 0 {
		sampleFilter = " AND sample_id IN {samples:Array(String)}"
		p["samples"] = sampleIDs
	} else if sampleListID != "" {
		sampleFilter = " AND sample_id IN " + s.BySampleList(sampleListID).ListMembers(core.ParamsBinder(p))
	}

	querySub := fmt.Sprintf(
		"SELECT sample_id, toFloat64OrZero(value) AS qval FROM %s "+
			"WHERE study_id = {study:String} AND profile_stable_id = {profile:String} AND geneset_id = {geneset:String} "+
			"AND toFloat64OrNull(value) IS NOT NULL%s",
		genesetScoreSource(s), sampleFilter)

	type corrRow struct {
		Entrez *int64   `json:"entity"`
		Hugo   string   `json:"hugo"`
		N      int      `json:"n"`
		SC     *float64 `json:"sc"`
	}
	rows, err := ch.QueryP[corrRow](s.CH, spearmanSQL(fmt.Sprintf(
		"SELECT toInt64OrNull(b.entrez_gene_id) AS entity, b.hugo_symbol AS hugo, "+
			"toFloat64OrZero(b.value) AS x, q.qval AS y "+
			"FROM %s AS b "+
			"INNER JOIN (%s) AS q USING (sample_id) "+
			"WHERE b.study_id = {study:String} AND b.profile_stable_id = {exprProfile:String} "+
			"AND toFloat64OrNull(b.value) IS NOT NULL",
		s.RawSource("mrna_expression_matrix", geneMatrixCols...), querySub),
		"1", threshold)+" ORDER BY sc DESC", p)
	if err != nil {
		core.Fail(w, r, err)
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
	core.WriteJSON(w, out)
}
