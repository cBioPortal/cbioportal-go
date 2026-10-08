package core

import (
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// AlterationTypeTable maps a molecular alteration type to its unpivoted matrix
// staging table (long form: study_id, profile_stable_id, hugo_symbol,
// entrez_gene_id, sample_id, value).
func AlterationTypeTable(at string) string {
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

// ProfileMeta is a molecular profile's study, alteration type, datatype and
// stable id.
type ProfileMeta struct {
	StudyID        string `json:"studyId"`
	AlterationType string `json:"alterationType"`
	Datatype       string `json:"datatype"`
	StableID       string `json:"stableId"`
}

// ProfileMeta looks up the molecular profile mpid (cached); false when it
// does not exist.
func (s *State) ProfileMeta(mpid string) (ProfileMeta, bool) {
	rows, _ := CachedQueryP[ProfileMeta](s,
		"SELECT study_id AS studyId, molecular_alteration_type AS alterationType, datatype, profile_stable_id AS stableId "+
			"FROM molecular_profile WHERE molecular_profile_id = {profile:String} LIMIT 1", ch.Params{"profile": mpid})
	if len(rows) == 0 {
		return ProfileMeta{}, false
	}
	return rows[0], true
}

// DataTable resolves the staging table holding a profile's per-cell data,
// accounting for the DISCRETE_LONG copy-number layout.
func (m ProfileMeta) DataTable() string {
	if strings.EqualFold(m.AlterationType, "COPY_NUMBER_ALTERATION") &&
		strings.EqualFold(m.Datatype, "DISCRETE_LONG") {
		return "cna_discrete_long"
	}
	return AlterationTypeTable(m.AlterationType)
}
