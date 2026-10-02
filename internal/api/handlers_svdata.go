package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
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

// StructuralVariant mirrors the Java StructuralVariant JSON shape. Only a subset
// of fields is populated from the transform view; the rest are left null/empty,
// matching how the Java backend reports absent optional columns.
type StructuralVariant struct {
	Annotation               string          `json:"annotation"`
	BreakpointType           string          `json:"breakpointType"`
	Comments                 string          `json:"comments"`
	ConnectionType           string          `json:"connectionType"`
	DnaSupport               string          `json:"dnaSupport"`
	DriverFilter             string          `json:"driverFilter"`
	DriverFilterAnn          string          `json:"driverFilterAnn"`
	DriverTiersFilter        string          `json:"driverTiersFilter"`
	DriverTiersFilterAnn     string          `json:"driverTiersFilterAnn"`
	EventInfo                string          `json:"eventInfo"`
	Length                   *int64          `json:"length"`
	MolecularProfileID       string          `json:"molecularProfileId"`
	NamespaceColumns         json.RawMessage `json:"namespaceColumns"`
	NcbiBuild                string          `json:"ncbiBuild"`
	NormalPairedEndReadCount *int64          `json:"normalPairedEndReadCount"`
	NormalReadCount          *int64          `json:"normalReadCount"`
	NormalSplitReadCount     *int64          `json:"normalSplitReadCount"`
	NormalVariantCount       *int64          `json:"normalVariantCount"`
	PatientID                string          `json:"patientId"`
	RnaSupport               string          `json:"rnaSupport"`
	SampleID                 string          `json:"sampleId"`
	Site1Chromosome          string          `json:"site1Chromosome"`
	Site1Contig              string          `json:"site1Contig"`
	Site1Description         string          `json:"site1Description"`
	Site1EnsemblTranscriptID string          `json:"site1EnsemblTranscriptId"`
	Site1EntrezGeneID        *int64          `json:"site1EntrezGeneId"`
	Site1HugoSymbol          string          `json:"site1HugoSymbol"`
	Site1Position            *int64          `json:"site1Position"`
	Site1Region              string          `json:"site1Region"`
	Site1RegionNumber        *int64          `json:"site1RegionNumber"`
	Site2Chromosome          string          `json:"site2Chromosome"`
	Site2Contig              string          `json:"site2Contig"`
	Site2Description         string          `json:"site2Description"`
	Site2EffectOnFrame       string          `json:"site2EffectOnFrame"`
	Site2EnsemblTranscriptID string          `json:"site2EnsemblTranscriptId"`
	Site2EntrezGeneID        *int64          `json:"site2EntrezGeneId"`
	Site2HugoSymbol          string          `json:"site2HugoSymbol"`
	Site2Position            *int64          `json:"site2Position"`
	Site2Region              string          `json:"site2Region"`
	Site2RegionNumber        *int64          `json:"site2RegionNumber"`
	StudyID                  string          `json:"studyId"`
	SvStatus                 string          `json:"svStatus"`
	TumorPairedEndReadCount  *int64          `json:"tumorPairedEndReadCount"`
	TumorReadCount           *int64          `json:"tumorReadCount"`
	TumorSplitReadCount      *int64          `json:"tumorSplitReadCount"`
	TumorVariantCount        *int64          `json:"tumorVariantCount"`
	UniquePatientKey         string          `json:"uniquePatientKey"`
	UniqueSampleKey          string          `json:"uniqueSampleKey"`
	VariantClass             string          `json:"variantClass"`
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
	switch {
	case len(f.SampleMolecularIdentifiers) > 0:
		pairs := make([][2]string, len(f.SampleMolecularIdentifiers))
		for i, id := range f.SampleMolecularIdentifiers {
			pairs[i] = [2]string{svStudy(id.MolecularProfileID), id.SampleID}
		}
		scope = fmt.Sprintf("(sv.study_id, sv.sample_id) IN (%s)", tupleIn(pairs))
	case len(f.MolecularProfileIDs) > 0:
		studies := make([]string, len(f.MolecularProfileIDs))
		for i, p := range f.MolecularProfileIDs {
			studies[i] = svStudy(p)
		}
		scope = fmt.Sprintf("sv.study_id IN (%s)", inList(studies))
		if f.SampleListID != "" {
			scope += fmt.Sprintf(" AND sv.sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(f.SampleListID))
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
		geneOrs = append(geneOrs, fmt.Sprintf("sv.site1_entrez_gene_id IN (%s) OR sv.site2_entrez_gene_id IN (%s)", joined, joined))
	}
	for _, q := range f.StructuralVariantQueries {
		geneOrs = append(geneOrs, "("+joinPreds(q.Gene1.clause("sv.site1_entrez_gene_id"), q.Gene2.clause("sv.site2_entrez_gene_id"))+")")
	}
	if len(geneOrs) > 0 {
		scope += " AND (" + strings.Join(geneOrs, " OR ") + ")"
	}

	type svDetailRow struct {
		StructuralVariant
		NamespaceColumns string `json:"namespaceColumns"`
	}
	num := func(col, alias string) string { return fmt.Sprintf("toInt64OrNull(toString(sv.%s)) AS %s", col, alias) }
	str := func(col, alias string) string { return fmt.Sprintf("toString(sv.%s) AS %s", col, alias) }
	cols := []string{
		"sv.study_id AS studyId", "sv.sample_id AS sampleId", "ifNull(sm.patient_id, '') AS patientId",
		"sv.site1_hugo_symbol AS site1HugoSymbol", "sv.site1_entrez_gene_id AS site1EntrezGeneId",
		"sv.site2_hugo_symbol AS site2HugoSymbol", "sv.site2_entrez_gene_id AS site2EntrezGeneId",
		"concat(sv.study_id, '_', sv.profile_stable_id) AS molecularProfileId",
		"sv.sv_status AS svStatus", "sv.cbp_driver AS driverFilter", "sv.cbp_driver_tiers AS driverTiersFilter",
		"sv.namespace_columns AS namespaceColumns",
	}
	have := s.svViewColumns()
	for _, c := range [][3]string{
		{"str", "site1_chromosome", "site1Chromosome"}, {"num", "site1_position", "site1Position"},
		{"str", "site1_region", "site1Region"}, {"num", "site1_region_number", "site1RegionNumber"},
		{"str", "site1_description", "site1Description"}, {"str", "site1_ensembl_transcript_id", "site1EnsemblTranscriptId"},
		{"str", "site2_chromosome", "site2Chromosome"}, {"num", "site2_position", "site2Position"},
		{"str", "site2_region", "site2Region"}, {"num", "site2_region_number", "site2RegionNumber"},
		{"str", "site2_description", "site2Description"}, {"str", "site2_ensembl_transcript_id", "site2EnsemblTranscriptId"},
		{"str", "site2_effect_on_frame", "site2EffectOnFrame"}, {"str", "site2_contig", "site2Contig"},
		{"str", "dna_support", "dnaSupport"}, {"str", "rna_support", "rnaSupport"}, {"str", "event_info", "eventInfo"},
		{"str", "ncbi_build", "ncbiBuild"}, {"str", "class", "variantClass"}, {"num", "sv_length", "length"},
		{"str", "connection_type", "connectionType"}, {"str", "breakpoint_type", "breakpointType"},
		{"str", "annotation", "annotation"}, {"str", "comments", "comments"},
		{"num", "normal_read_count", "normalReadCount"}, {"num", "normal_variant_count", "normalVariantCount"},
		{"num", "normal_paired_end_read_count", "normalPairedEndReadCount"}, {"num", "normal_split_read_count", "normalSplitReadCount"},
		{"num", "tumor_read_count", "tumorReadCount"}, {"num", "tumor_variant_count", "tumorVariantCount"},
		{"num", "tumor_paired_end_read_count", "tumorPairedEndReadCount"}, {"num", "tumor_split_read_count", "tumorSplitReadCount"},
		{"str", "cbp_driver_annotation", "driverFilterAnn"}, {"str", "cbp_driver_tiers_annotation", "driverTiersFilterAnn"},
	} {
		if !have[c[1]] {
			continue
		}
		if c[0] == "num" {
			cols = append(cols, num(c[1], c[2]))
		} else {
			cols = append(cols, str(c[1], c[2]))
		}
	}
	rows, err := ch.Query[svDetailRow](s.CH, fmt.Sprintf(
		"SELECT %s FROM structural_variant sv "+
			"LEFT JOIN sample sm ON sv.study_id = sm.study_id AND sv.sample_id = sm.sample_id "+
			"WHERE %s ORDER BY sv.study_id, sv.sample_id", strings.Join(cols, ", "), scope))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]StructuralVariant, len(rows))
	for i, r := range rows {
		v := r.StructuralVariant
		v.NamespaceColumns = rawJSONOrEmpty(r.NamespaceColumns)
		v.UniqueSampleKey = uniqueKey(v.StudyID, v.SampleID)
		v.UniquePatientKey = uniqueKey(v.StudyID, v.PatientID)
		out[i] = v
	}
	writeJSON(w, out)
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
	rows, _ := ch.Query[panelProfile](s.CH, fmt.Sprintf(
		"SELECT molecular_profile_id AS molecularProfileId, study_id AS studyId, profile_stable_id AS stableId "+
			"FROM molecular_profile WHERE molecular_profile_id IN (%s)", inList(ids)))
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
		rows, _ := ch.Query[sr](s.CH, fmt.Sprintf(
			"SELECT sample_id AS sampleId FROM sample_list WHERE list_id = '%s'", sqlLit(f.SampleListID)))
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
	rows, _ := ch.Query[sr](s.CH, fmt.Sprintf(
		"SELECT sample_id AS sampleId FROM sample WHERE study_id = '%s' ORDER BY sample_id", sqlLit(study)))
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
	types, _ := ch.Query[typeRow](s.CH, fmt.Sprintf(
		"SELECT molecular_profile_id AS id, molecular_alteration_type AS type FROM molecular_profile WHERE molecular_profile_id IN (%s)",
		inList(profIDs)))
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
	rows, _ := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM sample WHERE study_id IN (%s)",
		inList(studies)))
	for _, r := range rows {
		patientOf[jkey(r.StudyID, r.SampleID)] = r.PatientID
	}
	type memberRow struct {
		Profile  string  `json:"profile"`
		SampleID string  `json:"sampleId"`
		Panel    *string `json:"panel"`
	}
	members, _ := ch.Query[memberRow](s.CH, fmt.Sprintf(
		"SELECT molecular_profile_id AS profile, sample_id AS sampleId, gene_panel_id AS panel FROM sample_profile "+
			"WHERE molecular_profile_id IN (%s)", inList(profIDs)))
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
	seqRows, _ := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS sampleId FROM sample_list WHERE list_id IN (%s)",
		inList(func() []string {
			ids := make([]string, len(studies))
			for i, st := range studies {
				ids[i] = st + "_sequenced"
			}
			return ids
		}())))
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

	out := []MolecularData{}
	if len(f.SampleMolecularIdentifiers) > 0 {
		byProfile := map[string][]string{}
		var order []string
		for _, id := range f.SampleMolecularIdentifiers {
			if _, seen := byProfile[id.MolecularProfileID]; !seen {
				order = append(order, id.MolecularProfileID)
			}
			byProfile[id.MolecularProfileID] = append(byProfile[id.MolecularProfileID], id.SampleID)
		}
		for _, p := range order {
			out = append(out, s.molecularDataForProfile(p, byProfile[p], f.EntrezGeneIDs)...)
		}
	} else {
		for _, p := range f.MolecularProfileIDs {
			out = append(out, s.molecularDataForProfile(p, nil, f.EntrezGeneIDs)...)
		}
	}
	if strings.EqualFold(r.URL.Query().Get("projection"), "META") {
		w.Header().Set("total-count", fmt.Sprint(len(out)))
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, out)
}

// molecularDataForProfile mirrors molecularDataFetch's per-profile query, scoped
// to an optional sample subset, returning the rows for one molecular profile.
func (s *State) molecularDataForProfile(mpid string, sampleIDs []string, entrez []int64) []MolecularData {
	out, _ := s.molecularData(mpid, molecularDataFilter{EntrezGeneIDs: entrez, SampleIDs: sampleIDs})
	return out
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
