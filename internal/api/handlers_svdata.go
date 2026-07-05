package api

import (
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
	Annotation               string   `json:"annotation"`
	BreakpointType           string   `json:"breakpointType"`
	Comments                 string   `json:"comments"`
	ConnectionType           string   `json:"connectionType"`
	DnaSupport               string   `json:"dnaSupport"`
	DriverFilter             string   `json:"driverFilter"`
	DriverFilterAnn          string   `json:"driverFilterAnn"`
	DriverTiersFilter        string   `json:"driverTiersFilter"`
	DriverTiersFilterAnn     string   `json:"driverTiersFilterAnn"`
	EventInfo                string   `json:"eventInfo"`
	Length                   *int64   `json:"length"`
	MolecularProfileID       string   `json:"molecularProfileId"`
	NamespaceColumns         struct{} `json:"namespaceColumns"`
	NcbiBuild                string   `json:"ncbiBuild"`
	NormalPairedEndReadCount *int64   `json:"normalPairedEndReadCount"`
	NormalReadCount          *int64   `json:"normalReadCount"`
	NormalSplitReadCount     *int64   `json:"normalSplitReadCount"`
	NormalVariantCount       *int64   `json:"normalVariantCount"`
	PatientID                string   `json:"patientId"`
	RnaSupport               string   `json:"rnaSupport"`
	SampleID                 string   `json:"sampleId"`
	Site1Chromosome          string   `json:"site1Chromosome"`
	Site1Contig              string   `json:"site1Contig"`
	Site1Description         string   `json:"site1Description"`
	Site1EnsemblTranscriptID string   `json:"site1EnsemblTranscriptId"`
	Site1EntrezGeneID        *int64   `json:"site1EntrezGeneId"`
	Site1HugoSymbol          string   `json:"site1HugoSymbol"`
	Site1Position            *int64   `json:"site1Position"`
	Site1Region              string   `json:"site1Region"`
	Site1RegionNumber        *int64   `json:"site1RegionNumber"`
	Site2Chromosome          string   `json:"site2Chromosome"`
	Site2Contig              string   `json:"site2Contig"`
	Site2Description         string   `json:"site2Description"`
	Site2EffectOnFrame       string   `json:"site2EffectOnFrame"`
	Site2EnsemblTranscriptID string   `json:"site2EnsemblTranscriptId"`
	Site2EntrezGeneID        *int64   `json:"site2EntrezGeneId"`
	Site2HugoSymbol          string   `json:"site2HugoSymbol"`
	Site2Position            *int64   `json:"site2Position"`
	Site2Region              string   `json:"site2Region"`
	Site2RegionNumber        *int64   `json:"site2RegionNumber"`
	StudyID                  string   `json:"studyId"`
	SvStatus                 string   `json:"svStatus"`
	TumorPairedEndReadCount  *int64   `json:"tumorPairedEndReadCount"`
	TumorReadCount           *int64   `json:"tumorReadCount"`
	TumorSplitReadCount      *int64   `json:"tumorSplitReadCount"`
	TumorVariantCount        *int64   `json:"tumorVariantCount"`
	UniquePatientKey         string   `json:"uniquePatientKey"`
	UniqueSampleKey          string   `json:"uniqueSampleKey"`
	VariantClass             string   `json:"variantClass"`
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

// svStudy strips the structural-variant profile suffix to recover the study id.
func svStudy(mpid string) string { return strings.TrimSuffix(mpid, "_structural_variants") }

type svFilter struct {
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

	if len(f.EntrezGeneIDs) > 0 {
		ids := make([]string, len(f.EntrezGeneIDs))
		for i, e := range f.EntrezGeneIDs {
			ids[i] = fmt.Sprintf("%d", e)
		}
		joined := strings.Join(ids, ", ")
		scope += fmt.Sprintf(" AND (sv.site1_entrez_gene_id IN (%s) OR sv.site2_entrez_gene_id IN (%s))", joined, joined)
	}

	rows, err := ch.Query[svDataRow](s.CH, fmt.Sprintf(
		"SELECT sv.study_id AS studyId, sv.sample_id AS sampleId, sm.patient_id AS patientId, "+
			"sv.site1_hugo_symbol AS site1HugoSymbol, sv.site1_entrez_gene_id AS site1EntrezGeneId, "+
			"sv.site2_hugo_symbol AS site2HugoSymbol, sv.site2_entrez_gene_id AS site2EntrezGeneId "+
			"FROM structural_variant sv "+
			"LEFT JOIN sample sm ON sv.study_id = sm.study_id AND sv.sample_id = sm.sample_id "+
			"WHERE %s ORDER BY sv.study_id, sv.sample_id", scope))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]StructuralVariant, len(rows))
	for i, r := range rows {
		out[i] = StructuralVariant{
			MolecularProfileID: r.StudyID + "_structural_variants",
			SampleID:           r.SampleID,
			PatientID:          r.PatientID,
			StudyID:            r.StudyID,
			Site1HugoSymbol:    r.Site1HugoSymbol,
			Site1EntrezGeneID:  r.Site1EntrezGeneID,
			Site2HugoSymbol:    r.Site2HugoSymbol,
			Site2EntrezGeneID:  r.Site2EntrezGeneID,
			UniqueSampleKey:    uniqueKey(r.StudyID, r.SampleID),
			UniquePatientKey:   uniqueKey(r.StudyID, r.PatientID),
		}
	}
	writeJSON(w, out)
}

// ---------- Gene panel data (sample -> panel mapping) ----------

type GenePanelData struct {
	GenePanelID        string `json:"genePanelId"`
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

// gpmValueColumns returns the set of gene_panel_matrix columns that hold a
// per-profile panel id (i.e. the wide value columns, not the metadata columns).
func (s *State) gpmValueColumns() map[string]bool {
	type nameRow struct {
		Name string `json:"name"`
	}
	rows, _ := ch.Query[nameRow](s.CH,
		"SELECT name FROM system.columns WHERE database = 'cbioportal_raw' AND table = 'gene_panel_matrix' "+
			"AND name NOT IN ('study_id', 'source_file', 'alteration_type', 'datatype', 'profile_stable_id', 'sample_id')")
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Name] = true
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

func (s *State) buildGenePanelData(reqs []gpReq) []GenePanelData {
	out := []GenePanelData{}
	if len(reqs) == 0 {
		return out
	}

	// Resolve profiles (study + stable id / matrix column name).
	profIDset := map[string]bool{}
	for _, q := range reqs {
		profIDset[q.profileID] = true
	}
	profIDs := make([]string, 0, len(profIDset))
	for id := range profIDset {
		profIDs = append(profIDs, id)
	}
	profs := s.resolvePanelProfiles(profIDs)

	valueCols := s.gpmValueColumns()

	// Study for each request, plus the distinct studies and needed columns.
	studyOf := func(profileID string) string {
		if p, ok := profs[profileID]; ok && p.StudyID != "" {
			return p.StudyID
		}
		return deriveStudyFromProfile(profileID, valueCols)
	}
	stableOf := func(profileID string) string {
		if p, ok := profs[profileID]; ok && p.StableID != "" {
			return p.StableID
		}
		return ""
	}

	studySet := map[string]bool{}
	colSet := map[string]bool{}
	for _, q := range reqs {
		studySet[studyOf(q.profileID)] = true
		if c := stableOf(q.profileID); c != "" && valueCols[c] {
			colSet[c] = true
		}
	}
	studies := make([]string, 0, len(studySet))
	for st := range studySet {
		if st != "" {
			studies = append(studies, st)
		}
	}

	// patient_id per (study, sample).
	patientOf := map[string]string{}
	if len(studies) > 0 {
		rows, _ := ch.Query[idRow](s.CH, fmt.Sprintf(
			"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM sample WHERE study_id IN (%s)",
			inList(studies)))
		for _, r := range rows {
			patientOf[jkey(r.StudyID, r.SampleID)] = r.PatientID
		}
	}

	// Which of the requested studies actually have a gene panel matrix.
	matrixStudies := map[string]bool{}
	if len(studies) > 0 {
		type sr struct {
			StudyID string `json:"studyId"`
		}
		rows, _ := ch.Query[sr](s.CH, fmt.Sprintf(
			"SELECT DISTINCT study_id AS studyId FROM cbioportal_raw.gene_panel_matrix WHERE study_id IN (%s)",
			inList(studies)))
		for _, r := range rows {
			matrixStudies[r.StudyID] = true
		}
	}

	// Panel value per (study, sample, column).
	panelOf := map[string]string{}
	if len(studies) > 0 {
		for col := range colSet {
			type pr struct {
				StudyID  string `json:"studyId"`
				SampleID string `json:"sampleId"`
				Panel    string `json:"panel"`
			}
			rows, _ := ch.Query[pr](s.CH, fmt.Sprintf(
				"SELECT study_id AS studyId, sample_id AS sampleId, %s AS panel FROM cbioportal_raw.gene_panel_matrix WHERE study_id IN (%s)",
				col, inList(studies)))
			for _, r := range rows {
				panelOf[jkey(r.StudyID, r.SampleID, col)] = r.Panel
			}
		}
	}

	for _, q := range reqs {
		study := studyOf(q.profileID)
		stable := stableOf(q.profileID)
		patient := patientOf[jkey(study, q.sampleID)]

		panelID := ""
		profiled := true
		if matrixStudies[study] && valueCols[stable] {
			// A sample only counts as "not profiled" when it has an explicit
			// empty/NA gene-panel-matrix cell for this alteration type. Samples
			// absent from the matrix are whole-genome profiled (profiled=true, no
			// panel) — otherwise the oncoprint hides alterations for every sample
			// the study didn't put in its (often partial) gene matrix.
			// Empty / NA / WES / WGS / WXS all mean whole-exome/genome sequenced =
			// profiled for every gene (profiled=true, no specific panel), matching
			// cBioPortal. A concrete panel id sets genePanelId. Samples absent from
			// the matrix are likewise whole-genome profiled (handled by the outer
			// default profiled=true).
			if v, inMatrix := panelOf[jkey(study, q.sampleID, stable)]; inMatrix {
				switch {
				case v == "" || strings.EqualFold(v, "NA") ||
					strings.EqualFold(v, "WES") || strings.EqualFold(v, "WXS") || strings.EqualFold(v, "WGS"):
					profiled = true
				default:
					panelID = v
					profiled = true
				}
			}
		}

		out = append(out, GenePanelData{
			GenePanelID:        panelID,
			MolecularProfileID: q.profileID,
			PatientID:          patient,
			Profiled:           profiled,
			SampleID:           q.sampleID,
			StudyID:            study,
			UniquePatientKey:   uniqueKey(study, patient),
			UniqueSampleKey:    uniqueKey(study, q.sampleID),
		})
	}
	return out
}

// deriveStudyFromProfile strips a known matrix-column suffix (e.g. "_mutations")
// to recover the study id when the profile can't be resolved from the DB.
func deriveStudyFromProfile(profileID string, valueCols map[string]bool) string {
	for col := range valueCols {
		if strings.HasSuffix(profileID, "_"+col) {
			return strings.TrimSuffix(profileID, "_"+col)
		}
	}
	return profileID
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
	writeJSON(w, out)
}

// molecularDataForProfile mirrors molecularDataFetch's per-profile query, scoped
// to an optional sample subset, returning the rows for one molecular profile.
func (s *State) molecularDataForProfile(mpid string, sampleIDs []string, entrez []int64) []MolecularData {
	meta, ok := s.profileMeta(mpid)
	if !ok {
		return nil
	}
	table := meta.dataTable()
	if table == "" {
		return nil
	}
	where := fmt.Sprintf("study_id = '%s' AND profile_stable_id = '%s'", sqlLit(meta.StudyID), sqlLit(meta.StableID))
	where += geneFilter(entrez)
	if len(sampleIDs) > 0 {
		where += fmt.Sprintf(" AND sample_id IN (%s)", inList(sampleIDs))
	}
	rows, err := ch.Query[molecularDataRow](s.CH, fmt.Sprintf(
		"SELECT sample_id AS sampleId, toInt64OrNull(entrez_gene_id) AS entrezGeneId, hugo_symbol AS hugoGeneSymbol, value FROM cbioportal_raw.%s WHERE %s",
		table, where))
	if err != nil {
		return nil
	}
	out := make([]MolecularData, len(rows))
	for i, r := range rows {
		out[i] = MolecularData{
			MolecularProfileID: mpid, SampleID: r.SampleID, EntrezGeneID: r.EntrezGeneID,
			Gene:    Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol},
			StudyID: meta.StudyID, Value: r.Value, UniqueSampleKey: uniqueKey(meta.StudyID, r.SampleID),
		}
	}
	return out
}
