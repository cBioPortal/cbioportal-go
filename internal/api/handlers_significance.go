package api

import (
	"net/http"
	"strconv"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// routeSignificance registers the per-study GISTIC and MutSig endpoints
// (SignificantCopyNumberRegionController / SignificantlyMutatedGenesController).
func (s *State) routeSignificance(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/studies/{studyId}/significant-copy-number-regions", s.significantCopyNumberRegions)
	mux.HandleFunc("GET /api/studies/{studyId}/significantly-mutated-genes", s.significantlyMutatedGenes)
}

// Gistic is the Gistic model as serialized through GisticMixin (gisticRoiId
// ignored, cancerStudyId -> studyId). Genes are omitted for the ID projection.
type Gistic struct {
	StudyID       string         `json:"studyId"`
	Chromosome    int32          `json:"chromosome"`
	Cytoband      string         `json:"cytoband"`
	WidePeakStart int64          `json:"widePeakStart"`
	WidePeakEnd   int64          `json:"widePeakEnd"`
	QValue        float64        `json:"qValue"`
	Amp           bool           `json:"amp"`
	Genes         []GisticToGene `json:"genes,omitempty"`
}

// GisticToGene is a gene of a GISTIC region (GisticToGeneMixin drops the
// region id).
type GisticToGene struct {
	EntrezGeneID   int64  `json:"entrezGeneId"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
}

// MutSig is the MutSig model as serialized through MutSigMixin (internal ids
// and numbasescovered ignored, nummutations -> numberOfMutations).
type MutSig struct {
	StudyID           string  `json:"studyId"`
	EntrezGeneID      int64   `json:"entrezGeneId"`
	HugoGeneSymbol    string  `json:"hugoGeneSymbol"`
	Rank              int32   `json:"rank"`
	NumberOfMutations int32   `json:"numberOfMutations"`
	PValue            float64 `json:"pValue"`
	QValue            float64 `json:"qValue"`
}

// exactFloat parses a decimal as written (correctly rounded), falling back to
// the value ClickHouse parsed.
func exactFloat(text string, parsed float64) float64 {
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		return f
	}
	return parsed
}

// importerFloat is a GISTIC / MutSig p- or q-value as cbioportal-core stores
// it: parsed as a Java float (GisticReader, MutSigReader), so it carries
// float precision and values below its range read 0.
func importerFloat(text string, parsed float64) float64 {
	return float64(float32(exactFloat(text, parsed)))
}

// writeTotalCount answers a META projection: the total-count header, no body.
func (s *State) writeTotalCount(w http.ResponseWriter, sql string, p ch.Params) {
	rows, err := queryView[struct {
		N uint64 `json:"n"`
	}](s.CH, sql, p)
	if err != nil {
		fail(w, err)
		return
	}
	var n uint64
	if len(rows) > 0 {
		n = rows[0].N
	}
	w.Header().Set("total-count", strconv.FormatUint(n, 10))
	w.WriteHeader(http.StatusOK)
}

var gisticSort = map[string]string{
	"chromosome": "chromosome", "cytoband": "cytoband", "widePeakStart": "wide_peak_start",
	"widePeakEnd": "wide_peak_end", "qValue": "q_value", "amp": "amp",
}

// significantCopyNumberRegions — GET /api/studies/{studyId}/significant-copy-number-regions.
func (s *State) significantCopyNumberRegions(w http.ResponseWriter, r *http.Request) {
	studyID := r.PathValue("studyId")
	if !s.requireStudy(w, studyID) {
		return
	}
	where := "study_id = {study:String}"
	p := ch.Params{"study": studyID}
	projection := projectionParam(r)
	if projection == "META" {
		s.writeTotalCount(w, "SELECT count() AS n FROM gistic WHERE "+where, p)
		return
	}
	type regionRow struct {
		ID            uint64  `json:"gistic_roi_id"`
		Chromosome    int32   `json:"chromosome"`
		Cytoband      string  `json:"cytoband"`
		WidePeakStart int64   `json:"wide_peak_start"`
		WidePeakEnd   int64   `json:"wide_peak_end"`
		QValue        float64 `json:"q_value"`
		QValueText    string  `json:"q_value_text"`
		Amp           uint8   `json:"amp"`
	}
	regions, err := queryView[regionRow](s.CH,
		"SELECT gistic_roi_id, chromosome, cytoband, wide_peak_start, wide_peak_end, q_value, q_value_text, amp FROM gistic WHERE "+where+
			pagingClause(r, gisticSort, "amp DESC, chromosome ASC, wide_peak_start ASC, gistic_roi_id ASC"), p)
	if err != nil {
		fail(w, err)
		return
	}
	genes := map[uint64][]GisticToGene{}
	if projection != "ID" && len(regions) > 0 {
		type geneRow struct {
			ID uint64 `json:"gistic_roi_id"`
			GisticToGene
		}
		rows, err := queryView[geneRow](s.CH,
			"SELECT gistic_roi_id, entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol FROM gistic_to_gene WHERE "+
				where+" ORDER BY gistic_roi_id, gene_index", p)
		if err != nil {
			fail(w, err)
			return
		}
		for _, g := range rows {
			genes[g.ID] = append(genes[g.ID], g.GisticToGene)
		}
	}
	out := make([]Gistic, len(regions))
	for i, g := range regions {
		out[i] = Gistic{
			StudyID: studyID, Chromosome: g.Chromosome, Cytoband: g.Cytoband,
			WidePeakStart: g.WidePeakStart, WidePeakEnd: g.WidePeakEnd, QValue: importerFloat(g.QValueText, g.QValue), Amp: g.Amp != 0,
		}
		if projection != "ID" {
			out[i].Genes = genes[g.ID]
			if out[i].Genes == nil {
				out[i].Genes = []GisticToGene{}
			}
		}
	}
	writeJSON(w, out)
}

var mutSigSort = map[string]string{
	"entrezGeneId": "entrez_gene_id", "hugoGeneSymbol": "hugo_gene_symbol", "rank": "`rank`",
	"numberOfMutations": "num_mutations", "pValue": "p_value", "qValue": "q_value",
}

// significantlyMutatedGenes — GET /api/studies/{studyId}/significantly-mutated-genes.
func (s *State) significantlyMutatedGenes(w http.ResponseWriter, r *http.Request) {
	studyID := r.PathValue("studyId")
	if !s.requireStudy(w, studyID) {
		return
	}
	where := "study_id = {study:String}"
	p := ch.Params{"study": studyID}
	if projectionParam(r) == "META" {
		s.writeTotalCount(w, "SELECT count() AS n FROM mutsig WHERE "+where, p)
		return
	}
	type mutSigRow struct {
		MutSig
		PValueText string `json:"pValueText"`
		QValueText string `json:"qValueText"`
	}
	rows, err := queryView[mutSigRow](s.CH,
		"SELECT study_id AS studyId, entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, `rank` AS rank, "+
			"num_mutations AS numberOfMutations, p_value AS pValue, q_value AS qValue, "+
			"p_value_text AS pValueText, q_value_text AS qValueText FROM mutsig WHERE "+where+
			pagingClause(r, mutSigSort, "entrez_gene_id ASC"), p)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]MutSig, len(rows))
	for i, row := range rows {
		out[i] = row.MutSig
		out[i].PValue = importerFloat(row.PValueText, row.PValue)
		out[i].QValue = importerFloat(row.QValueText, row.QValue)
	}
	writeJSON(w, out)
}
