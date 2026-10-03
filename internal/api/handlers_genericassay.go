package api

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// ---------- Generic assay data ----------

type GenericAssayData struct {
	GenericAssayStableID string `json:"genericAssayStableId"`
	MolecularProfileID   string `json:"molecularProfileId"`
	PatientID            string `json:"patientId"`
	PatientLevel         bool   `json:"patientLevel"`
	SampleID             string `json:"sampleId"`
	StableID             string `json:"stableId"`
	StudyID              string `json:"studyId"`
	UniquePatientKey     string `json:"uniquePatientKey"`
	UniqueSampleKey      string `json:"uniqueSampleKey"`
	Value                string `json:"value"`
}

type genericAssayFilter struct {
	GenericAssayStableIDs []string `json:"genericAssayStableIds"`
	SampleIDs             []string `json:"sampleIds"`
	SampleListID          string   `json:"sampleListId"`
}

type genericAssayMatrixRow struct {
	SampleID  string `json:"sampleId"`
	StableID  string `json:"stableId"`
	PatientID string `json:"patientId"`
	Value     string `json:"value"`
}

// gaProfileData is one generic-assay profile's cells, keyed by sample, with
// its entities in matrix (import) order.
type gaProfileData struct {
	id           string
	meta         profileMeta
	patientLevel bool
	entities     []string
	cells        map[string]map[string]string // sample id -> entity -> value
	patientOf    map[string]string
}

// gaLoadProfile reads the cells of one generic-assay profile, restricted to
// the given entities and samples (nil: all). A patient-level profile's
// matrix columns are patients; every sample of the patient carries the
// patient's value, as the importer links all of a patient's samples to the
// profile. Values read as the Java mapper truncates them.
func (s *State) gaLoadProfile(meta profileMeta, entityIDs, sampleIDs []string) (*gaProfileData, error) {
	d := &gaProfileData{meta: meta, patientLevel: s.gaProfilePatientLevel(meta),
		cells: map[string]map[string]string{}, patientOf: map[string]string{}}
	where := fmt.Sprintf("study_id = '%s' AND profile_stable_id = '%s'", sqlLit(meta.StudyID), sqlLit(meta.StableID))
	if entityIDs != nil {
		if len(entityIDs) == 0 {
			return d, nil
		}
		where += fmt.Sprintf(" AND entity_stable_id IN (%s)", inList(entityIDs))
	}
	type entityRow struct {
		E string `json:"e"`
	}
	ents, err := ch.Query[entityRow](s.CH, fmt.Sprintf(
		"SELECT entity_stable_id AS e FROM cbioportal_raw.generic_assay_matrix WHERE %s "+
			"GROUP BY entity_stable_id ORDER BY min((_part, _part_offset)), entity_stable_id", where))
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		d.entities = append(d.entities, e.E)
	}
	join := "s.sample_id = m.sample_id"
	if d.patientLevel {
		join = "s.patient_id = m.sample_id"
	}
	sampleCond := ""
	if sampleIDs != nil {
		if len(sampleIDs) == 0 {
			return d, nil
		}
		sampleCond = fmt.Sprintf(" AND s.sample_id IN (%s)", inList(sampleIDs))
	}
	rows, err := ch.Query[genericAssayMatrixRow](s.CH, fmt.Sprintf(
		"SELECT s.sample_id AS sampleId, m.entity_stable_id AS stableId, s.patient_id AS patientId, %s AS value "+
			"FROM (SELECT * FROM %s WHERE %s) m JOIN sample s ON s.study_id = m.study_id AND %s%s",
		javaTruncatedValueSQL("m.value"), genericAssaySource(s), where, join, sampleCond))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		c := d.cells[r.SampleID]
		if c == nil {
			c = map[string]string{}
			d.cells[r.SampleID] = c
		}
		c[r.StableID] = r.Value
		d.patientOf[r.SampleID] = r.PatientID
	}
	return d, nil
}

// genericAssayData ports GenericAssayServiceImpl.fetchGenericAssayData: the
// samples (all of the profiles' samples when samplesByStudy is nil, else the
// listed samples of each profile's study) in sample-id order, and for each, every requested profile of its study that holds the
// sample, one entry per entity in matrix order. Cells without a value (empty
// or NA) are left out, as the controllers filter them.
func (s *State) genericAssayData(profileIDs []string, samplesByStudy map[string][]string, entityIDs []string) ([]GenericAssayData, error) {
	ids := slices.Clone(profileIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	var profiles []*gaProfileData
	for _, mpid := range ids {
		meta, ok := s.profileMeta(mpid)
		if !ok || !strings.EqualFold(meta.AlterationType, "GENERIC_ASSAY") {
			continue
		}
		var sampleIDs []string
		if samplesByStudy != nil {
			sampleIDs = append([]string{}, samplesByStudy[meta.StudyID]...)
		}
		d, err := s.gaLoadProfile(meta, entityIDs, sampleIDs)
		if err != nil {
			return nil, err
		}
		d.id = mpid
		profiles = append(profiles, d)
	}
	type sampleKey struct{ study, sample string }
	seen := map[sampleKey]bool{}
	var samples []sampleKey
	for _, p := range profiles {
		for sm := range p.cells {
			k := sampleKey{p.meta.StudyID, sm}
			if !seen[k] {
				seen[k] = true
				samples = append(samples, k)
			}
		}
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].sample != samples[j].sample {
			return samples[i].sample < samples[j].sample
		}
		return samples[i].study < samples[j].study
	})
	out := []GenericAssayData{}
	for _, sm := range samples {
		for _, p := range profiles {
			if p.meta.StudyID != sm.study {
				continue
			}
			cells, ok := p.cells[sm.sample]
			if !ok {
				continue
			}
			patient := p.patientOf[sm.sample]
			for _, e := range p.entities {
				v, ok := cells[e]
				if !ok || v == "" || v == "NA" {
					continue
				}
				out = append(out, GenericAssayData{
					GenericAssayStableID: e, MolecularProfileID: p.id, PatientID: patient,
					PatientLevel: p.patientLevel, SampleID: sm.sample, StableID: e, StudyID: sm.study, Value: v,
					UniqueSampleKey:  uniqueKey(sm.study, sm.sample),
					UniquePatientKey: uniqueKey(sm.study, patient),
				})
			}
		}
	}
	return out, nil
}

// genericAssayDataInProfileFetch handles POST /api/generic_assay_data/{molecularProfileId}/fetch:
// the samples of the sample list, or the given sample ids.
func (s *State) genericAssayDataInProfileFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("molecularProfileId")
	var f genericAssayFilter
	readBody(r, &f)
	sampleIDs := f.SampleIDs
	if f.SampleListID != "" {
		type idRow struct {
			SampleID string `json:"sampleId"`
		}
		rows, err := ch.Query[idRow](s.CH, fmt.Sprintf(
			"SELECT sample_id AS sampleId FROM sample_list WHERE list_id = '%s'", sqlLit(f.SampleListID)))
		if err != nil {
			fail(w, err)
			return
		}
		sampleIDs = make([]string, len(rows))
		for i, row := range rows {
			sampleIDs[i] = row.SampleID
		}
		if len(sampleIDs) == 0 {
			writeJSON(w, []GenericAssayData{})
			return
		}
	}
	var byStudy map[string][]string
	if sampleIDs != nil {
		meta, _ := s.profileMeta(mpid)
		byStudy = map[string][]string{meta.StudyID: sampleIDs}
	}
	out, err := s.genericAssayData([]string{mpid}, byStudy, f.GenericAssayStableIDs)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

type genericAssayMultipleStudyFilter struct {
	GenericAssayStableIDs      []string `json:"genericAssayStableIds"`
	MolecularProfileIDs        []string `json:"molecularProfileIds"`
	SampleMolecularIdentifiers []struct {
		MolecularProfileID string `json:"molecularProfileId"`
		SampleID           string `json:"sampleId"`
	} `json:"sampleMolecularIdentifiers"`
}

// genericAssayDataMultipleFetch handles POST /api/generic_assay_data/fetch:
// every sample of the profiles, or the samples named by the
// (profile, sample) identifiers — each then read from every requested
// profile of its study, as Java pairs samples with profiles by study.
// Exactly one of the two forms must be given (else 400 with no body).
func (s *State) genericAssayDataMultipleFetch(w http.ResponseWriter, r *http.Request) {
	var f genericAssayMultipleStudyFilter
	readBody(r, &f)
	if (len(f.MolecularProfileIDs) > 0) == (len(f.SampleMolecularIdentifiers) > 0) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var out []GenericAssayData
	var err error
	if len(f.MolecularProfileIDs) > 0 {
		out, err = s.genericAssayData(f.MolecularProfileIDs, nil, f.GenericAssayStableIDs)
	} else {
		var profiles []string
		byStudy := map[string][]string{}
		for _, smi := range f.SampleMolecularIdentifiers {
			profiles = append(profiles, smi.MolecularProfileID)
			if meta, ok := s.profileMeta(smi.MolecularProfileID); ok {
				byStudy[meta.StudyID] = append(byStudy[meta.StudyID], smi.SampleID)
			}
		}
		out, err = s.genericAssayData(profiles, byStudy, f.GenericAssayStableIDs)
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// ---------- Generic assay meta ----------

type GenericAssayMeta struct {
	EntityType                  string            `json:"entityType"`
	GenericEntityMetaProperties map[string]string `json:"genericEntityMetaProperties"`
	StableID                    string            `json:"stableId"`
}

type genericAssayMetaFilter struct {
	GenericAssayStableIDs []string `json:"genericAssayStableIds"`
	MolecularProfileIDs   []string `json:"molecularProfileIds"`
}

type genericAssayMetaRow struct {
	StableID    string `json:"stableId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// genericAssayMetaFetch handles POST /api/generic-assay-meta/fetch — distinct
// entities (with NAME/DESCRIPTION/URL properties) across the requested profiles
// and/or entity ids.
func (s *State) genericAssayMetaFetch(w http.ResponseWriter, r *http.Request) {
	var f genericAssayMetaFilter
	readBody(r, &f)

	var conds []string
	if len(f.MolecularProfileIDs) > 0 {
		var pairs [][2]string
		for _, mpid := range f.MolecularProfileIDs {
			if m, ok := s.profileMeta(mpid); ok && strings.EqualFold(m.AlterationType, "GENERIC_ASSAY") {
				pairs = append(pairs, [2]string{m.StudyID, m.StableID})
			}
		}
		if len(pairs) == 0 {
			writeJSON(w, []GenericAssayMeta{})
			return
		}
		conds = append(conds, fmt.Sprintf("(study_id, profile_stable_id) IN (%s)", tupleIn(pairs)))
	}
	if len(f.GenericAssayStableIDs) > 0 {
		conds = append(conds, fmt.Sprintf("entity_stable_id IN (%s)", inList(f.GenericAssayStableIDs)))
	}
	if len(conds) == 0 {
		writeJSON(w, []GenericAssayMeta{})
		return
	}
	where := "1 = 1"
	if len(conds) > 0 {
		where = strings.Join(conds, " AND ")
	}
	props, err := s.genericAssayMetaProperties(where)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, genericAssayMetaList(props))
}

// genericAssayMetaList orders entity metadata as GenericAssayMapper does:
// by the number an id starts with (0 without one), then by id.
func genericAssayMetaList(props map[string]map[string]string) []GenericAssayMeta {
	ids := sortedKeys(props)
	sort.SliceStable(ids, func(i, j int) bool { return entityLess(ids[i], ids[j]) })
	out := make([]GenericAssayMeta, 0, len(ids))
	for _, id := range ids {
		out = append(out, GenericAssayMeta{EntityType: "GENERIC_ASSAY", StableID: id, GenericEntityMetaProperties: props[id]})
	}
	return out
}

// genericAssayMetaProperties returns the meta properties of the generic
// assay entities matching where (over study_id, profile_stable_id and
// entity_stable_id), keyed by entity stable id: every stored property where
// the raw database has the entity tables of a production clone, else the
// NAME / DESCRIPTION / URL columns of the staged matrix.
func (s *State) genericAssayMetaProperties(where string) (map[string]map[string]string, error) {
	if len(s.rawColumns("generic_assay_entity_meta")) > 0 {
		type propRow struct {
			StableID string            `json:"stableId"`
			Props    map[string]string `json:"props"`
		}
		rows, err := ch.Query[propRow](s.CH, fmt.Sprintf(
			"SELECT entity_stable_id AS stableId, properties AS props FROM cbioportal_raw.generic_assay_entity_meta "+
				"WHERE entity_stable_id IN (SELECT entity_stable_id FROM cbioportal_raw.generic_assay_profile_entity WHERE %s)", where))
		if err != nil {
			return nil, err
		}
		out := make(map[string]map[string]string, len(rows))
		for _, r := range rows {
			if r.Props == nil {
				r.Props = map[string]string{}
			}
			out[r.StableID] = r.Props
		}
		return out, nil
	}
	rows, err := ch.Query[genericAssayMetaRow](s.CH, fmt.Sprintf(
		"SELECT entity_stable_id AS stableId, any(name) AS name, any(description) AS description, any(url) AS url "+
			"FROM %s WHERE %s GROUP BY entity_stable_id", genericAssaySource(s), where))
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]string, len(rows))
	for _, r := range rows {
		out[r.StableID] = map[string]string{"NAME": r.Name, "DESCRIPTION": r.Description, "URL": r.URL}
	}
	return out, nil
}

// ---------- Genesets ----------

type Geneset struct {
	Description          string  `json:"description"`
	GenesetID            string  `json:"genesetId"`
	Name                 string  `json:"name"`
	RefLink              string  `json:"refLink"`
	RepresentativePvalue float64 `json:"representativePvalue"`
	RepresentativeScore  float64 `json:"representativeScore"`
}

// genesetsForIDs returns distinct genesets from the score matrix, optionally
// restricted to the given ids. Name/description are best-effort (the geneset id).
func (s *State) genesetsForIDs(ids []string) ([]Geneset, error) {
	where := "1 = 1"
	if len(ids) > 0 {
		where = fmt.Sprintf("geneset_id IN (%s)", inList(ids))
	}
	type row struct {
		GenesetID string `json:"genesetId"`
	}
	rows, err := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT DISTINCT geneset_id AS genesetId FROM %s WHERE %s ORDER BY geneset_id", genesetScoreSource(s), where))
	if err != nil {
		return nil, err
	}
	out := make([]Geneset, len(rows))
	for i, r := range rows {
		out[i] = Geneset{GenesetID: r.GenesetID, Name: r.GenesetID, Description: r.GenesetID}
	}
	return out, nil
}

// genesetsFetch handles POST /api/genesets/fetch — body is a JSON array of geneset ids.
func (s *State) genesetsFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	out, err := s.genesetsForIDs(ids)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// genesetGet handles GET /api/genesets/{genesetId}.
func (s *State) genesetGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("genesetId")
	rows, err := s.genesetsForIDs([]string{id})
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		// Return the requested id best-effort so the frontend still renders.
		writeJSON(w, Geneset{GenesetID: id, Name: id, Description: id})
		return
	}
	writeJSON(w, rows[0])
}

// ---------- Geneset molecular data (GSVA) ----------

type GenesetMolecularData struct {
	Geneset          Geneset `json:"geneset"`
	GenesetID        string  `json:"genesetId"`
	GeneticProfileID string  `json:"geneticProfileId"`
	PatientID        string  `json:"patientId"`
	SampleID         string  `json:"sampleId"`
	StableID         string  `json:"stableId"`
	StudyID          string  `json:"studyId"`
	UniquePatientKey string  `json:"uniquePatientKey"`
	UniqueSampleKey  string  `json:"uniqueSampleKey"`
	Value            string  `json:"value"`
}

type genesetDataFilterCriteria struct {
	GenesetIDs   []string `json:"genesetIds"`
	SampleIDs    []string `json:"sampleIds"`
	SampleListID string   `json:"sampleListId"`
}

type genesetMatrixRow struct {
	GenesetID string `json:"genesetId"`
	SampleID  string `json:"sampleId"`
	PatientID string `json:"patientId"`
	Value     string `json:"value"`
}

// genesetGeneticDataFetch handles
// POST /api/genetic-profiles/{geneticProfileId}/geneset-genetic-data/fetch.
func (s *State) genesetGeneticDataFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("geneticProfileId")
	var f genesetDataFilterCriteria
	readBody(r, &f)
	meta, ok := s.profileMeta(mpid)
	if !ok || !strings.EqualFold(meta.AlterationType, "GENESET_SCORE") {
		writeJSON(w, []GenesetMolecularData{})
		return
	}
	where := fmt.Sprintf("m.study_id = '%s' AND m.profile_stable_id = '%s'", sqlLit(meta.StudyID), sqlLit(meta.StableID))
	if len(f.GenesetIDs) > 0 {
		where += fmt.Sprintf(" AND m.geneset_id IN (%s)", inList(f.GenesetIDs))
	}
	if len(f.SampleIDs) > 0 {
		where += fmt.Sprintf(" AND m.sample_id IN (%s)", inList(f.SampleIDs))
	} else if f.SampleListID != "" {
		where += fmt.Sprintf(" AND m.sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(f.SampleListID))
	}
	rows, err := ch.Query[genesetMatrixRow](s.CH, fmt.Sprintf(
		"SELECT m.geneset_id AS genesetId, m.sample_id AS sampleId, s.patient_id AS patientId, m.value AS value "+
			"FROM %s m "+
			"LEFT JOIN sample s ON s.study_id = m.study_id AND s.sample_id = m.sample_id "+
			"WHERE %s", genesetScoreSource(s), where))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]GenesetMolecularData, len(rows))
	for i, r := range rows {
		out[i] = GenesetMolecularData{
			Geneset:   Geneset{GenesetID: r.GenesetID, Name: r.GenesetID, Description: r.GenesetID},
			GenesetID: r.GenesetID, GeneticProfileID: mpid, PatientID: r.PatientID,
			SampleID: r.SampleID, StableID: r.GenesetID, StudyID: meta.StudyID, Value: r.Value,
			UniqueSampleKey:  uniqueKey(meta.StudyID, r.SampleID),
			UniquePatientKey: uniqueKey(meta.StudyID, r.PatientID),
		}
	}
	writeJSON(w, out)
}

// routeGenericAssay registers the generic-assay and geneset (GSVA) endpoints.
func (s *State) routeGenericAssay(mux *http.ServeMux) {
	// Generic assay data.
	mux.HandleFunc("POST /api/generic_assay_data/{molecularProfileId}/fetch", s.genericAssayDataInProfileFetch)
	mux.HandleFunc("POST /api/generic_assay_data/fetch", s.genericAssayDataMultipleFetch)
	mux.HandleFunc("POST /api/generic-assay-data/fetch", s.genericAssayDataMultipleFetch)

	// Generic assay meta.
	mux.HandleFunc("POST /api/generic-assay-meta/fetch", s.genericAssayMetaFetch)
	mux.HandleFunc("POST /api/generic_assay_meta/generic-assay/fetch", s.genericAssayMetaFetch)

	// Genesets.
	mux.HandleFunc("POST /api/genesets/fetch", s.genesetsFetch)
	mux.HandleFunc("GET /api/genesets/{genesetId}", s.genesetGet)

	// Geneset molecular data (GSVA).
	mux.HandleFunc("POST /api/genetic-profiles/{geneticProfileId}/geneset-genetic-data/fetch", s.genesetGeneticDataFetch)
}

// genericAssaySource is the staged generic-assay matrix with every column the
// handlers read (entity properties a file lacks read as empty).
func genericAssaySource(s *State) string {
	return s.rawSource("generic_assay_matrix",
		"study_id", "profile_stable_id", "entity_stable_id", "sample_id", "value", "name", "description", "url")
}

// genesetScoreSource is the staged GSVA score matrix.
func genesetScoreSource(s *State) string {
	return s.rawSource("geneset_score_matrix", "study_id", "profile_stable_id", "geneset_id", "sample_id", "value")
}

// geneMatrixCols are the columns read from a staged per-gene matrix.
var geneMatrixCols = []string{"study_id", "profile_stable_id", "sample_id", "hugo_symbol", "entrez_gene_id", "value"}
