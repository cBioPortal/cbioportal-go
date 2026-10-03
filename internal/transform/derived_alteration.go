package transform

import "fmt"

// geneticAlterationDerivedSQL builds prod's `genetic_alteration_derived`: the
// generic per-(sample, gene, profile) value table covering every non-mutation,
// non-SV, non-generic-assay molecular profile. Prod derives it from the
// genetic_alteration blob keyed by gene; we source directly from the per-gene
// long/matrix staging tables, with each line's gene resolved through
// gene_key_map as the importers store it (unresolved and duplicate-gene lines
// are not stored), so hugo_gene_symbol is the reference symbol.
//
// profile_type matches prod's INSERT semantics — the profile stable id with the
// study prefix stripped (e.g. `gistic`, `log2CNA`, `mrna`), which in our staging
// is already the bare `profile_stable_id` column. alteration_value drops NA
// and empty just as prod does (prod maps empty to NULL then filters out NA).
func geneticAlterationDerivedSQL(rawDB, targetDB string) string {
	part := func(table string) string {
		return fmt.Sprintf(
			"SELECT concat(t.study_id, '_', t.sample_id) AS sample_unique_id, "+
				"t.study_id AS cancer_study_identifier, "+
				"gk.resolved_hugo_gene_symbol AS hugo_gene_symbol, "+
				"t.profile_stable_id AS profile_type, "+
				"nullIf(t.value, '') AS alteration_value "+
				"FROM `%s`.%s t%s "+
				"WHERE t.value NOT IN ('NA', '') AND t.sample_id != ''",
			rawDB, table, geneKeyJoin(targetDB, table, "t", "hugo_symbol", "entrez_gene_id"))
	}
	return part("cna_discrete_long") + " UNION ALL " +
		part("cna_matrix") + " UNION ALL " +
		part("methylation_matrix") + " UNION ALL " +
		part("mrna_expression_matrix")
}

// genericAssayDataDerivedSQL builds prod's `generic_assay_data_derived` from the
// generic_assay_matrix staging table. profile_stable_id is the FULL molecular
// profile id (`<study>_<suffix>`, from genetic_profile.molecular_profile_id) while
// profile_type is the bare suffix, matching prod. genetic_entity_id is
// synthesized deterministically from entity_stable_id since we have no genetic
// entity table. The matrix columns of a patient-level profile are patient ids:
// as ImportGenericAssayPatientLevelData stores the patient's value for each of
// its samples, those rows are expanded to every sample of the patient. For a
// sample-level profile the LEFT JOIN keeps rows whose sample id is not in
// clinical data (prod's INNER JOIN would drop them). hasProfileMeta says the
// staging genetic_profile has the generic_assay_type and patient_level columns.
func genericAssayDataDerivedSQL(rawDB, refDB, targetDB string, hasProfileMeta bool) string {
	_ = refDB
	gaType, patientLevel := "''", "toUInt8(0)"
	if hasProfileMeta {
		gaType = "ifNull(gp.generic_assay_type, '')"
		patientLevel = "toUInt8(lower(toString(ifNull(gp.patient_level, ''))) IN ('true', '1'))"
	}
	inner := fmt.Sprintf("SELECT gam.study_id AS study_id, gam.sample_id AS case_id, gam.entity_stable_id AS entity_stable_id, "+
		"gam.value AS value, gam.profile_stable_id AS profile_type, gp.molecular_profile_id AS profile_stable_id, "+
		"gp.datatype AS datatype, %s AS generic_assay_type, %s AS patient_level "+
		"FROM `%s`.generic_assay_matrix gam "+
		"LEFT JOIN `%s`.genetic_profile gp "+
		"ON gp.study_id = gam.study_id AND gp.profile_stable_id = gam.profile_stable_id "+
		"WHERE gam.sample_id != ''", gaType, patientLevel, rawDB, rawDB)
	cols := func(sampleCol, patientCol string) string {
		return fmt.Sprintf("SELECT concat(m.study_id, '_', %s) AS sample_unique_id, "+
			"concat(m.study_id, '_', %s) AS patient_unique_id, "+
			"toString(cityHash64(m.entity_stable_id)) AS genetic_entity_id, "+
			"m.value AS value, m.generic_assay_type AS generic_assay_type, m.profile_stable_id AS profile_stable_id, "+
			"m.entity_stable_id AS entity_stable_id, m.datatype AS datatype, m.patient_level AS patient_level, "+
			"m.profile_type AS profile_type ", sampleCol, patientCol)
	}
	return inTarget(targetDB, cols("m.case_id", "s.patient_id")+
		"FROM ("+inner+") m LEFT JOIN {{target}}.sample s ON s.study_id = m.study_id AND s.sample_id = m.case_id "+
		"WHERE m.patient_level = 0 "+
		"UNION ALL "+cols("s.sample_id", "m.case_id")+
		"FROM ("+inner+") m JOIN {{target}}.sample s ON s.study_id = m.study_id AND s.patient_id = m.case_id "+
		"WHERE m.patient_level = 1")
}
