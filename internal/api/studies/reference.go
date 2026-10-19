package studies

import (
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// routeReference registers gene-alias resolution and the
// reference-genome-gene endpoints.
func (s Server) routeReference(mux *http.ServeMux) {
	// Gene aliases + reference genome genes.
	mux.HandleFunc("GET /api/genes/{geneId}/aliases", s.geneAliases)
	mux.HandleFunc("GET /api/reference-genome-genes/{genomeName}", s.referenceGenomeGenes)
	mux.HandleFunc("GET /api/reference-genome-genes/{genomeName}/{geneId}", s.referenceGenomeGeneGet)
	mux.HandleFunc("POST /api/reference-genome-genes/{genomeName}/fetch", s.referenceGenomeGenesFetch)
}

// ---------- 5. GET /api/genes/{geneId}/aliases ----------

// geneAliases returns the alias symbols for a gene. The geneId may be an Entrez
// id or a Hugo symbol; it is resolved via cbioportal_ref.gene. Shape: []string.
func (s Server) geneAliases(w http.ResponseWriter, r *http.Request) {
	geneID := r.PathValue("geneId")
	entrez, _, ok, err := s.resolveRefGene(geneID)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if !ok {
		core.WriteJSON(w, []string{})
		return
	}
	type aliasRow struct {
		Alias string `json:"alias"`
	}
	rows, err := ch.Query[aliasRow](s.CH, fmt.Sprintf(
		"SELECT gene_alias AS alias FROM cbioportal_ref.gene_alias WHERE entrez_gene_id = %d ORDER BY gene_alias", entrez))
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Alias)
	}
	core.WriteJSON(w, out)
}

// resolveRefGene resolves a gene identifier (Entrez id or Hugo symbol) to its
// canonical (entrez, hugo) pair from cbioportal_ref.gene.
func (s Server) resolveRefGene(geneID string) (int64, string, bool, error) {
	var where string
	p := ch.Params{}
	if n, err := strconv.ParseInt(geneID, 10, 64); err == nil {
		where = fmt.Sprintf("entrez_gene_id = %d", n)
	} else {
		where = "hugo_gene_symbol = {gene:String}"
		p["gene"] = geneID
	}
	type geneRow struct {
		Entrez int64  `json:"entrez"`
		Hugo   string `json:"hugo"`
	}
	rows, err := ch.QueryP[geneRow](s.CH,
		"SELECT entrez_gene_id AS entrez, hugo_gene_symbol AS hugo FROM cbioportal_ref.gene WHERE "+where+" LIMIT 1", p)
	if err != nil || len(rows) == 0 {
		return 0, "", false, err
	}
	return rows[0].Entrez, rows[0].Hugo, true, nil
}

// ---------- 6/7. reference-genome-genes ----------

type ReferenceGenomeGene struct {
	ReferenceGenomeID int64  `json:"referenceGenomeId"`
	EntrezGeneID      int64  `json:"entrezGeneId"`
	HugoGeneSymbol    string `json:"hugoGeneSymbol"`
	Chromosome        string `json:"chromosome"`
	Cytoband          string `json:"cytoband"`
	Start             int64  `json:"start"`
	End               int64  `json:"end"`
}

// referenceGenomeIDFor maps a genome name to the numeric reference-genome id used
// by cBioPortal (GRCh37/hg19 -> 1, GRCh38/hg38 -> 2), for reference DBs loaded
// without the reference_genome table.
func referenceGenomeIDFor(name string) int64 {
	switch strings.ToLower(name) {
	case "hg38", "grch38":
		return 2
	default:
		return 1
	}
}

// refGenomeGenes returns the genes of a reference genome with their location,
// as ReferenceGenomeGeneMapper does: reference_genome_gene joined to gene and
// to the reference_genome whose name (hg19, hg38, mm10; case-insensitive)
// matches. where filters on gene (aliased g), with placeholder values in p.
// Reference DBs without the reference_genome tables yield every gene with an
// empty location.
func (s Server) refGenomeGenes(genomeName, where string, p ch.Params) ([]ReferenceGenomeGene, error) {
	withGenome := ch.Params{"genome": genomeName}
	maps.Copy(withGenome, p)
	type geneRow struct {
		Entrez     int64  `json:"entrez"`
		Hugo       string `json:"hugo"`
		GenomeID   int64  `json:"genomeId"`
		Chromosome string `json:"chromosome"`
		Cytoband   string `json:"cytoband"`
		Start      int64  `json:"start"`
		End        int64  `json:"end"`
	}
	rows, err := ch.QueryP[geneRow](s.CH, fmt.Sprintf(
		"SELECT g.entrez_gene_id AS entrez, g.hugo_gene_symbol AS hugo, toInt64(rg.reference_genome_id) AS genomeId, "+
			"ifNull(rgg.chr, '') AS chromosome, ifNull(rgg.cytoband, '') AS cytoband, ifNull(rgg.start, 0) AS start, ifNull(rgg.end, 0) AS end "+
			"FROM cbioportal_ref.reference_genome_gene rgg "+
			"INNER JOIN cbioportal_ref.reference_genome rg ON rg.reference_genome_id = rgg.reference_genome_id "+
			"INNER JOIN cbioportal_ref.gene g ON g.entrez_gene_id = rgg.entrez_gene_id "+
			"WHERE lower(rg.name) = lower({genome:String}) AND (%s) ORDER BY g.entrez_gene_id", where), withGenome)
	if err != nil {
		rows, err = ch.QueryP[geneRow](s.CH, fmt.Sprintf(
			"SELECT g.entrez_gene_id AS entrez, g.hugo_gene_symbol AS hugo, toInt64(%d) AS genomeId "+
				"FROM cbioportal_ref.gene g WHERE %s ORDER BY g.entrez_gene_id", referenceGenomeIDFor(genomeName), where), p)
		if err != nil {
			return nil, err
		}
	}
	out := make([]ReferenceGenomeGene, 0, len(rows))
	for _, row := range rows {
		out = append(out, ReferenceGenomeGene{
			EntrezGeneID:      row.Entrez,
			HugoGeneSymbol:    row.Hugo,
			ReferenceGenomeID: row.GenomeID,
			Chromosome:        row.Chromosome,
			Cytoband:          row.Cytoband,
			Start:             row.Start,
			End:               row.End,
		})
	}
	return out, nil
}

// referenceGenomeGenes handles GET /api/reference-genome-genes/{genomeName}:
// every gene of the genome.
func (s Server) referenceGenomeGenes(w http.ResponseWriter, r *http.Request) {
	out, err := s.refGenomeGenes(r.PathValue("genomeName"), "1", nil)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, out)
}

// referenceGenomeGeneGet handles GET /api/reference-genome-genes/{genomeName}/{geneId}.
// geneId is an Entrez id; an unknown gene is 404.
func (s Server) referenceGenomeGeneGet(w http.ResponseWriter, r *http.Request) {
	entrez, err := strconv.ParseInt(r.PathValue("geneId"), 10, 32)
	if err != nil {
		http.Error(w, "geneId must be an integer", http.StatusBadRequest)
		return
	}
	out, err := s.refGenomeGenes(r.PathValue("genomeName"), fmt.Sprintf("g.entrez_gene_id = %d", entrez), nil)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(out) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	core.WriteJSON(w, out[0])
}

// referenceGenomeGenesFetch handles POST /api/reference-genome-genes/{genomeName}/fetch.
// As in ReferenceGenomeGeneController, the body is read as Entrez ids when its
// first entry is an integer, else as Hugo symbols matched with their exact
// case; the list holds 1 to 100000 ids (@Size).
func (s Server) referenceGenomeGenesFetch(w http.ResponseWriter, r *http.Request) {
	var geneIDs []string
	core.ReadBody(r, &geneIDs)
	if len(geneIDs) == 0 || len(geneIDs) > 100000 {
		core.WriteError(w, http.StatusBadRequest, "geneIds size must be between 1 and 100000")
		return
	}
	var where string
	var p ch.Params
	if _, err := strconv.ParseInt(geneIDs[0], 10, 32); err == nil {
		ids := make([]int64, 0, len(geneIDs))
		for _, id := range geneIDs {
			n, err := strconv.ParseInt(id, 10, 32)
			if err != nil {
				http.Error(w, "gene ids must all be integers", http.StatusBadRequest)
				return
			}
			ids = append(ids, n)
		}
		where = fmt.Sprintf("g.entrez_gene_id IN (%s)", core.IntListSQL(ids))
	} else {
		where = "g.hugo_gene_symbol IN {genes:Array(String)}"
		p = ch.Params{"genes": geneIDs}
	}
	out, err := s.refGenomeGenes(r.PathValue("genomeName"), where, p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, out)
}
