package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

type geneRow struct {
	EntrezGeneID    int64  `json:"entrezGeneId"`
	HugoGeneSymbol  string `json:"hugoGeneSymbol"`
	Type            string `json:"type"`
	GeneticEntityID int64  `json:"geneticEntityId"`
}

// genesFetch resolves gene identifiers against the reference. Body is a JSON
// array of entrez ids (numbers) or Hugo symbols (strings); `geneIdType` query
// param (ENTREZ_GENE_ID / HUGO_GENE_SYMBOL) hints which, but we detect per item.
func (s *State) genesFetch(w http.ResponseWriter, r *http.Request) {
	var raw []json.RawMessage
	readBody(r, &raw)
	if len(raw) == 0 {
		writeJSON(w, []geneRow{})
		return
	}
	var entrez, symbols []string
	for _, m := range raw {
		str := strings.Trim(string(m), `"`)
		if _, err := strconv.ParseInt(str, 10, 64); err == nil {
			entrez = append(entrez, str)
		} else {
			symbols = append(symbols, str)
		}
	}
	var conds []string
	if len(entrez) > 0 {
		conds = append(conds, fmt.Sprintf("entrez_gene_id IN (%s)", strings.Join(entrez, ", ")))
	}
	if len(symbols) > 0 {
		up := make([]string, len(symbols))
		for i, sym := range symbols {
			up[i] = "'" + sqlLit(strings.ToUpper(sym)) + "'"
		}
		conds = append(conds, fmt.Sprintf("upper(hugo_gene_symbol) IN (%s)", strings.Join(up, ", ")))
	}
	rows, err := ch.Query[geneRow](s.CH, fmt.Sprintf(
		"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type, "+
			"genetic_entity_id AS geneticEntityId FROM cbioportal_ref.gene WHERE %s",
		strings.Join(conds, " OR ")))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, rows)
}

// gene by id (entrez or hugo).
func (s *State) gene(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cond := fmt.Sprintf("upper(hugo_gene_symbol) = '%s'", sqlLit(strings.ToUpper(id)))
	if _, err := strconv.ParseInt(id, 10, 64); err == nil {
		cond = "entrez_gene_id = " + id
	}
	rows, err := ch.Query[geneRow](s.CH, fmt.Sprintf(
		"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type, genetic_entity_id AS geneticEntityId FROM cbioportal_ref.gene WHERE %s LIMIT 1", cond))
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "gene not found", http.StatusNotFound)
		return
	}
	writeJSON(w, rows[0])
}
