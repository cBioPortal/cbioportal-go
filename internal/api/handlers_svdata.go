package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// routeSvData registers the structural-variant detail, gene-panel-data, and
// multi-study molecular-data endpoints.
func (s *State) routeSvData(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/structural-variant/fetch", s.structuralVariantFetch)
	mux.HandleFunc("POST /api/gene-panel-data/fetch", s.genePanelDataFetch)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/gene-panel-data/fetch", s.genePanelDataProfileFetch)
	mux.HandleFunc("POST /api/molecular-data/fetch", s.molecularDataMultipleStudyFetch)
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

type svDataRow struct {
	StudyID           string `json:"studyId"`
	SampleID          string `json:"sampleId"`
	PatientID         string `json:"patientId"`
	Site1HugoSymbol   string `json:"site1HugoSymbol"`
	Site1EntrezGeneID *int64 `json:"site1EntrezGeneId"`
	Site2HugoSymbol   string `json:"site2HugoSymbol"`
	Site2EntrezGeneID *int64 `json:"site2EntrezGeneId"`
}

// svResolvedSQL is the structural_variant view with each site's missing entrez
// id resolved from its Hugo symbol through the reference genes, as the Java
// importer does.
const svResolvedSQL = "SELECT sv.study_id AS study_id, sv.sample_id AS sample_id, " +
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

func (s *State) structuralVariantFetch(w http.ResponseWriter, r *http.Request) {
	var f svFilter
	readBody(r, &f)

	var scope string
	args := newArgs()
	switch {
	case len(f.SampleMolecularIdentifiers) > 0:
		pairs := make([][2]string, len(f.SampleMolecularIdentifiers))
		for i, id := range f.SampleMolecularIdentifiers {
			pairs[i] = [2]string{svStudy(id.MolecularProfileID), id.SampleID}
		}
		scope = "(sv.study_id, sv.sample_id) IN " + args.pairs(pairs)
	case len(f.MolecularProfileIDs) > 0:
		studies := make([]string, len(f.MolecularProfileIDs))
		for i, p := range f.MolecularProfileIDs {
			studies[i] = svStudy(p)
		}
		scope = "sv.study_id IN " + args.strs(studies)
		if f.SampleListID != "" {
			scope += " AND sv.sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = " + args.str(f.SampleListID) + ")"
		}
	default:
		writeJSON(w, []StructuralVariant{})
		return
	}

	// SVs matching any entrezGeneIds gene (either site) OR any of the
	// structuralVariantQueries; neither given means no gene restriction.
	var geneOrs []string
	if len(f.EntrezGeneIDs) > 0 {
		joined := intListSQL(f.EntrezGeneIDs)
		geneOrs = append(geneOrs, fmt.Sprintf("sv.e1 IN (%s) OR sv.e2 IN (%s)", joined, joined))
	}
	for _, q := range f.StructuralVariantQueries {
		geneOrs = append(geneOrs, "("+joinPreds(q.Gene1.clause("sv.e1"), q.Gene2.clause("sv.e2"))+")")
	}
	if len(geneOrs) > 0 {
		scope += " AND (" + strings.Join(geneOrs, " OR ") + ")"
	}

	cols := svSelectColumns(s.svViewColumns())
	rows, err := queryA[map[string]any](s.CH, fmt.Sprintf(
		"SELECT %s FROM (%s) sv "+
			"LEFT JOIN sample sm ON sv.study_id = sm.study_id AND sv.sample_id = sm.sample_id "+
			"LEFT JOIN cbioportal_ref.gene g1 ON g1.entrez_gene_id = sv.e1 "+
			"LEFT JOIN cbioportal_ref.gene g2 ON g2.entrez_gene_id = sv.e2 "+
			"WHERE %s ORDER BY site1HugoSymbol, site2HugoSymbol, studyId, sampleId",
		strings.Join(cols, ", "), svSiteEntrezSQL, scope), args)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]StructuralVariant, len(rows))
	for i, r := range rows {
		out[i] = toStructuralVariant(r)
	}
	writeJSON(w, out)
}

// svTextColumns are the structural_variant view columns (left) a fetched SV
// record carries, under the field names toStructuralVariant reads (right).
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

// svSelectColumns selects a structural variant record over svSiteEntrezSQL
// (alias sv, sample alias sm, gene aliases g1 and g2) as toStructuralVariant
// reads it; view columns missing from have read as empty.
func svSelectColumns(have map[string]bool) []string {
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

// svSiteEntrezSQL is the structural_variant view with each site's entrez id
// (e1, e2) resolved from its Hugo symbol when the file gave none, as the
// importer does.
const svSiteEntrezSQL = "SELECT sv0.*, " +
	"if(sv0.site1_entrez_gene_id IS NULL AND h1.entrez_gene_id != 0, h1.entrez_gene_id, sv0.site1_entrez_gene_id) AS e1, " +
	"if(sv0.site2_entrez_gene_id IS NULL AND h2.entrez_gene_id != 0, h2.entrez_gene_id, sv0.site2_entrez_gene_id) AS e2 " +
	"FROM structural_variant sv0 " +
	"LEFT JOIN cbioportal_ref.gene h1 ON h1.hugo_gene_symbol = sv0.site1_hugo_symbol " +
	"LEFT JOIN cbioportal_ref.gene h2 ON h2.hugo_gene_symbol = sv0.site2_hugo_symbol"

// toStructuralVariant maps one fetched row (column alias -> value) to the
// Java record.
func toStructuralVariant(r map[string]any) StructuralVariant {
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
	na := func(k string) string { return naString(str(k)) }
	num := func(k string) int64 { return javaPartInt(str(k)) }
	study, sample, patient := str("studyId"), str("sampleId"), str("patientId")
	v := StructuralVariant{
		UniqueSampleKey: uniqueKey(study, sample), UniquePatientKey: uniqueKey(study, patient),
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
		NamespaceColumns: namespaceColumnsJSON(str("namespaceColumns")),
	}
	// An NA status takes the column default.
	if v.SvStatus == "" || v.SvStatus == "NA" {
		v.SvStatus = "SOMATIC"
	}
	d := newDriverAnnotation(str("driverFilter"), str("driverFilterAnn"), str("driverTiersFilter"), str("driverTiersFilterAnn"), naString)
	v.DriverFilter, v.DriverFilterAnn, v.DriverTiersFilter, v.DriverTiersFilterAnn = d.Filter, d.FilterAnn, d.TiersFilter, d.TiersFilterAnn
	return v
}

// ---------- Gene panel data (sample -> panel mapping) ----------

type GenePanelData struct {
	GenePanelID        string `json:"genePanelId,omitempty"`
	MolecularProfileID string `json:"molecularProfileId"`
	PatientID          string `json:"patientId"`
	Profiled           bool   `json:"profiled"`
	SampleID           string `json:"sampleId"`
	StudyID            string `json:"studyId"`
	UniquePatientKey   string `json:"uniquePatientKey"`
	UniqueSampleKey    string `json:"uniqueSampleKey"`
}

type gpReq struct {
	profileID string
	sampleID  string
}

type panelProfile struct {
	MolecularProfileID string `json:"molecularProfileId"`
	StudyID            string `json:"studyId"`
	StableID           string `json:"stableId"`
}

func (s *State) resolvePanelProfiles(ids []string) map[string]panelProfile {
	out := map[string]panelProfile{}
	if len(ids) == 0 {
		return out
	}
	rows, _ := cachedQueryP[panelProfile](s,
		"SELECT molecular_profile_id AS molecularProfileId, study_id AS studyId, profile_stable_id AS stableId "+
			"FROM molecular_profile WHERE molecular_profile_id IN {ids:Array(String)}", ch.Params{"ids": ids})
	for _, r := range rows {
		out[r.MolecularProfileID] = r
	}
	return out
}

func jkey(parts ...string) string { return strings.Join(parts, "\x00") }

func (s *State) genePanelDataFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		MolecularProfileIDs        []string `json:"molecularProfileIds"`
		SampleMolecularIdentifiers []struct {
			MolecularProfileID string `json:"molecularProfileId"`
			SampleID           string `json:"sampleId"`
		} `json:"sampleMolecularIdentifiers"`
	}
	readBody(r, &f)

	var reqs []gpReq
	switch {
	case len(f.SampleMolecularIdentifiers) > 0:
		for _, id := range f.SampleMolecularIdentifiers {
			reqs = append(reqs, gpReq{profileID: id.MolecularProfileID, sampleID: id.SampleID})
		}
	case len(f.MolecularProfileIDs) > 0:
		profs := s.resolvePanelProfiles(f.MolecularProfileIDs)
		for _, pid := range f.MolecularProfileIDs {
			study := profs[pid].StudyID
			if study == "" {
				continue
			}
			for _, sid := range s.studySampleIDs(study) {
				reqs = append(reqs, gpReq{profileID: pid, sampleID: sid})
			}
		}
	}
	writeJSON(w, s.buildGenePanelData(reqs))
}

func (s *State) genePanelDataProfileFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("id")
	var f struct {
		SampleIDs    []string `json:"sampleIds"`
		SampleListID string   `json:"sampleListId"`
	}
	readBody(r, &f)

	var samples []string
	switch {
	case len(f.SampleIDs) > 0:
		samples = f.SampleIDs
	case f.SampleListID != "":
		type sr struct {
			SampleID string `json:"sampleId"`
		}
		rows, _ := ch.QueryP[sr](s.CH,
			"SELECT sample_id AS sampleId FROM sample_list WHERE list_id = {list:String}", ch.Params{"list": f.SampleListID})
		for _, x := range rows {
			samples = append(samples, x.SampleID)
		}
	default:
		study := s.resolvePanelProfiles([]string{mpid})[mpid].StudyID
		if study != "" {
			samples = s.studySampleIDs(study)
		}
	}
	reqs := make([]gpReq, len(samples))
	for i, sid := range samples {
		reqs[i] = gpReq{profileID: mpid, sampleID: sid}
	}
	writeJSON(w, s.buildGenePanelData(reqs))
}

func (s *State) studySampleIDs(study string) []string {
	type sr struct {
		SampleID string `json:"sampleId"`
	}
	rows, _ := cachedQueryP[sr](s,
		"SELECT sample_id AS sampleId FROM sample WHERE study_id = {study:String} ORDER BY sample_id", ch.Params{"study": study})
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SampleID
	}
	return out
}

// buildGenePanelData resolves each (profile, sample) request against
// sample_profile: a sample is profiled when it belongs to the profile, with the
// panel it was profiled on (none for whole-exome/genome). Mutation profiles also
// count members of the study's _sequenced sample list as profiled. Samples not
// in the profile's study are skipped.
func (s *State) buildGenePanelData(reqs []gpReq) []GenePanelData {
	out := []GenePanelData{}
	if len(reqs) == 0 {
		return out
	}
	profSet := map[string]bool{}
	for _, q := range reqs {
		profSet[q.profileID] = true
	}
	profIDs := sortedKeys(profSet)
	profs := s.resolvePanelProfiles(profIDs)
	type typeRow struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	types, _ := ch.QueryP[typeRow](s.CH,
		"SELECT molecular_profile_id AS id, molecular_alteration_type AS type FROM molecular_profile WHERE molecular_profile_id IN {ids:Array(String)}",
		ch.Params{"ids": profIDs})
	isMutation := map[string]bool{}
	for _, t := range types {
		isMutation[t.ID] = t.Type == "MUTATION_EXTENDED"
	}
	studySet := map[string]bool{}
	for _, p := range profs {
		studySet[p.StudyID] = true
	}
	studies := sortedKeys(studySet)
	if len(studies) == 0 {
		return out
	}
	patientOf := map[string]string{}
	rows, _ := ch.QueryP[idRow](s.CH,
		"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM sample WHERE study_id IN {ids:Array(String)}",
		ch.Params{"ids": studies})
	for _, r := range rows {
		patientOf[jkey(r.StudyID, r.SampleID)] = r.PatientID
	}
	type memberRow struct {
		Profile  string  `json:"profile"`
		SampleID string  `json:"sampleId"`
		Panel    *string `json:"panel"`
	}
	members, _ := ch.QueryP[memberRow](s.CH,
		"SELECT molecular_profile_id AS profile, sample_id AS sampleId, gene_panel_id AS panel FROM sample_profile "+
			"WHERE molecular_profile_id IN {ids:Array(String)}", ch.Params{"ids": profIDs})
	type membership struct{ panel string }
	member := map[string]membership{}
	for _, m := range members {
		panel := ""
		if m.Panel != nil {
			panel = *m.Panel
		}
		member[jkey(m.Profile, m.SampleID)] = membership{panel}
	}
	sequenced := map[string]bool{}
	sequencedLists := make([]string, len(studies))
	for i, st := range studies {
		sequencedLists[i] = st + "_sequenced"
	}
	seqRows, _ := ch.QueryP[idRow](s.CH,
		"SELECT study_id AS studyId, sample_id AS sampleId FROM sample_list WHERE list_id IN {ids:Array(String)}",
		ch.Params{"ids": sequencedLists})
	for _, r := range seqRows {
		sequenced[jkey(r.StudyID, r.SampleID)] = true
	}
	for _, q := range reqs {
		p, ok := profs[q.profileID]
		if !ok {
			continue
		}
		patient, ok := patientOf[jkey(p.StudyID, q.sampleID)]
		if !ok {
			continue
		}
		m, profiled := member[jkey(q.profileID, q.sampleID)]
		if !profiled && isMutation[q.profileID] {
			profiled = sequenced[jkey(p.StudyID, q.sampleID)]
		}
		out = append(out, GenePanelData{
			GenePanelID:        m.panel,
			MolecularProfileID: q.profileID,
			PatientID:          patient,
			Profiled:           profiled,
			SampleID:           q.sampleID,
			StudyID:            p.StudyID,
			UniquePatientKey:   uniqueKey(p.StudyID, patient),
			UniqueSampleKey:    uniqueKey(p.StudyID, q.sampleID),
		})
	}
	return out
}

// ---------- Multi-study numeric molecular data ----------

// molecularDataMultipleStudyFetch is MolecularDataServiceImpl.getMolecularDataInMultipleMolecularProfiles:
// sample-major (requested samples by stable id, as fetchSamples with the ID
// projection returns them, all samples of the profiles by internal id, as
// getSamplesByInternalIds does); for each sample every requested profile of
// its study, then the genes in request order.
func (s *State) molecularDataMultipleStudyFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		EntrezGeneIDs              []int64  `json:"entrezGeneIds"`
		MolecularProfileIDs        []string `json:"molecularProfileIds"`
		SampleMolecularIdentifiers []struct {
			MolecularProfileID string `json:"molecularProfileId"`
			SampleID           string `json:"sampleId"`
		} `json:"sampleMolecularIdentifiers"`
	}
	readBody(r, &f)

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
	profiles := sortedKeys(profileSet)
	studyOf := map[string]string{}
	for _, p := range profiles {
		if m, ok := s.profileMeta(p); ok {
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

	geneRank := map[int64]int{}
	for _, e := range f.EntrezGeneIDs {
		if _, seen := geneRank[e]; !seen {
			geneRank[e] = len(geneRank)
		}
	}
	type cellKey struct{ profile, sample string }
	cells := map[cellKey][]MolecularData{}
	type sampleRef struct{ study, sample string }
	sampleSet := map[sampleRef]bool{}
	for _, p := range profiles {
		study, ok := studyOf[p]
		if !ok {
			continue
		}
		var sampleIDs []string
		if len(f.SampleMolecularIdentifiers) > 0 {
			sampleIDs = sortedKeys(samplesByStudy[study])
		}
		data, err := s.molecularData(p, molecularDataFilter{EntrezGeneIDs: f.EntrezGeneIDs, SampleIDs: sampleIDs})
		if err != nil {
			fail(w, err)
			return
		}
		for _, d := range data {
			k := cellKey{p, d.SampleID}
			cells[k] = append(cells[k], d)
			sampleSet[sampleRef{study, d.SampleID}] = true
		}
	}
	samples := make([]sampleKey, 0, len(sampleSet))
	for sr := range sampleSet {
		samples = append(samples, sampleKey(sr))
	}
	if len(f.SampleMolecularIdentifiers) > 0 {
		sort.Slice(samples, func(i, j int) bool {
			if samples[i].sample != samples[j].sample {
				return samples[i].sample < samples[j].sample
			}
			return samples[i].study < samples[j].study
		})
	} else {
		s.sortSamplesByImport(samples)
	}
	var out []MolecularData
	for _, sr := range samples {
		for _, p := range profiles {
			if studyOf[p] != sr.study {
				continue
			}
			rows := cells[cellKey{p, sr.sample}]
			if len(geneRank) > 0 {
				sort.SliceStable(rows, func(i, j int) bool {
					return geneRank[derefInt(rows[i].EntrezGeneID)] < geneRank[derefInt(rows[j].EntrezGeneID)]
				})
			}
			out = append(out, rows...)
		}
	}
	writeJSON(w, numericMolecularData(out, projectionOf(r)))
}

// svViewColumns lists the columns of the structural_variant view (the staged
// detail columns vary by dataset).
func (s *State) svViewColumns() map[string]bool {
	type col struct {
		Name string `json:"name"`
	}
	rows, _ := ch.Query[col](s.CH, "SELECT name FROM system.columns WHERE database = currentDatabase() AND table = 'structural_variant'")
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Name] = true
	}
	return out
}
