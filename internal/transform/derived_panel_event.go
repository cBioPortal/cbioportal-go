package transform

import "fmt"

// sampleToGenePanelDerivedSQL reproduces prod's sample_to_gene_panel_derived
// (sample_unique_id, alteration_type, gene_panel_id, cancer_study_identifier,
// genetic_profile_id) over the target-DB `sample_profile` view: one row per
// profiled (sample, profile), with a NULL panel (whole exome/genome or no
// panel declared) reported as 'WES', as prod does.
func sampleToGenePanelDerivedSQL(targetDB string) string {
	return inTarget(targetDB,
		"SELECT "+
			"concat(sp.study_id, '_', sp.sample_id) AS sample_unique_id, "+
			"sp.alteration_type AS alteration_type, "+
			"ifNull(sp.gene_panel_id, 'WES') AS gene_panel_id, "+
			"sp.study_id AS cancer_study_identifier, "+
			"sp.molecular_profile_id AS genetic_profile_id "+
			"FROM {{target}}.sample_profile sp")
}

// genePanelToGeneDerivedSQL reproduces prod's gene_panel_to_gene_derived
// (columns gene_panel_id, gene). Our gene_panel staging lists the panel's Hugo
// symbols directly, so no gene-list join is needed. The trailing UNION appends
// the reserved 'WES' panel = every reference gene with a real Entrez id, matching
// prod's whole-exome expansion used by the off_panel check.
func genePanelToGeneDerivedSQL(rawDB, refDB string) string {
	return fmt.Sprintf(
		"SELECT DISTINCT gp.gene_panel_id AS gene_panel_id, gp.hugo_symbol AS gene "+
			"FROM `%s`.gene_panel gp WHERE gp.gene_panel_id != '' "+
			"UNION ALL "+
			"SELECT DISTINCT 'WES' AS gene_panel_id, g.hugo_gene_symbol AS gene "+
			"FROM `%s`.gene g WHERE g.entrez_gene_id > 0",
		rawDB, refDB)
}

// clinicalEventDerivedSQL reproduces prod's clinical_event_derived from our
// timeline staging (one row per event). Prod columns: clinical_event_id,
// patient_id, patient_stable_id, start_date, stop_date, event_type,
// cancer_study_identifier. We have no DB primary keys, so clinical_event_id is a
// deterministic hash of the full event row and patient_id is the deterministic
// patient internal id used across all derived tables.
func clinicalEventDerivedSQL(rawDB, refDB string) string {
	return fmt.Sprintf(
		"SELECT "+
			"reinterpretAsInt64(cityHash64(t.study_id, t.patient_id, t.event_type, t.start_date, t.stop_date, "+
			"t.surgery, t.status, t.event_type_detailed, t.treatment_type, t.subtype, t.agent, t.sample_id)) AS clinical_event_id, "+
			"toInt64(cityHash64(concat(t.study_id, '_', t.patient_id)) %% 2147483647) AS patient_id, "+
			"t.patient_id AS patient_stable_id, "+
			"toInt64OrNull(t.start_date) AS start_date, "+
			"toInt64OrNull(t.stop_date) AS stop_date, "+
			"t.event_type AS event_type, "+
			"t.study_id AS cancer_study_identifier "+
			"FROM `%s`.timeline t",
		rawDB)
}

// clinicalEventDataDerivedSQL reproduces prod's clinical_event_data_derived: the
// per-event key/value attributes. Prod columns: patient_unique_id, key, value,
// start_date, stop_date, event_type, cancer_study_identifier. The timeline
// staging's non-standard detail columns are the file's attribute headers
// (lowercased), so we unpivot them back to UPPER(header)=key with non-empty
// values.
func clinicalEventDataDerivedSQL(rawDB, refDB string) string {
	return fmt.Sprintf(
		"SELECT "+
			"concat(t.study_id, '_', t.patient_id) AS patient_unique_id, "+
			"kv.1 AS key, "+
			"kv.2 AS value, "+
			"toInt32OrZero(t.start_date) AS start_date, "+
			"toInt32OrZero(t.stop_date) AS stop_date, "+
			"t.event_type AS event_type, "+
			"t.study_id AS cancer_study_identifier "+
			"FROM `%s`.timeline t "+
			"ARRAY JOIN ["+
			"('SURGERY', t.surgery), "+
			"('STATUS', t.status), "+
			"('EVENT_TYPE_DETAILED', t.event_type_detailed), "+
			"('TREATMENT_TYPE', t.treatment_type), "+
			"('SUBTYPE', t.subtype), "+
			"('AGENT', t.agent), "+
			"('SAMPLE_ID', t.sample_id)"+
			"] AS kv "+
			"WHERE kv.2 != ''",
		rawDB)
}
