package molecular

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/studies"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// routeSvData registers the structural-variant detail, gene-panel-data, and
// multi-study molecular-data endpoints.
func (s Server) routeSvData(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/structural-variant/fetch", s.structuralVariantFetch)
	mux.HandleFunc("POST /api/gene-panel-data/fetch", s.Bulk().genePanelDataFetch)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/gene-panel-data/fetch", s.Bulk().genePanelDataProfileFetch)
	mux.HandleFunc("POST /api/molecular-data/fetch", s.Bulk().molecularDataMultipleStudyFetch)
}

// ---------- Structural variants (detail rows) ----------

// StructuralVariant is the Java model as Jackson writes it (NON_NULL). Text
// columns read as the importer stores them ("NA" when empty or absent),
// numeric columns as TabDelimitedFileUtil.getPartInt (-1 when empty). Site
// Hugo symbols are the reference genes' of the site entrez ids (absent
// without a gene); driver fields exist only with a driver annotation.
type StructuralVariant struct {
	UniqueSampleKey          string          `json:"uniqueSampleKey"`
	UniquePatientKey         string          `json:"uniquePatientKey"`
	MolecularProfileID       string          `json:"molecularProfileId"`
	SampleID                 string          `json:"sampleId"`
	PatientID                string          `json:"patientId"`
	StudyID                  string          `json:"studyId"`
	Site1EntrezGeneID        *int64          `json:"site1EntrezGeneId,omitempty"`
	Site1HugoSymbol          string          `json:"site1HugoSymbol"`
	Site1EnsemblTranscriptID string          `json:"site1EnsemblTranscriptId"`
	Site1Chromosome          string          `json:"site1Chromosome"`
	Site1Position            int64           `json:"site1Position"`
	Site1Contig              string          `json:"site1Contig"`
	Site1Region              string          `json:"site1Region"`
	Site1RegionNumber        int64           `json:"site1RegionNumber"`
	Site1Description         string          `json:"site1Description"`
	Site2EntrezGeneID        *int64          `json:"site2EntrezGeneId,omitempty"`
	Site2HugoSymbol          string          `json:"site2HugoSymbol"`
	Site2EnsemblTranscriptID string          `json:"site2EnsemblTranscriptId"`
	Site2Chromosome          string          `json:"site2Chromosome"`
	Site2Position            int64           `json:"site2Position"`
	Site2Contig              string          `json:"site2Contig"`
	Site2Region              string          `json:"site2Region"`
	Site2RegionNumber        int64           `json:"site2RegionNumber"`
	Site2Description         string          `json:"site2Description"`
	Site2EffectOnFrame       string          `json:"site2EffectOnFrame"`
	NcbiBuild                string          `json:"ncbiBuild"`
	DnaSupport               string          `json:"dnaSupport"`
	RnaSupport               string          `json:"rnaSupport"`
	NormalReadCount          int64           `json:"normalReadCount"`
	TumorReadCount           int64           `json:"tumorReadCount"`
	NormalVariantCount       int64           `json:"normalVariantCount"`
	TumorVariantCount        int64           `json:"tumorVariantCount"`
	NormalPairedEndReadCount int64           `json:"normalPairedEndReadCount"`
	TumorPairedEndReadCount  int64           `json:"tumorPairedEndReadCount"`
	NormalSplitReadCount     int64           `json:"normalSplitReadCount"`
	TumorSplitReadCount      int64           `json:"tumorSplitReadCount"`
	Annotation               string          `json:"annotation"`
	BreakpointType           string          `json:"breakpointType"`
	ConnectionType           string          `json:"connectionType"`
	EventInfo                string          `json:"eventInfo"`
	VariantClass             string          `json:"variantClass"`
	Length                   int64           `json:"length"`
	Comments                 string          `json:"comments"`
	DriverFilter             *string         `json:"driverFilter,omitempty"`
	DriverFilterAnn          *string         `json:"driverFilterAnn,omitempty"`
	DriverTiersFilter        *string         `json:"driverTiersFilter,omitempty"`
	DriverTiersFilterAnn     *string         `json:"driverTiersFilterAnn,omitempty"`
	SvStatus                 string          `json:"svStatus"`
	NamespaceColumns         json.RawMessage `json:"namespaceColumns,omitempty"`
}

// SVResolvedSQL is the structural_variant view with each site's missing entrez
// id resolved from its Hugo symbol through the reference genes, as the Java
// importer does.
const SVResolvedSQL = "SELECT sv.study_id AS study_id, sv.sample_id AS sample_id, " +
	"sv.site1_hugo_symbol AS site1_hugo_symbol, " +
	"if(sv.site1_entrez_gene_id IS NULL AND g1.entrez_gene_id != 0, g1.entrez_gene_id, sv.site1_entrez_gene_id) AS site1_entrez_gene_id, " +
	"sv.site2_hugo_symbol AS site2_hugo_symbol, " +
	"if(sv.site2_entrez_gene_id IS NULL AND g2.entrez_gene_id != 0, g2.entrez_gene_id, sv.site2_entrez_gene_id) AS site2_entrez_gene_id, " +
	"sv.sv_status AS sv_status, sv.cbp_driver AS cbp_driver, sv.cbp_driver_tiers AS cbp_driver_tiers " +
	"FROM structural_variant sv " +
	"LEFT JOIN cbioportal_ref.gene g1 ON g1.hugo_gene_symbol = sv.site1_hugo_symbol " +
	"LEFT JOIN cbioportal_ref.gene g2 ON g2.hugo_gene_symbol = sv.site2_hugo_symbol"

// svStudy strips the structural-variant profile suffix to recover the study id.
func svStudy(mpid string) string { return strings.TrimSuffix(mpid, "_structural_variants") }

// svGeneSubQuery is one side of a structural-variant query: an entrez id, or
// the special values ANY_GENE / NO_GENE.
type svGeneSubQuery struct {
	EntrezID     *int64 `json:"entrezId"`
	HugoSymbol   string `json:"hugoSymbol"`
	SpecialValue string `json:"specialValue"`
}

// clause renders the sub-query as a predicate over one site's entrez column
// ("" when it does not constrain the site).
func (q *svGeneSubQuery) clause(col string) string {
	switch {
	case q == nil:
		return ""
	case q.EntrezID != nil:
		return fmt.Sprintf("%s = %d", col, *q.EntrezID)
	case strings.EqualFold(q.SpecialValue, "NO_GENE"):
		return col + " IS NULL"
	}
	return ""
}

type svFilter struct {
	StructuralVariantQueries []struct {
		Gene1 *svGeneSubQuery `json:"gene1"`
		Gene2 *svGeneSubQuery `json:"gene2"`
	} `json:"structuralVariantQueries"`
	EntrezGeneIDs              []int64  `json:"entrezGeneIds"`
	MolecularProfileIDs        []string `json:"molecularProfileIds"`
	SampleListID               string   `json:"sampleListId"`
	SampleMolecularIdentifiers []struct {
		MolecularProfileID string `json:"molecularProfileId"`
		SampleID           string `json:"sampleId"`
	} `json:"sampleMolecularIdentifiers"`
}

func (s Server) structuralVariantFetch(w http.ResponseWriter, r *http.Request) {
	var f svFilter
	core.ReadBody(r, &f)

	var where string
	var studies []string
	args := core.NewArgs()
	// Present sample identifiers win over the profiles, as in Java's
	// controller: an empty list selects nothing.
	switch {
	case f.SampleMolecularIdentifiers != nil && len(f.SampleMolecularIdentifiers) == 0:
		core.WriteJSON(w, []StructuralVariant{})
		return
	case len(f.SampleMolecularIdentifiers) > 0:
		pairs := make([][2]string, len(f.SampleMolecularIdentifiers))
		for i, id := range f.SampleMolecularIdentifiers {
			pairs[i] = [2]string{svStudy(id.MolecularProfileID), id.SampleID}
			studies = append(studies, pairs[i][0])
		}
		where = "(sv.study_id, sv.sample_id) IN " + args.Pairs(pairs)
	case len(f.MolecularProfileIDs) > 0:
		studies = make([]string, len(f.MolecularProfileIDs))
		for i, p := range f.MolecularProfileIDs {
			studies[i] = svStudy(p)
		}
		where = "sv.study_id IN " + args.Strs(studies)
		if f.SampleListID != "" {
			where += " AND sv.sample_id IN " + s.BySampleList(f.SampleListID).ListMembers(args)
		}
	default:
		core.WriteJSON(w, []StructuralVariant{})
		return
	}

	// SVs matching any entrezGeneIds gene (either site) OR any of the
	// structuralVariantQueries; neither given means no gene restriction.
	var geneOrs []string
	if len(f.EntrezGeneIDs) > 0 {
		joined := core.IntListSQL(f.EntrezGeneIDs)
		geneOrs = append(geneOrs, fmt.Sprintf("sv.e1 IN (%s) OR sv.e2 IN (%s)", joined, joined))
	}
	for _, q := range f.StructuralVariantQueries {
		geneOrs = append(geneOrs, "("+JoinPreds(q.Gene1.clause("sv.e1"), q.Gene2.clause("sv.e2"))+")")
	}
	if len(geneOrs) > 0 {
		where += " AND (" + strings.Join(geneOrs, " OR ") + ")"
	}

	viewCols, err := s.SVViewColumns()
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	cols := SVSelectColumns(viewCols)
	order := " ORDER BY site1HugoSymbol, site2HugoSymbol, studyId, sampleId"
	if sp, expr := core.ClaimListPage(r, structuralVariantSortable); sp != nil {
		// Records of one gene pair in a sample tie in that order; a page
		// breaks the ties by every field, so pages do not overlap.
		tie := []string{"site1HugoSymbol", "site2HugoSymbol", "studyId", "sampleId"}
		for _, c := range cols {
			if _, alias, ok := strings.Cut(c, " AS "); ok && !slices.Contains(tie, alias) {
				tie = append(tie, alias)
			}
		}
		order = sp.OrderLimit(expr, strings.Join(tie, ", "))
	}
	query := fmt.Sprintf(
		"SELECT %s FROM (%s) sv "+
			"LEFT JOIN %s sm ON sv.study_id = sm.study_id AND sv.sample_id = sm.sample_id "+
			"LEFT JOIN cbioportal_ref.gene g1 ON g1.entrez_gene_id = sv.e1 "+
			"LEFT JOIN cbioportal_ref.gene g2 ON g2.entrez_gene_id = sv.e2 "+
			"WHERE %s%s",
		strings.Join(cols, ", "), SVSiteEntrezSQL, core.ByStudies(studies).Samples(args), where, order)
	out := core.NewJSONArray(w, r)
	err = ch.QueryEachP(s.CH, query, args.Params(query), func(r *map[string]any) error {
		return out.Add(ToStructuralVariant(*r))
	})
	out.Finish(err)
}

// structuralVariantSortable are the sortBy fields of structural variants
// over SVSelectColumns' aliases, as the records carry them: counts and
// positions as numbers (-1 when empty), empty text as "NA".
var structuralVariantSortable = map[string]string{
	"sampleId": "sampleId", "patientId": "patientId", "studyId": "studyId", "molecularProfileId": "molecularProfileId",
	"site1EntrezGeneId": "site1EntrezGeneId", "site1HugoSymbol": "site1HugoSymbol", "site1Chromosome": svNA("site1Chromosome"),
	"site1Position":     svNum("site1Position"),
	"site2EntrezGeneId": "site2EntrezGeneId", "site2HugoSymbol": "site2HugoSymbol", "site2Chromosome": svNA("site2Chromosome"),
	"site2Position": svNum("site2Position"),
	"variantClass":  svNA("variantClass"), "eventInfo": svNA("eventInfo"), "svStatus": "svStatus",
	"tumorVariantCount": svNum("tumorVariantCount"), "tumorReadCount": svNum("tumorReadCount"),
}

// svNA is a text column as core.NAString reads it.
func svNA(col string) string { return fmt.Sprintf("if(%[1]s = '', 'NA', %[1]s)", col) }

// svNum is a numeric column as javaPartInt reads it, for sorting.
func svNum(col string) string { return fmt.Sprintf("ifNull(toFloat64OrNull(trimBoth(%s)), -1)", col) }

// svTextColumns are the structural_variant view columns (left) a fetched SV
// record carries, under the field names ToStructuralVariant reads (right).
var svTextColumns = [][2]string{
	{"site1_ensembl_transcript_id", "site1EnsemblTranscriptId"}, {"site1_chromosome", "site1Chromosome"},
	{"site1_position", "site1Position"}, {"site1_contig", "site1Contig"}, {"site1_region", "site1Region"},
	{"site1_region_number", "site1RegionNumber"}, {"site1_description", "site1Description"},
	{"site2_ensembl_transcript_id", "site2EnsemblTranscriptId"}, {"site2_chromosome", "site2Chromosome"},
	{"site2_position", "site2Position"}, {"site2_contig", "site2Contig"}, {"site2_region", "site2Region"},
	{"site2_region_number", "site2RegionNumber"}, {"site2_description", "site2Description"},
	{"site2_effect_on_frame", "site2EffectOnFrame"}, {"ncbi_build", "ncbiBuild"},
	{"dna_support", "dnaSupport"}, {"rna_support", "rnaSupport"},
	{"normal_read_count", "normalReadCount"}, {"tumor_read_count", "tumorReadCount"},
	{"normal_variant_count", "normalVariantCount"}, {"tumor_variant_count", "tumorVariantCount"},
	{"normal_paired_end_read_count", "normalPairedEndReadCount"}, {"tumor_paired_end_read_count", "tumorPairedEndReadCount"},
	{"normal_split_read_count", "normalSplitReadCount"}, {"tumor_split_read_count", "tumorSplitReadCount"},
	{"annotation", "annotation"}, {"breakpoint_type", "breakpointType"}, {"connection_type", "connectionType"},
	{"event_info", "eventInfo"}, {"class", "variantClass"}, {"sv_length", "length"}, {"comments", "comments"},
	{"cbp_driver", "driverFilter"}, {"cbp_driver_annotation", "driverFilterAnn"},
	{"cbp_driver_tiers", "driverTiersFilter"}, {"cbp_driver_tiers_annotation", "driverTiersFilterAnn"},
	{"sv_status", "svStatus"}, {"namespace_columns", "namespaceColumns"},
}

// SVSelectColumns selects a structural variant record over SVSiteEntrezSQL
// (alias sv, sample alias sm, gene aliases g1 and g2) as ToStructuralVariant
// reads it; view columns missing from have read as empty.
func SVSelectColumns(have map[string]bool) []string {
	cols := []string{
		"sv.study_id AS studyId", "sv.sample_id AS sampleId", "ifNull(sm.patient_id, '') AS patientId",
		"sv.e1 AS site1EntrezGeneId", "g1.hugo_gene_symbol AS site1HugoSymbol",
		"sv.e2 AS site2EntrezGeneId", "g2.hugo_gene_symbol AS site2HugoSymbol",
		"concat(sv.study_id, '_', sv.profile_stable_id) AS molecularProfileId",
	}
	for _, c := range svTextColumns {
		if have[c[0]] {
			cols = append(cols, fmt.Sprintf("ifNull(toString(sv.%s), '') AS %s", c[0], c[1]))
		} else {
			cols = append(cols, "'' AS "+c[1])
		}
	}
	return cols
}

// SVSiteEntrezSQL is the structural_variant view with each site's entrez id
// (e1, e2) resolved from its Hugo symbol when the file gave none, as the
// importer does.
const SVSiteEntrezSQL = "SELECT sv0.*, " +
	"if(sv0.site1_entrez_gene_id IS NULL AND h1.entrez_gene_id != 0, h1.entrez_gene_id, sv0.site1_entrez_gene_id) AS e1, " +
	"if(sv0.site2_entrez_gene_id IS NULL AND h2.entrez_gene_id != 0, h2.entrez_gene_id, sv0.site2_entrez_gene_id) AS e2 " +
	"FROM structural_variant sv0 " +
	"LEFT JOIN cbioportal_ref.gene h1 ON h1.hugo_gene_symbol = sv0.site1_hugo_symbol " +
	"LEFT JOIN cbioportal_ref.gene h2 ON h2.hugo_gene_symbol = sv0.site2_hugo_symbol"

// ToStructuralVariant maps one fetched row (column alias -> value) to the
// Java record.
func ToStructuralVariant(r map[string]any) StructuralVariant {
	str := func(k string) string {
		v, _ := r[k].(string)
		return v
	}
	entrez := func(k string) *int64 {
		switch v := r[k].(type) {
		case float64:
			n := int64(v)
			return &n
		case string:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return &n
			}
		}
		return nil
	}
	na := func(k string) string { return core.NAString(str(k)) }
	num := func(k string) int64 { return core.JavaPartInt(str(k)) }
	study, sample, patient := str("studyId"), str("sampleId"), str("patientId")
	v := StructuralVariant{
		UniqueSampleKey: core.UniqueKey(study, sample), UniquePatientKey: core.UniqueKey(study, patient),
		MolecularProfileID: str("molecularProfileId"), SampleID: sample, PatientID: patient, StudyID: study,
		Site1EntrezGeneID: entrez("site1EntrezGeneId"), Site1HugoSymbol: str("site1HugoSymbol"),
		Site1EnsemblTranscriptID: na("site1EnsemblTranscriptId"), Site1Chromosome: na("site1Chromosome"),
		Site1Position: num("site1Position"), Site1Contig: na("site1Contig"), Site1Region: na("site1Region"),
		Site1RegionNumber: num("site1RegionNumber"), Site1Description: na("site1Description"),
		Site2EntrezGeneID: entrez("site2EntrezGeneId"), Site2HugoSymbol: str("site2HugoSymbol"),
		Site2EnsemblTranscriptID: na("site2EnsemblTranscriptId"), Site2Chromosome: na("site2Chromosome"),
		Site2Position: num("site2Position"), Site2Contig: na("site2Contig"), Site2Region: na("site2Region"),
		Site2RegionNumber: num("site2RegionNumber"), Site2Description: na("site2Description"),
		Site2EffectOnFrame: na("site2EffectOnFrame"), NcbiBuild: na("ncbiBuild"),
		DnaSupport: na("dnaSupport"), RnaSupport: na("rnaSupport"),
		NormalReadCount: num("normalReadCount"), TumorReadCount: num("tumorReadCount"),
		NormalVariantCount: num("normalVariantCount"), TumorVariantCount: num("tumorVariantCount"),
		NormalPairedEndReadCount: num("normalPairedEndReadCount"), TumorPairedEndReadCount: num("tumorPairedEndReadCount"),
		NormalSplitReadCount: num("normalSplitReadCount"), TumorSplitReadCount: num("tumorSplitReadCount"),
		Annotation: na("annotation"), BreakpointType: na("breakpointType"), ConnectionType: na("connectionType"),
		EventInfo: na("eventInfo"), VariantClass: na("variantClass"), Length: num("length"), Comments: na("comments"),
		SvStatus:         str("svStatus"),
		NamespaceColumns: core.NamespaceColumnsJSON(str("namespaceColumns")),
	}
	// An NA status takes the column default.
	if v.SvStatus == "" || v.SvStatus == "NA" {
		v.SvStatus = "SOMATIC"
	}
	d := core.NewDriverAnnotation(str("driverFilter"), str("driverFilterAnn"), str("driverTiersFilter"), str("driverTiersFilterAnn"), core.NAString)
	v.DriverFilter, v.DriverFilterAnn, v.DriverTiersFilter, v.DriverTiersFilterAnn = d.Filter, d.FilterAnn, d.TiersFilter, d.TiersFilterAnn
	return v
}

// ---------- Gene panel data (sample -> panel mapping) ----------

type GenePanelData struct {
	UniqueSampleKey    string `json:"uniqueSampleKey"`
	UniquePatientKey   string `json:"uniquePatientKey"`
	MolecularProfileID string `json:"molecularProfileId"`
	SampleID           string `json:"sampleId"`
	PatientID          string `json:"patientId"`
	StudyID            string `json:"studyId"`
	GenePanelID        string `json:"genePanelId,omitempty"`
	Profiled           bool   `json:"profiled"`
}

type gpReq struct {
	profileID string
	sampleID  string
}

// PanelProfile is a molecular profile's id, study and stable id.
type PanelProfile struct {
	MolecularProfileID string `json:"molecularProfileId"`
	StudyID            string `json:"studyId"`
	StableID           string `json:"stableId"`
}

// ResolvePanelProfiles looks up the molecular profiles ids (cached), keyed
// by id; unknown ids are left out.
func (s Server) ResolvePanelProfiles(ids []string) (map[string]PanelProfile, error) {
	out := map[string]PanelProfile{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := core.CachedQueryP[PanelProfile](s.State,
		"SELECT molecular_profile_id AS molecularProfileId, study_id AS studyId, profile_stable_id AS stableId "+
			"FROM molecular_profile WHERE molecular_profile_id IN {ids:Array(String)}", ch.Params{"ids": ids})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.MolecularProfileID] = r
	}
	return out, nil
}

// JoinKey joins parts into one map key.
func JoinKey(parts ...string) string { return strings.Join(parts, "\x00") }

func (s Server) genePanelDataFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		MolecularProfileIDs        []string `json:"molecularProfileIds"`
		SampleMolecularIdentifiers []struct {
			MolecularProfileID string `json:"molecularProfileId"`
			SampleID           string `json:"sampleId"`
		} `json:"sampleMolecularIdentifiers"`
	}
	core.ReadBody(r, &f)

	var reqs []gpReq
	switch {
	case len(f.SampleMolecularIdentifiers) > 0:
		for _, id := range f.SampleMolecularIdentifiers {
			reqs = append(reqs, gpReq{profileID: id.MolecularProfileID, sampleID: id.SampleID})
		}
	case len(f.MolecularProfileIDs) > 0:
		profs, err := s.ResolvePanelProfiles(f.MolecularProfileIDs)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		for _, pid := range f.MolecularProfileIDs {
			study := profs[pid].StudyID
			if study == "" {
				continue
			}
			sampleIDs, err := s.studySampleIDs(study)
			if err != nil {
				core.Fail(w, r, err)
				return
			}
			for _, sid := range sampleIDs {
				reqs = append(reqs, gpReq{profileID: pid, sampleID: sid})
			}
		}
	}
	s.writeGenePanelData(w, r, reqs)
}

// writeGenePanelData streams genePanelDataEach's records.
func (s Server) writeGenePanelData(w http.ResponseWriter, r *http.Request, reqs []gpReq) {
	out := core.NewJSONArray(w, r)
	out.Finish(s.genePanelDataEach(reqs, func(d GenePanelData) error {
		return out.Add(d)
	}))
}

func (s Server) genePanelDataProfileFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("id")
	var f struct {
		SampleIDs    []string `json:"sampleIds"`
		SampleListID string   `json:"sampleListId"`
	}
	core.ReadBody(r, &f)
	if core.NoSamples(f.SampleIDs, f.SampleListID) {
		core.WriteJSON(w, []struct{}{})
		return
	}

	var samples []string
	switch {
	case len(f.SampleIDs) > 0:
		samples = f.SampleIDs
	case f.SampleListID != "":
		type sr struct {
			SampleID string `json:"sampleId"`
		}
		p := ch.Params{}
		sql := "SELECT sample_id AS sampleId FROM " + s.BySampleList(f.SampleListID).From(core.ParamsBinder(p), core.TSampleList)
		if core.PageAsked(r) {
			// Pages need a stable order.
			sql += " ORDER BY sample_id"
		}
		rows, err := ch.QueryP[sr](s.CH, sql, p)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		for _, x := range rows {
			samples = append(samples, x.SampleID)
		}
	default:
		profs, err := s.ResolvePanelProfiles([]string{mpid})
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		if study := profs[mpid].StudyID; study != "" {
			if samples, err = s.studySampleIDs(study); err != nil {
				core.Fail(w, r, err)
				return
			}
		}
	}
	reqs := make([]gpReq, len(samples))
	for i, sid := range samples {
		reqs[i] = gpReq{profileID: mpid, sampleID: sid}
	}
	s.writeGenePanelData(w, r, reqs)
}

func (s Server) studySampleIDs(study string) ([]string, error) {
	type sr struct {
		SampleID string `json:"sampleId"`
	}
	p := ch.Params{}
	rows, err := core.CachedQueryP[sr](s.State, "SELECT sample_id AS sampleId FROM "+core.ByStudy(study).From(core.ParamsBinder(p), core.TSample)+" ORDER BY sample_id", p)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SampleID
	}
	return out, nil
}

// genePanelDataEach resolves each (profile, sample) request against
// sample_profile and calls fn with its record, in request order: a sample is
// profiled when it belongs to the profile, with the panel it was profiled on
// (none for whole-exome/genome). Mutation profiles also count members of the
// study's _sequenced sample list as profiled. Samples not in the profile's
// study are skipped.
func (s Server) genePanelDataEach(reqs []gpReq, fn func(GenePanelData) error) error {
	if len(reqs) == 0 {
		return nil
	}
	profSet := map[string]bool{}
	for _, q := range reqs {
		profSet[q.profileID] = true
	}
	profIDs := core.SortedKeys(profSet)
	profs, err := s.ResolvePanelProfiles(profIDs)
	if err != nil {
		return err
	}
	type typeRow struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	types, err := ch.QueryP[typeRow](s.CH,
		"SELECT molecular_profile_id AS id, molecular_alteration_type AS type FROM molecular_profile WHERE molecular_profile_id IN {ids:Array(String)}",
		ch.Params{"ids": profIDs})
	if err != nil {
		return err
	}
	isMutation := map[string]bool{}
	for _, t := range types {
		isMutation[t.ID] = t.Type == "MUTATION_EXTENDED"
	}
	studySet := map[string]bool{}
	for _, p := range profs {
		studySet[p.StudyID] = true
	}
	studyIDs := core.SortedKeys(studySet)
	if len(studyIDs) == 0 {
		return nil
	}
	patientOf := map[string]string{}
	sp := ch.Params{}
	err = ch.QueryEachP(s.CH,
		"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM "+core.ByStudies(studyIDs).From(core.ParamsBinder(sp), core.TSample), sp,
		func(r *studies.IDRow) error {
			patientOf[JoinKey(r.StudyID, r.SampleID)] = r.PatientID
			return nil
		})
	if err != nil {
		return err
	}
	type memberRow struct {
		Profile  string  `json:"profile"`
		SampleID string  `json:"sampleId"`
		Panel    *string `json:"panel"`
	}
	type membership struct{ panel string }
	member := map[string]membership{}
	err = ch.QueryEachP(s.CH,
		"SELECT molecular_profile_id AS profile, sample_id AS sampleId, gene_panel_id AS panel FROM sample_profile "+
			"WHERE study_id IN {studies:Array(String)} AND molecular_profile_id IN {ids:Array(String)}",
		ch.Params{"studies": studyIDs, "ids": profIDs},
		func(m *memberRow) error {
			panel := ""
			if m.Panel != nil {
				panel = *m.Panel
			}
			member[JoinKey(m.Profile, m.SampleID)] = membership{panel}
			return nil
		})
	if err != nil {
		return err
	}
	sequenced := map[string]bool{}
	sequencedLists := make([]string, len(studyIDs))
	for i, st := range studyIDs {
		sequencedLists[i] = st + "_sequenced"
	}
	seqP := ch.Params{}
	seqRows, err := ch.QueryP[studies.IDRow](s.CH,
		"SELECT study_id AS studyId, sample_id AS sampleId FROM "+s.BySampleLists(sequencedLists...).From(core.ParamsBinder(seqP), core.TSampleList), seqP)
	if err != nil {
		return err
	}
	for _, r := range seqRows {
		sequenced[JoinKey(r.StudyID, r.SampleID)] = true
	}
	for _, q := range reqs {
		p, ok := profs[q.profileID]
		if !ok {
			continue
		}
		patient, ok := patientOf[JoinKey(p.StudyID, q.sampleID)]
		if !ok {
			continue
		}
		m, profiled := member[JoinKey(q.profileID, q.sampleID)]
		if !profiled && isMutation[q.profileID] {
			profiled = sequenced[JoinKey(p.StudyID, q.sampleID)]
		}
		err := fn(GenePanelData{
			GenePanelID:        m.panel,
			MolecularProfileID: q.profileID,
			PatientID:          patient,
			Profiled:           profiled,
			SampleID:           q.sampleID,
			StudyID:            p.StudyID,
			UniquePatientKey:   core.UniqueKey(p.StudyID, patient),
			UniqueSampleKey:    core.UniqueKey(p.StudyID, q.sampleID),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// ---------- Multi-study numeric molecular data ----------

// molecularDataMultipleStudyFetch is MolecularDataServiceImpl.getMolecularDataInMultipleMolecularProfiles:
// sample-major (requested samples by stable id, as fetchSamples with the ID
// projection returns them, all samples of the profiles by internal id, as
// getSamplesByInternalIds does); for each sample every requested profile of
// its study by stable id (getMolecularProfiles), then the genes in request
// order. One query orders the profiles' records so, and they are streamed.
func (s Server) molecularDataMultipleStudyFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		EntrezGeneIDs              []int64  `json:"entrezGeneIds"`
		MolecularProfileIDs        []string `json:"molecularProfileIds"`
		SampleMolecularIdentifiers []struct {
			MolecularProfileID string `json:"molecularProfileId"`
			SampleID           string `json:"sampleId"`
		} `json:"sampleMolecularIdentifiers"`
	}
	core.ReadBody(r, &f)

	profileSet := map[string]bool{}
	samplesByStudy := map[string]map[string]bool{}
	if len(f.SampleMolecularIdentifiers) > 0 {
		for _, id := range f.SampleMolecularIdentifiers {
			profileSet[id.MolecularProfileID] = true
		}
	} else {
		for _, p := range f.MolecularProfileIDs {
			profileSet[p] = true
		}
	}
	profiles := core.SortedKeys(profileSet)
	studyOf := map[string]string{}
	for _, p := range profiles {
		if m, ok := s.ProfileMeta(p); ok {
			studyOf[p] = m.StudyID
		}
	}
	for _, id := range f.SampleMolecularIdentifiers {
		if st, ok := studyOf[id.MolecularProfileID]; ok {
			if samplesByStudy[st] == nil {
				samplesByStudy[st] = map[string]bool{}
			}
			samplesByStudy[st][id.SampleID] = true
		}
	}

	// Samples sort by internal id (where the database carries them) unless
	// requested by id, then by stable id and study.
	sampleRank := "toUInt64(0)"
	if len(f.SampleMolecularIdentifiers) == 0 && s.ImportOrdered() {
		sampleRank = "toUInt64(s.internal_id)"
	}
	args := core.NewArgs()
	var parts []string
	for _, p := range profiles {
		study, ok := studyOf[p]
		if !ok {
			continue
		}
		var sampleIDs []string
		if len(f.SampleMolecularIdentifiers) > 0 {
			sampleIDs = core.SortedKeys(samplesByStudy[study])
		}
		extra := fmt.Sprintf(", %s AS molecularProfileId, %s AS studyId, %s AS sampleRank, rg.genetic_entity_id AS entityRank",
			args.Str(p), args.Str(study), sampleRank)
		sel, _, ok, err := s.molecularDataSelect(args, p, molecularDataFilter{EntrezGeneIDs: f.EntrezGeneIDs, SampleIDs: sampleIDs}, extra)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		if ok {
			parts = append(parts, sel)
		}
	}
	if core.ProjectionOf(r) == "META" && len(parts) > 0 {
		sql := "SELECT count() AS n FROM (" + strings.Join(parts, " UNION ALL ") + ") WHERE " + core.JavaNumericSQL("value")
		core.WriteMetaCount(w, r, s.CH, sql, args.Params(sql))
		return
	}
	out := core.NewJSONArray(w, r)
	if len(parts) == 0 {
		out.Finish(nil)
		return
	}
	// Within a cell, genes in request order (their first occurrence), else
	// as a single profile orders them.
	geneOrder := ""
	if len(f.EntrezGeneIDs) > 0 {
		geneOrder = "indexOf([" + core.IntListSQL(f.EntrezGeneIDs) + "], entrezGeneId), "
	}
	order := "sampleRank, sampleId, studyId, molecularProfileId, " + geneOrder + "entityRank, entrezGeneId"
	query := "SELECT sampleId, patientId, entrezGeneId, hugoGeneSymbol, geneType, value, molecularProfileId, studyId FROM (" +
		strings.Join(parts, " UNION ALL ") + ")"
	// A page holds the records the numeric filter below keeps.
	if sp, expr := core.ClaimListPage(r, molecularDataMultiSortable); sp != nil {
		query += " WHERE " + core.JavaNumericSQL("value") + sp.OrderLimit(expr, order)
	} else {
		query += " ORDER BY " + order
	}
	projection := core.ProjectionOf(r)
	err := ch.QueryEachP(s.CH, query, args.Params(query), func(row *molecularDataMultiRow) error {
		d := MolecularData{
			MolecularProfileID: row.MolecularProfileID, SampleID: row.SampleID, PatientID: row.PatientID, EntrezGeneID: row.EntrezGeneID,
			Gene:    Gene{EntrezGeneID: row.EntrezGeneID, HugoGeneSymbol: row.HugoGeneSymbol, Type: row.GeneType},
			StudyID: row.StudyID, Value: row.Value,
			UniqueSampleKey: core.UniqueKey(row.StudyID, row.SampleID), UniquePatientKey: core.UniqueKey(row.StudyID, row.PatientID),
		}
		if rec, ok := numericRecord(d, projection); ok {
			return out.Add(rec)
		}
		return nil
	})
	out.Finish(err)
}

// molecularDataMultiSortable are molecularDataSortable over the columns of
// the multi-profile fetch.
var molecularDataMultiSortable = map[string]string{
	"entrezGeneId": "entrezGeneId", "sampleId": "sampleId", "patientId": "patientId",
	"studyId": "studyId", "molecularProfileId": "molecularProfileId", "value": "toFloat64OrNull(value)",
}

// molecularDataMultiRow is a molecularDataRow with its profile and study.
type molecularDataMultiRow struct {
	SampleID           string `json:"sampleId"`
	PatientID          string `json:"patientId"`
	EntrezGeneID       *int64 `json:"entrezGeneId"`
	HugoGeneSymbol     string `json:"hugoGeneSymbol"`
	GeneType           string `json:"geneType"`
	Value              string `json:"value"`
	MolecularProfileID string `json:"molecularProfileId"`
	StudyID            string `json:"studyId"`
}

// SVViewColumns lists the columns of the structural_variant view (the staged
// detail columns vary by dataset).
func (s Server) SVViewColumns() (map[string]bool, error) {
	type col struct {
		Name string `json:"name"`
	}
	rows, err := ch.Query[col](s.CH, "SELECT name FROM system.columns WHERE database = currentDatabase() AND table = 'structural_variant'")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Name] = true
	}
	return out, nil
}

// JoinPreds joins the non-empty SQL predicates with AND ("1 = 1" for none).
func JoinPreds(preds ...string) string {
	var out []string
	for _, p := range preds {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return "1 = 1"
	}
	return strings.Join(out, " AND ")
}
