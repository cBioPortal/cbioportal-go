package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// geneRow is the legacy Gene model as GeneMapper selects it: the genetic
// entity id is never written. The ID projection drops type (javaContract).
type geneRow struct {
	EntrezGeneID   int64  `json:"entrezGeneId"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
	Type           string `json:"type"`
}

// genesFetch resolves gene identifiers against the reference. Body is a JSON
// array of entrez ids (numbers) or Hugo symbols (strings); `geneIdType` query
// param (ENTREZ_GENE_ID / HUGO_GENE_SYMBOL) hints which, but we detect per
// item. Unknown identifiers are skipped.
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
	p := ch.Params{}
	if len(entrez) > 0 {
		conds = append(conds, fmt.Sprintf("entrez_gene_id IN (%s)", strings.Join(entrez, ", ")))
	}
	if len(symbols) > 0 {
		up := make([]string, len(symbols))
		for i, sym := range symbols {
			up[i] = strings.ToUpper(sym)
		}
		conds = append(conds, "upper(hugo_gene_symbol) IN {symbols:Array(String)}")
		p["symbols"] = up
	}
	rows, err := ch.QueryP[geneRow](s.CH, fmt.Sprintf(
		"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type "+
			"FROM cbioportal_ref.gene WHERE %s",
		strings.Join(conds, " OR ")), p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, rows)
}

// gene by id (entrez or hugo).
func (s *State) gene(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cond := "upper(hugo_gene_symbol) = {symbol:String}"
	p := ch.Params{"symbol": strings.ToUpper(id)}
	if _, err := strconv.ParseInt(id, 10, 64); err == nil {
		cond = "entrez_gene_id = " + id
	}
	rows, err := ch.QueryP[geneRow](s.CH,
		"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type FROM cbioportal_ref.gene WHERE "+cond+" LIMIT 1", p)
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusNotFound, msgGeneNotFound(id))
		return
	}
	writeJSON(w, rows[0])
}
