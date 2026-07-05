package transform

import "fmt"

// geneticAlterationDerivedSQL builds prod's `genetic_alteration_derived`: the
// generic per-(sample, gene, profile) value table covering every non-mutation,
// non-SV, non-generic-assay molecular profile. Prod derives it from the
// genetic_alteration blob keyed by gene; we source directly from the per-gene
// long/matrix staging tables (all schema-on-read into the same shape:
// study_id, hugo_symbol, sample_id, value, profile_stable_id).
//
// profile_type matches prod's INSERT semantics — the profile stable id with the
// study prefix stripped (e.g. `gistic`, `log2CNA`, `mrna`), which in our staging
// is already the bare `profile_stable_id` column. alteration_value drops NA
// and empty just as prod does (prod maps empty to NULL then filters out NA).
func geneticAlterationDerivedSQL(rawDB, refDB string) string {
	_ = refDB
	part := func(table string) string {
		return fmt.Sprintf(
			"SELECT concat(study_id, '_', sample_id) AS sample_unique_id, "+
				"study_id AS cancer_study_identifier, "+
				"hugo_symbol AS hugo_gene_symbol, "+
				"profile_stable_id AS profile_type, "+
				"nullIf(value, '') AS alteration_value "+
				"FROM `%s`.%s "+
				"WHERE value NOT IN ('NA', '') AND sample_id != ''",
			rawDB, table)
	}
	return part("cna_discrete_long") + " UNION ALL " +
		part("cna_matrix") + " UNION ALL " +
		part("methylation_matrix") + " UNION ALL " +
		part("mrna_expression_matrix")
}

// genericAssayDataDerivedSQL builds prod's `generic_assay_data_derived` from the
// generic_assay_matrix staging table. profile_stable_id is the FULL molecular
// profile id (`<study>_<suffix>`, from genetic_profile.molecular_profile_id) while
// profile_type is the bare suffix, matching prod. patient_unique_id resolves via
// the transform `cbioportal.sample` view (LEFT JOIN keeps rows whose sample id is
// not in clinical data; prod's INNER JOIN would drop them). genetic_entity_id is
// synthesized deterministically from entity_stable_id since we have no genetic
// entity table. generic_assay_type and patient_level are not captured by ingest,
// so they default to empty string and 0 respectively.
func genericAssayDataDerivedSQL(rawDB, refDB string) string {
	_ = refDB
	return fmt.Sprintf(
		"SELECT concat(gam.study_id, '_', gam.sample_id) AS sample_unique_id, "+
			"concat(gam.study_id, '_', s.patient_id) AS patient_unique_id, "+
			"toString(cityHash64(gam.entity_stable_id)) AS genetic_entity_id, "+
			"gam.value AS value, "+
			"'' AS generic_assay_type, "+
			"gp.molecular_profile_id AS profile_stable_id, "+
			"gam.entity_stable_id AS entity_stable_id, "+
			"gp.datatype AS datatype, "+
			"toUInt8(0) AS patient_level, "+
			"gam.profile_stable_id AS profile_type "+
			"FROM `%s`.generic_assay_matrix gam "+
			"LEFT JOIN `%s`.genetic_profile gp "+
			"ON gp.study_id = gam.study_id AND gp.profile_stable_id = gam.profile_stable_id "+
			"LEFT JOIN cbioportal.sample s "+
			"ON s.study_id = gam.study_id AND s.sample_id = gam.sample_id "+
			"WHERE gam.sample_id != ''",
		rawDB, rawDB)
}
