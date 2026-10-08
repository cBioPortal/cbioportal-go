package studies

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// GeneRow is the legacy Gene model as GeneMapper selects it: the genetic
// entity id is never written. The ID projection drops type (javaContract).
type GeneRow struct {
	EntrezGeneID   int64  `json:"entrezGeneId"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
	Type           string `json:"type"`
}

// GenesFetch resolves gene identifiers against the reference. Body is a JSON
// array of entrez ids (numbers) or Hugo symbols (strings); `geneIdType` query
// param (ENTREZ_GENE_ID / HUGO_GENE_SYMBOL) hints which, but we detect per
// item. Unknown identifiers are skipped.
func (s Server) GenesFetch(w http.ResponseWriter, r *http.Request) {
	var raw []json.RawMessage
	core.ReadBody(r, &raw)
	if len(raw) == 0 {
		core.WriteJSON(w, []GeneRow{})
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
	rows, err := ch.QueryP[GeneRow](s.CH, fmt.Sprintf(
		"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type "+
			"FROM cbioportal_ref.gene WHERE %s",
		strings.Join(conds, " OR ")), p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, rows)
}

// Gene by id (entrez or hugo).
func (s Server) Gene(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cond := "upper(hugo_gene_symbol) = {symbol:String}"
	p := ch.Params{"symbol": strings.ToUpper(id)}
	if _, err := strconv.ParseInt(id, 10, 64); err == nil {
		cond = "entrez_gene_id = " + id
	}
	rows, err := ch.QueryP[GeneRow](s.CH,
		"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type FROM cbioportal_ref.gene WHERE "+cond+" LIMIT 1", p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		core.WriteError(w, http.StatusNotFound, core.MsgGeneNotFound(id))
		return
	}
	core.WriteJSON(w, rows[0])
}
