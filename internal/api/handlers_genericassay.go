package api

import (
	"fmt"
	"net/http"
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

// genericAssayRows reads generic-assay cells for one molecular profile, applying
// optional entity and sample scoping. Returns an empty slice when the profile is
// unknown or holds no matching data.
func (s *State) genericAssayRows(mpid string, entityIDs, sampleIDs []string, sampleListID string) ([]GenericAssayData, error) {
	meta, ok := s.profileMeta(mpid)
	if !ok || !strings.EqualFold(meta.AlterationType, "GENERIC_ASSAY") {
		return []GenericAssayData{}, nil
	}
	where := fmt.Sprintf("m.study_id = '%s' AND m.profile_stable_id = '%s'", sqlLit(meta.StudyID), sqlLit(meta.StableID))
	if len(entityIDs) > 0 {
		where += fmt.Sprintf(" AND m.entity_stable_id IN (%s)", inList(entityIDs))
	}
	if len(sampleIDs) > 0 {
		where += fmt.Sprintf(" AND m.sample_id IN (%s)", inList(sampleIDs))
	} else if sampleListID != "" {
		where += fmt.Sprintf(" AND m.sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(sampleListID))
	}
	rows, err := ch.Query[genericAssayMatrixRow](s.CH, fmt.Sprintf(
		"SELECT m.sample_id AS sampleId, m.entity_stable_id AS stableId, s.patient_id AS patientId, m.value AS value "+
			"FROM cbioportal_raw.generic_assay_matrix m "+
			"LEFT JOIN sample s ON s.study_id = m.study_id AND s.sample_id = m.sample_id "+
			"WHERE %s", where))
	if err != nil {
		return nil, err
	}
	out := make([]GenericAssayData, len(rows))
	for i, r := range rows {
		out[i] = GenericAssayData{
			GenericAssayStableID: r.StableID, MolecularProfileID: mpid, PatientID: r.PatientID,
			SampleID: r.SampleID, StableID: r.StableID, StudyID: meta.StudyID, Value: r.Value,
			UniqueSampleKey:  uniqueKey(meta.StudyID, r.SampleID),
			UniquePatientKey: uniqueKey(meta.StudyID, r.PatientID),
		}
	}
	return out, nil
}

// genericAssayDataInProfileFetch handles POST /api/generic_assay_data/{molecularProfileId}/fetch.
func (s *State) genericAssayDataInProfileFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("molecularProfileId")
	var f genericAssayFilter
	readBody(r, &f)
	out, err := s.genericAssayRows(mpid, f.GenericAssayStableIDs, f.SampleIDs, f.SampleListID)
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

// genericAssayDataMultipleFetch handles POST /api/generic_assay_data/fetch — data
// across several molecular profiles, optionally scoped per (profile, sample).
func (s *State) genericAssayDataMultipleFetch(w http.ResponseWriter, r *http.Request) {
	var f genericAssayMultipleStudyFilter
	readBody(r, &f)

	// Group requested sample ids by molecular profile id.
	samplesByProfile := map[string][]string{}
	for _, smi := range f.SampleMolecularIdentifiers {
		samplesByProfile[smi.MolecularProfileID] = append(samplesByProfile[smi.MolecularProfileID], smi.SampleID)
	}
	profiles := f.MolecularProfileIDs
	if len(profiles) == 0 {
		for mpid := range samplesByProfile {
			profiles = append(profiles, mpid)
		}
	}
	out := []GenericAssayData{}
	for _, mpid := range profiles {
		rows, err := s.genericAssayRows(mpid, f.GenericAssayStableIDs, samplesByProfile[mpid], "")
		if err != nil {
			fail(w, err)
			return
		}
		out = append(out, rows...)
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
	where := "1 = 1"
	if len(conds) > 0 {
		where = strings.Join(conds, " AND ")
	}
	rows, err := ch.Query[genericAssayMetaRow](s.CH, fmt.Sprintf(
		"SELECT entity_stable_id AS stableId, any(name) AS name, any(description) AS description, any(url) AS url "+
			"FROM cbioportal_raw.generic_assay_matrix WHERE %s GROUP BY entity_stable_id", where))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]GenericAssayMeta, len(rows))
	for i, r := range rows {
		out[i] = GenericAssayMeta{
			EntityType: "GENERIC_ASSAY", StableID: r.StableID,
			GenericEntityMetaProperties: map[string]string{
				"NAME": r.Name, "DESCRIPTION": r.Description, "URL": r.URL,
			},
		}
	}
	writeJSON(w, out)
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
		"SELECT DISTINCT geneset_id AS genesetId FROM cbioportal_raw.geneset_score_matrix WHERE %s ORDER BY geneset_id", where))
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
			"FROM cbioportal_raw.geneset_score_matrix m "+
			"LEFT JOIN sample s ON s.study_id = m.study_id AND s.sample_id = m.sample_id "+
			"WHERE %s", where))
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
