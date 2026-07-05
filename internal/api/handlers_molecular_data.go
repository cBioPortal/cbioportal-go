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
	mpid := r.PathValue("id")
	var f molecularDataFilter
	readBody(r, &f)
	meta, ok := s.profileMeta(mpid)
	if !ok {
		writeJSON(w, []MolecularData{})
		return
	}
	table := meta.dataTable()
	if table == "" {
		writeJSON(w, []MolecularData{})
		return
	}
	where := fmt.Sprintf("study_id = '%s' AND profile_stable_id = '%s'", sqlLit(meta.StudyID), sqlLit(meta.StableID))
	if len(f.EntrezGeneIDs) > 0 {
		ids := make([]string, len(f.EntrezGeneIDs))
		for i, e := range f.EntrezGeneIDs {
			ids[i] = fmt.Sprintf("%d", e)
		}
		where += fmt.Sprintf(" AND toInt64OrNull(entrez_gene_id) IN (%s)", strings.Join(ids, ", "))
	}
	if len(f.SampleIDs) > 0 {
		where += fmt.Sprintf(" AND sample_id IN (%s)", inList(f.SampleIDs))
	} else if f.SampleListID != "" {
		where += fmt.Sprintf(" AND sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(f.SampleListID))
	}
	rows, err := ch.Query[molecularDataRow](s.CH, fmt.Sprintf(
		"SELECT sample_id AS sampleId, toInt64OrNull(entrez_gene_id) AS entrezGeneId, hugo_symbol AS hugoGeneSymbol, value FROM cbioportal_raw.%s WHERE %s",
		table, where))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]MolecularData, len(rows))
	for i, r := range rows {
		out[i] = MolecularData{
			MolecularProfileID: mpid, SampleID: r.SampleID, EntrezGeneID: r.EntrezGeneID,
			Gene:    Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol},
			StudyID: meta.StudyID, Value: r.Value, UniqueSampleKey: uniqueKey(meta.StudyID, r.SampleID),
		}
	}
	writeJSON(w, out)
}
