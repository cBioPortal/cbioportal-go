package studies

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// routeBulkList registers the bulk GET list endpoints and single-resource
// GETs. Paging query params are applied by PageList.
// All filtering is pushed into ClickHouse.
func (s Server) routeBulkList(mux *http.ServeMux) {
	// Bulk GET list endpoints (no study scope).
	mux.HandleFunc("GET /api/samples", s.allSamples)
	mux.HandleFunc("GET /api/patients", s.allPatients)
	mux.HandleFunc("GET /api/genes", s.allGenes)
	mux.HandleFunc("GET /api/sample-lists", s.allSampleLists)
	mux.HandleFunc("GET /api/clinical-attributes", s.allClinicalAttributes)

	// Single-resource / patient-view GETs.
	mux.HandleFunc("GET /api/sample-lists/{sampleListId}/sample-ids", s.sampleListSampleIDs)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}", s.sampleInStudy)
}

// ---------- Bulk samples ----------

// sampleFullRow carries the per-sample flags computed in ClickHouse so the
// all-samples / single-sample responses do not collide sample ids across
// studies (keys are (study_id, sample_id) tuples).
type sampleFullRow struct {
	StudyID       string `json:"studyId"`
	SampleID      string `json:"sampleId"`
	PatientID     string `json:"patientId"`
	SampleType    string `json:"sampleType"`
	SequencedFlag int    `json:"sequencedFlag"`
	SegFlag       int    `json:"segFlag"`
}

func toFullSample(r sampleFullRow) Sample {
	return Sample{
		SampleID: r.SampleID, SampleType: r.SampleType, PatientID: r.PatientID, StudyID: r.StudyID,
		Sequenced: r.SequencedFlag != 0, CopyNumberSegmentPresent: r.SegFlag != 0,
		UniqueSampleKey: core.UniqueKey(r.StudyID, r.SampleID), UniquePatientKey: core.UniqueKey(r.StudyID, r.PatientID),
	}
}

// sampleFullSelect builds the sample rows of the scope plus sampleType /
// sequenced / seg flags, for the given WHERE predicate over alias `s`.
func sampleFullSelect(b core.Binder, sc core.Scope, where string) string {
	return sampleFullSelectFrom(b, sc, where) + " ORDER BY " + sampleDefaultOrder
}

const sampleDefaultOrder = "s.study_id, s.sample_id"

// sampleFullSelectFrom is sampleFullSelect without its ORDER BY.
func sampleFullSelectFrom(b core.Binder, sc core.Scope, where string) string {
	return "SELECT s.study_id AS studyId, s.sample_id AS sampleId, s.patient_id AS patientId, " +
		"ifNull(sd.sample_type, '') AS sampleType, toUInt8(ifNull(sd.sequenced, 0)) AS sequencedFlag, " +
		"toUInt8(ifNull(sd.copy_number_segment_present, 0)) AS segFlag " +
		"FROM " + sc.Samples(b) + " s LEFT JOIN " + sc.Cases().Rows(b, core.TSampleDerived) +
		" sd ON sd.cancer_study_identifier = s.study_id AND sd.sample_stable_id = s.sample_id " +
		"WHERE " + where
}

// sampleSortable are the SampleSortBy columns (stableId, sampleType) as
// sampleFullSelect names them.
var sampleSortable = map[string]string{"sampleId": "sampleId", "sampleType": "sampleType"}

// writeSampleRows streams the samples sql selects, or counts them for
// projection=META.
func (s Server) writeSampleRows(w http.ResponseWriter, r *http.Request, sql string, args *core.SQLArgs) {
	if core.ProjectionOf(r) == "META" {
		core.WriteMetaCount(w, r, s.CH, "SELECT count() AS n FROM ("+sql+")", args.Params(sql))
		return
	}
	out := core.NewJSONArray(w, r)
	err := ch.QueryEachP(s.CH, sql, args.Params(sql), func(row *sampleFullRow) error {
		return out.Add(toFullSample(*row))
	})
	out.Finish(err)
}

// allSamples serves GET /api/samples (ClickhouseSampleMapper with a keyword):
// every space-separated term of `keyword` must occur in the sample id
// (ILIKE, so % and _ are wildcards), and matches whose id starts with the
// whole keyword come first, each part ordered by sample id.
func (s Server) allSamples(w http.ResponseWriter, r *http.Request) {
	args := core.NewArgs()
	conds := []string{"1"}
	order := sampleDefaultOrder
	if q := r.URL.Query(); q.Has("keyword") {
		kw := q.Get("keyword")
		for term := range strings.SplitSeq(strings.TrimSpace(kw), " ") {
			conds = append(conds, fmt.Sprintf("s.sample_id ILIKE concat('%%', %s, '%%')", args.Str(term)))
		}
		// A sample id in several studies: by study, which Java leaves
		// unordered.
		order = fmt.Sprintf("if(s.sample_id ILIKE concat(%s, '%%'), 0, 1), s.sample_id, s.study_id", args.Str(kw))
	}
	s.writeSampleRows(w, r, sampleFullSelectFrom(args, core.AcrossStudies, strings.Join(conds, " AND "))+core.SQLPageOrder(r, sampleSortable, order), args)
}

// sampleInStudy — GET /api/studies/{studyId}/samples/{sampleId}. Single Sample.
func (s Server) sampleInStudy(w http.ResponseWriter, r *http.Request) {
	p := ch.Params{}
	rows, err := ch.QueryP[sampleFullRow](s.CH, sampleFullSelect(core.ParamsBinder(p), core.BySample(r.PathValue("studyId"), r.PathValue("sampleId")), "1"), p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "sample not found", http.StatusNotFound)
		return
	}
	core.WriteJSON(w, toFullSample(rows[0]))
}

// ---------- Bulk patients ----------

// allPatients serves GET /api/patients (PatientMapper.getPatients). With a
// keyword, patients are joined to their samples and every space-separated
// term must occur (LIKE: case-sensitive on ClickHouse, % and _ wildcards) in
// the patient or the sample id; a patient is listed once per matching sample,
// patients whose id starts with the whole keyword first, then by patient id.
// sortBy and direction order the matches as they order a list without a
// keyword.
func (s Server) allPatients(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := core.NewArgs()
	if !q.Has("keyword") {
		s.writePatientRows(w, r, "SELECT study_id AS studyId, patient_id AS patientId FROM "+core.AcrossStudies.Patients(args)+
			core.SQLPageOrder(r, patientSortable, s.patientOrder(r)), nil)
		return
	}
	kw := q.Get("keyword")
	var conds []string
	for term := range strings.SplitSeq(kw, " ") {
		t := args.Str(term)
		conds = append(conds, fmt.Sprintf("(p.patient_id LIKE concat('%%', %s, '%%') OR sm.sample_id LIKE concat('%%', %s, '%%'))", t, t))
	}
	// Equal patient ids (one per study) come in the patient table's import
	// order, as ClickHouse returns production's rows.
	ties := "p.study_id, sm.sample_id"
	if s.ImportOrdered() {
		ties = "p.internal_id, sm.internal_id"
	}
	sql := fmt.Sprintf("SELECT p.study_id AS studyId, p.patient_id AS patientId FROM %s p "+
		"JOIN %s sm ON sm.study_id = p.study_id AND sm.patient_id = p.patient_id WHERE %s",
		core.AcrossStudies.Patients(args), core.AcrossStudies.Samples(args), strings.Join(conds, " AND ")) +
		core.SQLPageOrder(r, keywordPatientSortable,
			fmt.Sprintf("if(p.patient_id LIKE concat(%s, '%%'), 0, 1), p.patient_id, %s", args.Str(kw), ties))
	s.writePatientRows(w, r, sql, args.Params(sql))
}

// keywordPatientSortable is patientSortable over allPatients' keyword join;
// a sorted page keeps the match order for ties.
var keywordPatientSortable = map[string]string{"patientId": "p.patient_id"}

// ---------- Bulk genes ----------

// geneSortable are the GeneSortBy columns over allGenes' query. cytoband and
// length come from geneLocationJoin.
var geneSortable = map[string]string{
	"entrezGeneId": "g.entrez_gene_id", "hugoGeneSymbol": "g.hugo_gene_symbol", "type": "g.type",
	"cytoband": "loc.cytoband", "length": "loc.length",
}

// defaultGenome is the reference genome a portal uses unless its ucsc.build
// property names another (ReferenceGenome.HOMO_SAPIENS_DEFAULT_GENOME_NAME).
const defaultGenome = "hg19"

// geneLocationJoin joins every gene (aliased g) to its cytoband and length,
// the span from its start to its end, on the portal's reference genome
// (ucsc.build). Genes without a location there have neither.
func (s Server) geneLocationJoin(args *core.SQLArgs) string {
	genome := strings.TrimSpace(s.Props["ucsc.build"])
	if genome == "" {
		genome = defaultGenome
	}
	return " LEFT JOIN (SELECT rgg.entrez_gene_id AS entrez_gene_id, rgg.cytoband AS cytoband, " +
		"rgg.end - rgg.start AS length FROM cbioportal_ref.reference_genome_gene rgg " +
		"INNER JOIN cbioportal_ref.reference_genome rg ON rg.reference_genome_id = rgg.reference_genome_id " +
		"WHERE lower(rg.name) = lower(" + args.Str(genome) + ")) loc ON loc.entrez_gene_id = g.entrez_gene_id"
}

// geneSelect selects geneRow's columns from the genes (aliased g), joined to
// their location when sortBy needs it.
func (s Server) geneSelect(args *core.SQLArgs, sortBy string) string {
	from := "cbioportal_ref.gene g"
	if sortBy == "cytoband" || sortBy == "length" {
		from += s.geneLocationJoin(args)
	}
	return "SELECT g.entrez_gene_id AS entrezGeneId, g.hugo_gene_symbol AS hugoGeneSymbol, g.type AS type FROM " + from
}

// allGenes — GET /api/genes, with GeneMapper's keyword and alias matching.
func (s Server) allGenes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := core.NewArgs()
	conds := []string{"1"}
	if alias, ok := q["alias"]; ok {
		conds = append(conds,
			"g.entrez_gene_id IN (SELECT entrez_gene_id FROM cbioportal_ref.gene_alias WHERE gene_alias = "+args.Str(alias[0])+")")
	}
	// GeneMapper: every space-separated keyword term is a case-insensitive
	// symbol prefix; matches of the whole keyword with its exact case come
	// first. Without a keyword genes come in the gene table's entrez order.
	order := "g.entrez_gene_id"
	kw, keyword := q["keyword"]
	if keyword {
		for _, term := range strings.Split(kw[0], " ") {
			conds = append(conds, fmt.Sprintf("g.hugo_gene_symbol ILIKE concat(%s, '%%')", args.Str(term)))
		}
		order = fmt.Sprintf("g.hugo_gene_symbol LIKE concat(%s, '%%') DESC, g.hugo_gene_symbol", args.Str(kw[0]))
	}
	sortBy := q.Get("sortBy")
	sel := s.geneSelect(args, sortBy) + " WHERE " + strings.Join(conds, " AND ")
	if !keyword {
		rows, err := core.QueryA[GeneRow](s.CH, sel+core.SQLPageOrder(r, geneSortable, order), args)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		if rows == nil {
			rows = []GeneRow{}
		}
		core.WriteJSON(w, rows)
		return
	}
	// A keyword search is paged here (PageList leaves it alone), as
	// GeneServiceImpl post-processes the mapper's page; META counts every
	// match.
	pageSize, limit := core.MaxPageSize, ""
	if core.ProjectionOf(r) != "META" {
		if v, err := strconv.Atoi(strings.TrimSpace(q.Get("pageSize"))); err == nil {
			pageSize = v
		}
		number, _ := strconv.Atoi(strings.TrimSpace(q.Get("pageNumber")))
		limit = fmt.Sprintf(" LIMIT %d OFFSET %d", pageSize, pageSize*number)
	}
	desc := strings.EqualFold(q.Get("direction"), "DESC")
	expr, sorted := geneSortable[sortBy]
	if sorted {
		order = geneSortOrder(expr, desc) + ", " + order
	}
	rows, err := core.QueryA[GeneRow](s.CH, fmt.Sprintf("%s ORDER BY %s%s", sel, order, limit), args)
	if err == nil {
		rows, err = s.keywordGenes(rows, kw[0], pageSize)
	}
	if err == nil && sorted && len(rows) > 1 && limit != "" {
		rows, err = s.sortGenes(rows, sortBy, desc)
	}
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if rows == nil {
		rows = []GeneRow{}
	}
	core.WriteJSON(w, rows)
}

// geneSortOrder orders by a geneSortable expression, NULLS LAST in either
// direction as PageList sorts.
func geneSortOrder(expr string, desc bool) string {
	if desc {
		return expr + " DESC NULLS LAST"
	}
	return expr + " ASC NULLS LAST"
}

// sortGenes sorts a keyword page by sortBy (a geneSortable key), ties in the
// page's order.
func (s Server) sortGenes(page []GeneRow, sortBy string, desc bool) ([]GeneRow, error) {
	ids := make([]int64, len(page))
	for i, g := range page {
		ids[i] = g.EntrezGeneID
	}
	args := core.NewArgs()
	list := "[" + core.IntListSQL(ids) + "]"
	return core.QueryA[GeneRow](s.CH, fmt.Sprintf("%s WHERE g.entrez_gene_id IN %s ORDER BY %s, indexOf(%s, g.entrez_gene_id)",
		s.geneSelect(args, sortBy), list, geneSortOrder(geneSortable[sortBy], desc), list), args)
}

// keywordGenes completes a page of a keyword search as GeneServiceImpl
// .getAllGenes does: a page with room left gains the genes with an alias
// equal to the keyword (ignoring case), then genes whose symbol occurs more
// than once are dropped and the page is sorted by symbol. A page with a
// sortBy is sorted by it afterwards (sortGenes).
func (s Server) keywordGenes(page []GeneRow, keyword string, pageSize int) ([]GeneRow, error) {
	if len(page) < pageSize {
		aliased, err := ch.QueryP[GeneRow](s.CH,
			"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type FROM cbioportal_ref.gene "+
				"WHERE entrez_gene_id IN (SELECT entrez_gene_id FROM cbioportal_ref.gene_alias WHERE lower(gene_alias) = {alias:String}) "+
				"ORDER BY entrez_gene_id", ch.Params{"alias": strings.ToLower(keyword)})
		if err != nil {
			return nil, err
		}
		aliased = aliased[:min(len(aliased), pageSize-len(page))]
		for _, g := range aliased {
			if !slices.ContainsFunc(page, func(p GeneRow) bool { return p.EntrezGeneID == g.EntrezGeneID }) {
				page = append(page, g)
			}
		}
	}
	n := map[string]int{}
	for _, g := range page {
		n[g.HugoGeneSymbol]++
	}
	page = slices.DeleteFunc(page, func(g GeneRow) bool { return n[g.HugoGeneSymbol] > 1 })
	slices.SortFunc(page, func(a, b GeneRow) int { return strings.Compare(a.HugoGeneSymbol, b.HugoGeneSymbol) })
	return page, nil
}

// ---------- Bulk sample lists ----------

// allSampleLists — GET /api/sample-lists. Returns []SampleList across studies.
func (s Server) allSampleLists(w http.ResponseWriter, r *http.Request) {
	s.sampleListsQuery(w, r, core.AcrossStudies, false)
}

// sampleListSampleIDs — GET /api/sample-lists/{sampleListId}/sample-ids.
// Returns []string of sample ids in the list, in the list's order.
func (s Server) sampleListSampleIDs(w http.ResponseWriter, r *http.Request) {
	type row struct {
		SampleID string `json:"sampleId"`
	}
	order := "sample_id"
	if s.ImportOrdered() {
		order = "internal_id, sample_id"
	}
	p := ch.Params{}
	rows, err := ch.QueryP[row](s.CH,
		"SELECT sample_id AS sampleId FROM "+s.BySampleList(r.PathValue("sampleListId")).From(core.ParamsBinder(p), core.TSampleList)+core.SQLListOrder(r, nil, order), p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SampleID
	}
	core.WriteJSON(w, out)
}

// ---------- Bulk clinical attributes ----------

// allClinicalAttributes — GET /api/clinical-attributes. Returns
// []ClinicalAttribute across all studies from clinical_attribute_meta.
func (s Server) allClinicalAttributes(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.Query[ClinAttrRow](s.CH, clinAttrSelect+" ORDER BY clinical_attribute_id, "+s.StudyOrder("study_id"))
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]ClinicalAttribute, len(rows))
	for i, r := range rows {
		out[i] = toClinicalAttribute(r)
	}
	core.WriteJSON(w, out)
}

// writeSamples writes the samples of the scope matching a predicate over the
// `sample` view (aliased s), with type, sequenced and segment flags. where
// may hold placeholders bound in args. A page is read as SQLListOrder
// orders it.
func (s Server) writeSamples(w http.ResponseWriter, r *http.Request, sc core.Scope, where string, args *core.SQLArgs) {
	s.writeSampleRows(w, r, sampleFullSelectFrom(args, sc, where)+core.SQLListOrder(r, sampleSortable, sampleDefaultOrder), args)
}
