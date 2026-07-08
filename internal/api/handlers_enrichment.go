package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/stats"
)

type caseIdentifier struct {
	CaseID             string `json:"caseId"`
	MolecularProfileID string `json:"molecularProfileId"`
}

type casesGroup struct {
	Name                          string           `json:"name"`
	MolecularProfileCaseIdentifiers []caseIdentifier `json:"molecularProfileCaseIdentifiers"`
}

// study/sample pairs for a group's cases.
func (g casesGroup) pairs() [][2]string {
	out := make([][2]string, 0, len(g.MolecularProfileCaseIdentifiers))
	for _, c := range g.MolecularProfileCaseIdentifiers {
		out = append(out, [2]string{molecularProfileStudy(c.MolecularProfileID), c.CaseID})
	}
	return out
}

// molecularProfileStudy strips a known molecular-profile suffix to get the study.
func molecularProfileStudy(mpid string) string {
	for _, suf := range []string{"_mutations", "_gistic", "_cna", "_structural_variants", "_log2CNA"} {
		if strings.HasSuffix(mpid, suf) {
			return strings.TrimSuffix(mpid, suf)
		}
	}
	// fall back: strip after the last underscore-delimited profile token isn't
	// reliable, so use profileMeta lookup as a last resort.
	return mpid
}

type geneAlteredRow struct {
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
	EntrezGeneID   *int64 `json:"entrezGeneId"`
	Altered        int    `json:"altered"`
}

type CountSummary struct {
	AlteredCount  int    `json:"alteredCount"`
	Name          string `json:"name"`
	ProfiledCount int    `json:"profiledCount"`
}

type AlterationEnrichment struct {
	Counts         []CountSummary `json:"counts"`
	Cytoband       string         `json:"cytoband"`
	EntrezGeneID   int64          `json:"entrezGeneId"`
	HugoGeneSymbol string         `json:"hugoGeneSymbol"`
	PValue         float64        `json:"pValue"`
}

// alterationEnrichmentsFetch computes per-gene alteration enrichment (mutation
// OR discrete CNA) across the posted groups, with a Fisher-exact p-value for the
// two-group case.
func (s *State) alterationEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Groups               []casesGroup      `json:"molecularProfileCasesGroupFilter"`
		AlterationEventTypes *alterationFilter `json:"alterationEventTypes"`
	}
	readBody(r, &body)
	if len(body.Groups) < 2 {
		writeJSON(w, []AlterationEnrichment{})
		return
	}

	type geneKey struct {
		symbol string
		entrez int64
	}
	altered := make([]map[geneKey]int, len(body.Groups)) // per group: gene -> altered count
	profiled := make([]int, len(body.Groups))
	geneSet := map[geneKey]bool{}

	// Count only the alteration type(s) matching the groups' molecular profiles:
	// a mutations-profile enrichment must not fold in CNA genes (and vice versa).
	useMut, useCna := false, false
	for _, g := range body.Groups {
		for _, c := range g.MolecularProfileCaseIdentifiers {
			switch {
			case strings.HasSuffix(c.MolecularProfileID, "_mutations"):
				useMut = true
			case strings.HasSuffix(c.MolecularProfileID, "_gistic"), strings.HasSuffix(c.MolecularProfileID, "_cna"):
				useCna = true
			}
		}
	}
	if !useMut && !useCna {
		useMut, useCna = true, true
	}

	for gi, g := range body.Groups {
		altered[gi] = map[geneKey]int{}
		pairs := g.pairs()
		profiled[gi] = len(pairs)
		if len(pairs) == 0 {
			continue
		}
		scope := fmt.Sprintf("(study_id, sample_id) IN (%s)", tupleIn(pairs))
		var parts []string
		if useMut {
			parts = append(parts, "SELECT study_id, sample_id, hugo_symbol, entrez_gene_id FROM mutation WHERE "+
				andClause(scope, body.AlterationEventTypes.mutationFilterClause()))
		}
		if useCna {
			parts = append(parts, "SELECT study_id, sample_id, hugo_symbol, entrez_gene_id FROM cna WHERE "+
				andClause(scope, body.AlterationEventTypes.cnaFilterClause()))
		}
		rows, err := ch.Query[geneAlteredRow](s.CH, fmt.Sprintf(
			"SELECT hugo_symbol AS hugoGeneSymbol, toInt64OrNull(entrez_gene_id) AS entrezGeneId, uniqExact(sample_id) AS altered FROM (%s) GROUP BY hugo_symbol, entrez_gene_id",
			strings.Join(parts, " UNION ALL ")))
		if err != nil {
			fail(w, err)
			return
		}
		for _, row := range rows {
			var e int64
			if row.EntrezGeneID != nil {
				e = *row.EntrezGeneID
			}
			k := geneKey{row.HugoGeneSymbol, e}
			altered[gi][k] = row.Altered
			geneSet[k] = true
		}
	}

	out := make([]AlterationEnrichment, 0, len(geneSet))
	for k := range geneSet {
		counts := make([]CountSummary, len(body.Groups))
		for gi, g := range body.Groups {
			counts[gi] = CountSummary{AlteredCount: altered[gi][k], Name: g.Name, ProfiledCount: profiled[gi]}
		}
		p := 1.0
		if len(body.Groups) == 2 {
			a := counts[0].AlteredCount
			b := counts[0].ProfiledCount - a
			c := counts[1].AlteredCount
			d := counts[1].ProfiledCount - c
			if b >= 0 && d >= 0 {
				p = stats.FisherExactTest(a, b, c, d)
			}
		}
		out = append(out, AlterationEnrichment{
			Counts: counts, EntrezGeneID: k.entrez, HugoGeneSymbol: k.symbol, PValue: p,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PValue < out[j].PValue })
	writeJSON(w, out)
}

func (s *State) routeEnrichment(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/alteration-enrichments/fetch", s.alterationEnrichmentsFetch)
}
