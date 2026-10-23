package comparison

import (
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/studyview"
	"github.com/cbioportal/cbioportal-go/internal/stats"
)

type caseIdentifier struct {
	CaseID             string `json:"caseId"`
	MolecularProfileID string `json:"molecularProfileId"`
}

type casesGroup struct {
	Name                            string           `json:"name"`
	MolecularProfileCaseIdentifiers []caseIdentifier `json:"molecularProfileCaseIdentifiers"`
}

type CountSummary struct {
	AlteredCount  int    `json:"alteredCount"`
	Name          string `json:"name"`
	ProfiledCount int    `json:"profiledCount"`
}

// AlterationEnrichment is one gene's alteration-enrichment row. cBioPortal
// never populates its cytoband, so the null field is not serialized.
type AlterationEnrichment struct {
	Counts         []CountSummary `json:"counts"`
	EntrezGeneID   int64          `json:"entrezGeneId"`
	HugoGeneSymbol string         `json:"hugoGeneSymbol"`
	PValue         float64        `json:"pValue"`
}

// alterationEnrichmentsFetch compares per-gene alteration frequencies across
// groups (GetAlterationEnrichmentsUseCase): a case counts as profiled for a gene
// when one of its panels in the group's profiles covers the gene, and as
// altered when it has a matching event in those profiles under the alteration
// filter. Two groups are scored with a two-tailed Fisher exact test, more with
// chi-square.
func (s Server) alterationEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Groups               []casesGroup           `json:"molecularProfileCasesGroupFilter"`
		AlterationEventTypes *core.AlterationFilter `json:"alterationEventTypes"`
	}
	core.ReadBody(r, &body)
	patient := strings.EqualFold(r.URL.Query().Get("enrichmentType"), "PATIENT")
	unit := "sample_unique_id"
	if patient {
		unit = "patient_unique_id"
	}
	args := core.NewArgs()
	a := studyview.AlterationSQL{Args: args}
	if body.AlterationEventTypes != nil {
		a.Filter = *body.AlterationEventTypes
	}
	profileStudy, err := s.profileStudies(body.Groups)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	type geneRow struct {
		Gene   string `json:"gene"`
		Entrez *int64 `json:"entrez"`
		N      int    `json:"n"`
	}
	type geneCounts struct {
		entrez            *int64
		altered, profiled map[string]int
	}
	genes := map[string]*geneCounts{}
	get := func(g string) *geneCounts {
		if c, ok := genes[g]; ok {
			return c
		}
		c := &geneCounts{altered: map[string]int{}, profiled: map[string]int{}}
		genes[g] = c
		return c
	}
	// Altered counts first: only genes altered in some group are reported, so
	// profiled counts are needed for those genes alone.
	type groupScope struct {
		entities string
		profiles core.Scope
		studies  []string
		altered  []geneRow
	}
	scopes := make([]*groupScope, len(body.Groups))
	alteredGenes := map[string]bool{}
	for i, g := range body.Groups {
		entitySet, profileSet, studySet := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, c := range g.MolecularProfileCaseIdentifiers {
			st, ok := profileStudy[c.MolecularProfileID]
			if !ok {
				continue
			}
			entitySet[st+"_"+c.CaseID] = true
			profileSet[c.MolecularProfileID] = true
			studySet[st] = true
		}
		if len(entitySet) == 0 {
			continue
		}
		sc := &groupScope{entities: args.Strs(core.SortedKeys(entitySet)), profiles: core.ByProfiles(core.SortedKeys(profileSet)), studies: core.SortedKeys(studySet)}
		altered, err := core.QueryA[geneRow](s.CH, fmt.Sprintf(
			"SELECT hugo_gene_symbol AS gene, any(toInt64OrNull(toString(entrez_gene_id))) AS entrez, toInt64(uniqExact(%[1]s)) AS n "+
				"FROM %[2]s AND mutation_status != 'UNCALLED' "+
				"AND %[1]s IN %[3]s AND %[4]s GROUP BY hugo_gene_symbol",
			unit, sc.profiles.From(args, core.TGenomicEventDerived), sc.entities, a.EnrichmentClause()), args)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		sc.altered = altered
		for _, x := range altered {
			alteredGenes[x.Gene] = true
		}
		scopes[i] = sc
	}
	geneList := args.Strs(core.SortedKeys(alteredGenes))
	for i, g := range body.Groups {
		sc := scopes[i]
		if sc == nil || len(alteredGenes) == 0 {
			continue
		}
		profiled, err := s.profiledPerGene(args, unit, sc.profiles, sc.studies, sc.entities, geneList)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		for gene, n := range profiled {
			get(gene).profiled[g.Name] = n
		}
		for _, x := range sc.altered {
			if c, ok := genes[x.Gene]; ok && c.profiled[g.Name] > 0 {
				c.altered[g.Name] = x.N
				if c.entrez == nil {
					c.entrez = x.Entrez
				}
			}
		}
	}
	// Java keys the groups by name in a HashMap, so the counts, and the
	// group Fisher's test reads first, come in its iteration order.
	names := map[string]bool{}
	for _, g := range body.Groups {
		names[g.Name] = true
	}
	groupOrder := core.JavaHashMapOrder(core.SortedKeys(names))
	out := []AlterationEnrichment{}
	for _, gene := range core.SortedKeys(genes) {
		c := genes[gene]
		any := false
		counts := make([]CountSummary, len(groupOrder))
		for i, name := range groupOrder {
			counts[i] = CountSummary{Name: name, AlteredCount: c.altered[name], ProfiledCount: c.profiled[name]}
			if counts[i].AlteredCount > 0 {
				any = true
			}
		}
		if !any {
			continue
		}
		var entrez int64
		if c.entrez != nil {
			entrez = *c.entrez
		}
		out = append(out, AlterationEnrichment{Counts: counts, EntrezGeneID: entrez, HugoGeneSymbol: gene, PValue: enrichmentPValue(counts)})
	}
	core.WriteJSON(w, out)
}

// enrichmentPValue follows AlterationEnrichmentScoreUtil: 0 when a group has
// more altered than profiled cases, and 1 when fewer than two groups are
// profiled for the gene, as nothing is compared (Java answers 0, the most
// significant value).
func enrichmentPValue(counts []CountSummary) float64 {
	profiledGroups := 0
	for _, c := range counts {
		if c.ProfiledCount > 0 {
			profiledGroups++
			if c.AlteredCount > c.ProfiledCount {
				return 0
			}
		}
	}
	if profiledGroups < 2 {
		return 1
	}
	if len(counts) == 2 {
		return stats.JavaFisherTwoTailed(counts[1].ProfiledCount-counts[1].AlteredCount, counts[1].AlteredCount,
			counts[0].ProfiledCount-counts[0].AlteredCount, counts[0].AlteredCount)
	}
	// A row per group, as in Java. A group with no sample profiled for the
	// gene is an empty row, which makes the test NaN and the p-value 1: the
	// groups cannot all be compared.
	table := make([][]int64, len(counts))
	for i, c := range counts {
		table[i] = []int64{int64(c.AlteredCount), int64(c.ProfiledCount - c.AlteredCount)}
	}
	p := stats.CommonsChiSquareTest(table)
	if math.IsNaN(p) {
		return 1
	}
	return p
}

// profileStudies maps each molecular profile referenced by the groups to its study.
func (s Server) profileStudies(groups []casesGroup) (map[string]string, error) {
	set := map[string]bool{}
	for _, g := range groups {
		for _, c := range g.MolecularProfileCaseIdentifiers {
			set[c.MolecularProfileID] = true
		}
	}
	profs, err := s.ResolvePanelProfiles(core.SortedKeys(set))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for id, p := range profs {
		out[id] = p.StudyID
	}
	return out, nil
}

func (s Server) routeEnrichment(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/alteration-enrichments/fetch", s.alterationEnrichmentsFetch)
}

// profiledPerGene counts, per gene of genes, the entities (of studies) with a
// panel of profiles covering it: entities are grouped by their panel
// combination and each combination's genes are expanded once. entities and
// genes are array placeholders bound in args.
func (s Server) profiledPerGene(args *core.SQLArgs, unit string, profiles core.Scope, studies []string, entities, genes string) (map[string]int, error) {
	type combo struct {
		Panels []string `json:"panels"`
		N      int      `json:"n"`
	}
	combos, err := core.QueryA[combo](s.CH, fmt.Sprintf(
		"SELECT panels, toInt64(count()) AS n FROM (SELECT sd.%[1]s AS entity, arraySort(groupUniqArray(p.gene_panel_id)) AS panels "+
			"FROM %[2]s p JOIN %[4]s sd ON sd.sample_unique_id = p.sample_unique_id "+
			"WHERE sd.%[1]s IN %[3]s GROUP BY entity) GROUP BY panels",
		unit, profiles.Rows(args, core.TSampleToGenePanelDerived), entities, core.ByStudies(studies).Rows(args, core.TSampleDerived)), args)
	if err != nil {
		return nil, err
	}
	panelSet := map[string]bool{}
	for _, c := range combos {
		for _, p := range c.Panels {
			panelSet[p] = true
		}
	}
	panelGenes, err := s.panelGenes(args, core.SortedKeys(panelSet), genes)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, c := range combos {
		seen := map[string]bool{}
		for _, p := range c.Panels {
			for _, g := range panelGenes[p] {
				if !seen[g] {
					seen[g] = true
					out[g] += c.N
				}
			}
		}
	}
	return out, nil
}

// panelGenes returns each panel's genes among genes (an array placeholder
// bound in args); WES covers every gene.
func (s Server) panelGenes(args *core.SQLArgs, panels []string, genes string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(panels) == 0 {
		return out, nil
	}
	type row struct {
		Panel string   `json:"panel"`
		Genes []string `json:"genes"`
	}
	rows, err := core.QueryA[row](s.CH, fmt.Sprintf(
		"SELECT gene_panel_id AS panel, groupArray(gene) AS genes FROM gene_panel_to_gene_derived "+
			"WHERE gene_panel_id IN %s AND gene IN %s GROUP BY gene_panel_id", args.Strs(panels), genes), args)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Panel] = r.Genes
	}
	return out, nil
}
