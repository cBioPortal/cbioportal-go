package api

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// routeBulkList registers the bulk GET list endpoints, single-resource GETs
// and the data-access-token endpoints (dat.go) or their placeholders. Paging query params are applied by pageList.
// All filtering is pushed into ClickHouse.
func (s *State) routeBulkList(mux *http.ServeMux) {
	// Bulk GET list endpoints (no study scope).
	mux.HandleFunc("GET /api/samples", s.allSamples)
	mux.HandleFunc("GET /api/patients", s.allPatients)
	mux.HandleFunc("GET /api/genes", s.allGenes)
	mux.HandleFunc("GET /api/sample-lists", s.allSampleLists)
	mux.HandleFunc("GET /api/clinical-attributes", s.allClinicalAttributes)

	// Single-resource / patient-view GETs.
	mux.HandleFunc("GET /api/sample-lists/{sampleListId}/sample-ids", s.sampleListSampleIDs)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}", s.sampleInStudy)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}/copy-number-segments", s.sampleCopyNumberSegments)

	if s.dat != nil {
		s.routeDataAccessTokens(mux, s.dat)
		return
	}
	// Data-access tokens without dat.method=oauth2: placeholders.
	mux.HandleFunc("GET /api/data-access-token", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{}) })
	mux.HandleFunc("GET /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, []any{}) })
	mux.HandleFunc("POST /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) {
		readBody(r, nil)
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("DELETE /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /api/data-access-tokens/{token}", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{}) })
	mux.HandleFunc("DELETE /api/data-access-tokens/{token}", func(w http.ResponseWriter, r *http.Request) {})
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
		UniqueSampleKey: uniqueKey(r.StudyID, r.SampleID), UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
	}
}

// sampleFullSelect builds the sample rows plus sampleType / sequenced / seg
// flags for the given WHERE predicate over alias `s`.
func sampleFullSelect(where string) string {
	return sampleFullSelectFrom(where) + " ORDER BY " + sampleDefaultOrder
}

const sampleDefaultOrder = "s.study_id, s.sample_id"

// sampleFullSelectFrom is sampleFullSelect without its ORDER BY.
func sampleFullSelectFrom(where string) string {
	return "SELECT s.study_id AS studyId, s.sample_id AS sampleId, s.patient_id AS patientId, " +
		"ifNull(sd.sample_type, '') AS sampleType, toUInt8(ifNull(sd.sequenced, 0)) AS sequencedFlag, " +
		"toUInt8(ifNull(sd.copy_number_segment_present, 0)) AS segFlag " +
		"FROM sample s LEFT JOIN sample_derived sd ON sd.cancer_study_identifier = s.study_id AND sd.sample_stable_id = s.sample_id " +
		"WHERE " + where
}

// sampleSortable are the SampleSortBy columns (stableId, sampleType) as
// sampleFullSelect names them.
var sampleSortable = map[string]string{"sampleId": "sampleId", "sampleType": "sampleType"}

// writeSampleRows streams the samples sql selects, or counts them for
// projection=META.
func (s *State) writeSampleRows(w http.ResponseWriter, r *http.Request, sql string, args *sqlArgs) {
	if projectionOf(r) == "META" {
		writeMetaCount(w, r, s.CH, "SELECT count() AS n FROM ("+sql+")", args.params(sql))
		return
	}
	out := newJSONArray(w, r)
	err := ch.QueryEachP(s.CH, sql, args.params(sql), func(row *sampleFullRow) error {
		return out.add(toFullSample(*row))
	})
	out.finish(err)
}

// allSamples serves GET /api/samples (ClickhouseSampleMapper with a keyword):
// every space-separated term of `keyword` must occur in the sample id
// (ILIKE, so % and _ are wildcards), and matches whose id starts with the
// whole keyword come first, each part ordered by sample id.
func (s *State) allSamples(w http.ResponseWriter, r *http.Request) {
	args := newArgs()
	conds := []string{"1"}
	order := sampleDefaultOrder
	if q := r.URL.Query(); q.Has("keyword") {
		kw := q.Get("keyword")
		for term := range strings.SplitSeq(strings.TrimSpace(kw), " ") {
			conds = append(conds, fmt.Sprintf("s.sample_id ILIKE concat('%%', %s, '%%')", args.str(term)))
		}
		// A sample id in several studies: by study, which Java leaves
		// unordered.
		order = fmt.Sprintf("if(s.sample_id ILIKE concat(%s, '%%'), 0, 1), s.sample_id, s.study_id", args.str(kw))
	}
	s.writeSampleRows(w, r, sampleFullSelectFrom(strings.Join(conds, " AND "))+sqlPageOrder(r, sampleSortable, order), args)
}

// sampleInStudy — GET /api/studies/{studyId}/samples/{sampleId}. Single Sample.
func (s *State) sampleInStudy(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.QueryP[sampleFullRow](s.CH, sampleFullSelect("s.study_id = {study:String} AND s.sample_id = {sample:String}"),
		ch.Params{"study": r.PathValue("studyId"), "sample": r.PathValue("sampleId")})
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "sample not found", http.StatusNotFound)
		return
	}
	writeJSON(w, toFullSample(rows[0]))
}

// ---------- Bulk patients ----------

// allPatients serves GET /api/patients (PatientMapper.getPatients). With a
// keyword, patients are joined to their samples and every space-separated
// term must occur (LIKE: case-sensitive on ClickHouse, % and _ wildcards) in
// the patient or the sample id; a patient is listed once per matching sample,
// patients whose id starts with the whole keyword first, then by patient id.
func (s *State) allPatients(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := newArgs()
	if !q.Has("keyword") {
		s.writePatientRows(w, r, "SELECT study_id AS studyId, patient_id AS patientId FROM patient"+
			sqlPageOrder(r, patientSortable, s.patientOrder(r)), nil)
		return
	}
	kw := q.Get("keyword")
	var conds []string
	for term := range strings.SplitSeq(kw, " ") {
		t := args.str(term)
		conds = append(conds, fmt.Sprintf("(p.patient_id LIKE concat('%%', %s, '%%') OR sm.sample_id LIKE concat('%%', %s, '%%'))", t, t))
	}
	// Equal patient ids (one per study) come in the patient table's import
	// order, as ClickHouse returns production's rows.
	ties := "p.study_id, sm.sample_id"
	if s.importOrdered() {
		ties = "p.internal_id, sm.internal_id"
	}
	sql := fmt.Sprintf("SELECT p.study_id AS studyId, p.patient_id AS patientId FROM patient p "+
		"JOIN sample sm ON sm.study_id = p.study_id AND sm.patient_id = p.patient_id WHERE %s "+
		"ORDER BY if(p.patient_id LIKE concat(%s, '%%'), 0, 1), p.patient_id, %s",
		strings.Join(conds, " AND "), args.str(kw), ties)
	s.writePatientRows(w, r, sql, args.params(sql))
}

// ---------- Bulk genes ----------

// allGenes — GET /api/genes, with GeneMapper's keyword and alias matching.
func (s *State) allGenes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := newArgs()
	conds := []string{"1"}
	if alias, ok := q["alias"]; ok {
		conds = append(conds,
			"entrez_gene_id IN (SELECT entrez_gene_id FROM cbioportal_ref.gene_alias WHERE gene_alias = "+args.str(alias[0])+")")
	}
	// GeneMapper: every space-separated keyword term is a case-insensitive
	// symbol prefix; matches of the whole keyword with its exact case come
	// first. Without a keyword genes come in the gene table's entrez order.
	order := "entrez_gene_id"
	kw, keyword := q["keyword"]
	if keyword {
		for _, term := range strings.Split(kw[0], " ") {
			conds = append(conds, fmt.Sprintf("hugo_gene_symbol ILIKE concat(%s, '%%')", args.str(term)))
		}
		order = fmt.Sprintf("hugo_gene_symbol LIKE concat(%s, '%%') DESC, hugo_gene_symbol", args.str(kw[0]))
	}
	limit := ""
	pageSize := maxPageSize
	if keyword && projectionOf(r) != "META" {
		// A keyword search is paged here (pageList leaves it alone), as
		// GeneServiceImpl post-processes the mapper's page; META counts
		// every match.
		if v, err := strconv.Atoi(strings.TrimSpace(q.Get("pageSize"))); err == nil {
			pageSize = v
		}
		number, _ := strconv.Atoi(strings.TrimSpace(q.Get("pageNumber")))
		limit = fmt.Sprintf(" LIMIT %d OFFSET %d", pageSize, pageSize*number)
	}
	rows, err := queryA[geneRow](s.CH, fmt.Sprintf(
		"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type, "+
			"genetic_entity_id AS geneticEntityId FROM cbioportal_ref.gene WHERE %s ORDER BY %s%s",
		strings.Join(conds, " AND "), order, limit), args)
	if err != nil {
		fail(w, r, err)
		return
	}
	if keyword {
		rows, err = s.keywordGenes(rows, kw[0], pageSize)
		if err != nil {
			fail(w, r, err)
			return
		}
	}
	if rows == nil {
		rows = []geneRow{}
	}
	writeJSON(w, rows)
}

// keywordGenes completes a page of a keyword search as GeneServiceImpl
// .getAllGenes does: a page with room left gains the genes with an alias
// equal to the keyword (ignoring case), then genes whose symbol occurs more
// than once are dropped and the page is sorted by symbol.
func (s *State) keywordGenes(page []geneRow, keyword string, pageSize int) ([]geneRow, error) {
	if len(page) < pageSize {
		aliased, err := ch.QueryP[geneRow](s.CH,
			"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type FROM cbioportal_ref.gene "+
				"WHERE entrez_gene_id IN (SELECT entrez_gene_id FROM cbioportal_ref.gene_alias WHERE lower(gene_alias) = {alias:String}) "+
				"ORDER BY entrez_gene_id", ch.Params{"alias": strings.ToLower(keyword)})
		if err != nil {
			return nil, err
		}
		aliased = aliased[:min(len(aliased), pageSize-len(page))]
		for _, g := range aliased {
			if !slices.ContainsFunc(page, func(p geneRow) bool { return p.EntrezGeneID == g.EntrezGeneID }) {
				page = append(page, g)
			}
		}
	}
	n := map[string]int{}
	for _, g := range page {
		n[g.HugoGeneSymbol]++
	}
	page = slices.DeleteFunc(page, func(g geneRow) bool { return n[g.HugoGeneSymbol] > 1 })
	slices.SortFunc(page, func(a, b geneRow) int { return strings.Compare(a.HugoGeneSymbol, b.HugoGeneSymbol) })
	return page, nil
}

// ---------- Bulk sample lists ----------

// allSampleLists — GET /api/sample-lists. Returns []SampleList across studies.
func (s *State) allSampleLists(w http.ResponseWriter, r *http.Request) {
	s.sampleListsQuery(w, r, "", nil, false)
}

// sampleListSampleIDs — GET /api/sample-lists/{sampleListId}/sample-ids.
// Returns []string of sample ids in the list, in the list's order.
func (s *State) sampleListSampleIDs(w http.ResponseWriter, r *http.Request) {
	type row struct {
		SampleID string `json:"sampleId"`
	}
	order := "sample_id"
	if s.importOrdered() {
		order = "internal_id, sample_id"
	}
	p := ch.Params{}
	rows, err := ch.QueryP[row](s.CH,
		"SELECT sample_id AS sampleId FROM sample_list WHERE "+s.sampleListCondP(p, r.PathValue("sampleListId"))+" ORDER BY "+order, p)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SampleID
	}
	writeJSON(w, out)
}

// ---------- Bulk clinical attributes ----------

// allClinicalAttributes — GET /api/clinical-attributes. Returns
// []ClinicalAttribute across all studies from clinical_attribute_meta.
func (s *State) allClinicalAttributes(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.Query[clinAttrRow](s.CH, clinAttrSelect+" ORDER BY clinical_attribute_id, "+s.studyOrder("study_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]ClinicalAttribute, len(rows))
	for i, r := range rows {
		out[i] = toClinicalAttribute(r)
	}
	writeJSON(w, out)
}

// ---------- Single-sample copy-number segments ----------

// sampleCopyNumberSegments — GET
// /api/studies/{studyId}/samples/{sampleId}/copy-number-segments. Returns
// []CopyNumberSeg for one sample (same shape as copyNumberSegmentsFetch).
func (s *State) sampleCopyNumberSegments(w http.ResponseWriter, r *http.Request) {
	p := ch.Params{"study": r.PathValue("studyId"), "sample": r.PathValue("sampleId")}
	chrom := ""
	if c := r.URL.Query().Get("chromosome"); c != "" {
		chrom = " AND g.chrom = {chrom:String}"
		p["chrom"] = c
	}
	rows, err := ch.QueryP[segRow](s.CH, fmt.Sprintf(
		"SELECT g.study_id AS studyId, g.sample_id AS sampleId, ifNull(sp.patient_id, '') AS patientId, "+
			"g.chrom AS chromosome, toInt64OrZero(g.start) AS start, toInt64OrZero(g.end) AS end, "+
			"toInt64OrZero(g.num_probes) AS numberOfProbes, toFloat64OrZero(g.segment_mean) AS segmentMean "+
			"FROM seg g LEFT JOIN sample sp ON g.study_id = sp.study_id AND g.sample_id = sp.sample_id "+
			"WHERE g.study_id = {study:String} AND g.sample_id = {sample:String}%s ORDER BY %s",
		chrom, s.segOrder("g.chrom, start")), p)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]CopyNumberSeg, len(rows))
	for i, r := range rows {
		out[i] = CopyNumberSeg{
			UniqueSampleKey:  uniqueKey(r.StudyID, r.SampleID),
			UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
			PatientID:        r.PatientID, SampleID: r.SampleID, StudyID: r.StudyID,
			Chromosome: r.Chromosome, Start: r.Start, End: r.End,
			NumberOfProbes: r.NumberOfProbes, SegmentMean: r.SegmentMean,
			Genes: []any{},
		}
	}
	writeJSON(w, out)
}

// writeSamples writes the samples matching a predicate over the `sample` view
// (aliased s), with type, sequenced and segment flags. where may hold
// placeholders bound in args. A sorted page is read as sqlPageOrder orders it.
func (s *State) writeSamples(w http.ResponseWriter, r *http.Request, where string, args *sqlArgs) {
	s.writeSampleRows(w, r, sampleFullSelectFrom(where)+sqlPageOrder(r, sampleSortable, sampleDefaultOrder), args)
}
