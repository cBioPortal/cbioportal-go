package studies

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

// genesFetch is GeneServiceImpl.fetchGenes. The body is a JSON array of gene
// ids, read as strings. With geneIdType ENTREZ_GENE_ID (the default) only the
// integer ids are looked up, by entrez id; with HUGO_GENE_SYMBOL every id is
// matched exactly, case included, against the Hugo symbols (GeneMapper's
// `hugo_gene_symbol IN`). Unknown ids are skipped, symbols shared by several
// genes are dropped, and the genes are sorted by symbol.
func (s Server) genesFetch(w http.ResponseWriter, r *http.Request) {
	var raw []json.RawMessage
	core.ReadBody(r, &raw)
	cond, p := genesFetchCondition(geneIDStrings(raw), r.URL.Query().Get("geneIdType") == "HUGO_GENE_SYMBOL")
	if cond == "" {
		core.WriteJSON(w, []GeneRow{})
		return
	}
	rows, err := ch.QueryP[GeneRow](s.CH,
		"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type "+
			"FROM cbioportal_ref.gene WHERE "+cond, p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, uniqueSymbols(rows))
}

// geneIDStrings reads the items of a List<String> body: strings as they are,
// other JSON scalars as their text (as Jackson coerces them).
func geneIDStrings(raw []json.RawMessage) []string {
	ids := make([]string, 0, len(raw))
	for _, m := range raw {
		var s string
		if json.Unmarshal(m, &s) != nil {
			s = strings.TrimSpace(string(m))
		}
		ids = append(ids, s)
	}
	return ids
}

// genesFetchCondition is fetchGenes' WHERE condition for ids, by exact Hugo
// symbol or by entrez id; empty when nothing can match.
func genesFetchCondition(ids []string, bySymbol bool) (string, ch.Params) {
	if bySymbol {
		if len(ids) == 0 {
			return "", nil
		}
		return "hugo_gene_symbol IN {symbols:Array(String)}", ch.Params{"symbols": ids}
	}
	var entrez []string
	for _, id := range ids {
		if !isInteger(id) {
			continue
		}
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			entrez = append(entrez, strconv.FormatInt(n, 10))
		}
	}
	if len(entrez) == 0 {
		return "", nil
	}
	return fmt.Sprintf("entrez_gene_id IN (%s)", strings.Join(entrez, ", ")), nil
}

// isInteger is GeneServiceImpl.isInteger, the regular expression ^-?\d+$.
func isInteger(id string) bool {
	digits := strings.TrimPrefix(id, "-")
	if digits == "" {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// uniqueSymbols is GeneServiceImpl.filterGenesWithMultipleEntrezIds: the
// genes whose symbol no other gene of rows has, sorted by symbol.
func uniqueSymbols(rows []GeneRow) []GeneRow {
	n := map[string]int{}
	for _, g := range rows {
		n[g.HugoGeneSymbol]++
	}
	out := make([]GeneRow, 0, len(rows))
	for _, g := range rows {
		if n[g.HugoGeneSymbol] == 1 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HugoGeneSymbol < out[j].HugoGeneSymbol })
	return out
}

// gene is GeneServiceImpl.getGene: by entrez id when the id is an integer,
// else by exact Hugo symbol.
func (s Server) gene(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cond := "hugo_gene_symbol = {symbol:String}"
	p := ch.Params{"symbol": id}
	if n, err := strconv.ParseInt(id, 10, 64); err == nil && isInteger(id) {
		cond = "entrez_gene_id = " + strconv.FormatInt(n, 10)
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
