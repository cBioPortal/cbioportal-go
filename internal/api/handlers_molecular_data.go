package api

import (
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// alterationTypeTable maps a molecular alteration type to its unpivoted matrix
// staging table (long form: study_id, profile_stable_id, hugo_symbol,
// entrez_gene_id, sample_id, value).
func alterationTypeTable(at string) string {
	switch strings.ToUpper(at) {
	case "COPY_NUMBER_ALTERATION":
		return "cna_matrix"
	case "MRNA_EXPRESSION":
		return "mrna_expression_matrix"
	case "METHYLATION":
		return "methylation_matrix"
	case "PROTEIN_LEVEL":
		return "protein_level_matrix"
	case "GENESET_SCORE":
		return "geneset_score_matrix"
	case "GENERIC_ASSAY":
		return "generic_assay_matrix"
	}
	return ""
}

type profileMeta struct {
	StudyID        string `json:"studyId"`
	AlterationType string `json:"alterationType"`
	Datatype       string `json:"datatype"`
	StableID       string `json:"stableId"`
}

func (s *State) profileMeta(mpid string) (profileMeta, bool) {
	rows, _ := ch.Query[profileMeta](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, molecular_alteration_type AS alterationType, datatype, profile_stable_id AS stableId "+
			"FROM molecular_profile WHERE molecular_profile_id = '%s' LIMIT 1", sqlLit(mpid)))
	if len(rows) == 0 {
		return profileMeta{}, false
	}
	return rows[0], true
}

// dataTable resolves the staging table holding a profile's per-cell data,
// accounting for the DISCRETE_LONG copy-number layout.
func (m profileMeta) dataTable() string {
	if strings.EqualFold(m.AlterationType, "COPY_NUMBER_ALTERATION") &&
		strings.EqualFold(m.Datatype, "DISCRETE_LONG") {
		return "cna_discrete_long"
	}
	return alterationTypeTable(m.AlterationType)
}

type molecularDataRow struct {
	SampleID       string `json:"sampleId"`
	PatientID      string `json:"patientId"`
	EntrezGeneID   *int64 `json:"entrezGeneId"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
	Value          string `json:"value"`
}

type MolecularData struct {
	MolecularProfileID string `json:"molecularProfileId"`
	SampleID           string `json:"sampleId"`
	PatientID          string `json:"patientId"`
	EntrezGeneID       *int64 `json:"entrezGeneId"`
	Gene               Gene   `json:"gene"`
	StudyID            string `json:"studyId"`
	Value              string `json:"value"`
	UniqueSampleKey    string `json:"uniqueSampleKey"`
	UniquePatientKey   string `json:"uniquePatientKey"`
}

type molecularDataFilter struct {
	EntrezGeneIDs []int64  `json:"entrezGeneIds"`
	SampleIDs     []string `json:"sampleIds"`
	SampleListID  string   `json:"sampleListId"`
}

func (s *State) molecularDataFetch(w http.ResponseWriter, r *http.Request) {
	var f molecularDataFilter
	readBody(r, &f)
	out, err := s.molecularData(r.PathValue("id"), f)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// molecularDataGet serves GET /api/molecular-profiles/{id}/molecular-data
// (sampleListId and entrezGeneId query parameters).
func (s *State) molecularDataGet(w http.ResponseWriter, r *http.Request) {
	f := molecularDataFilter{SampleListID: r.URL.Query().Get("sampleListId")}
	for _, e := range r.URL.Query()["entrezGeneId"] {
		var id int64
		if _, err := fmt.Sscan(e, &id); err == nil {
			f.EntrezGeneIDs = append(f.EntrezGeneIDs, id)
		}
	}
	out, err := s.molecularData(r.PathValue("id"), f)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// molecularData returns a profile's per-sample gene values, optionally limited
// to genes and samples (explicit ids or a sample list).
func (s *State) molecularData(mpid string, f molecularDataFilter) ([]MolecularData, error) {
	meta, ok := s.profileMeta(mpid)
	if !ok {
		return []MolecularData{}, nil
	}
	table := meta.dataTable()
	if table == "" {
		return []MolecularData{}, nil
	}
	where := fmt.Sprintf("study_id = '%s' AND profile_stable_id = '%s'", sqlLit(meta.StudyID), sqlLit(meta.StableID))
	if len(f.EntrezGeneIDs) > 0 {
		ids := make([]string, len(f.EntrezGeneIDs))
		for i, e := range f.EntrezGeneIDs {
			ids[i] = fmt.Sprintf("%d", e)
		}
		joined := strings.Join(ids, ", ")
		where += fmt.Sprintf(" AND (toInt64OrNull(entrez_gene_id) IN (%[1]s) OR (ifNull(toInt64OrNull(entrez_gene_id), 0) <= 0 "+
			"AND upper(hugo_symbol) IN (SELECT upper(hugo_gene_symbol) FROM cbioportal_ref.gene WHERE entrez_gene_id IN (%[1]s))))", joined)
	}
	if len(f.SampleIDs) > 0 {
		where += fmt.Sprintf(" AND sample_id IN (%s)", inList(f.SampleIDs))
	} else if f.SampleListID != "" {
		where += fmt.Sprintf(" AND sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(f.SampleListID))
	}
	rows, err := ch.Query[molecularDataRow](s.CH, fmt.Sprintf(
		"SELECT m.sample_id AS sampleId, s.patient_id AS patientId, m.entrez AS entrezGeneId, "+
			"m.hugo_symbol AS hugoGeneSymbol, m.value AS value FROM (%s) m "+
			"LEFT JOIN sample s ON s.study_id = m.study_id AND s.sample_id = m.sample_id",
		resolvedEntrezSQL("cbioportal_raw."+table, where)))
	if err != nil {
		return nil, err
	}
	out := make([]MolecularData, len(rows))
	for i, r := range rows {
		out[i] = MolecularData{
			MolecularProfileID: mpid, SampleID: r.SampleID, PatientID: r.PatientID, EntrezGeneID: r.EntrezGeneID,
			Gene:    Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol},
			StudyID: meta.StudyID, Value: r.Value,
			UniqueSampleKey: uniqueKey(meta.StudyID, r.SampleID), UniquePatientKey: uniqueKey(meta.StudyID, r.PatientID),
		}
	}
	return out, nil
}

// resolvedEntrezSQL selects a staged per-gene table with an `entrez` column:
// the file's entrez id, or the reference gene's for a blank one (as the
// importer resolves Hugo symbols). where filters the staged rows.
func resolvedEntrezSQL(table, where string) string {
	return fmt.Sprintf("SELECT t.*, if(toInt64OrNull(t.entrez_gene_id) > 0, toInt64OrNull(t.entrez_gene_id), "+
		"nullIf(toInt64(g.entrez_gene_id), 0)) AS entrez FROM (SELECT * FROM %s WHERE %s) t "+
		"LEFT JOIN cbioportal_ref.gene g ON t.hugo_symbol != '' AND upper(t.hugo_symbol) = upper(g.hugo_gene_symbol)",
		table, where)
}
