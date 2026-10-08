package transform

import (
	"fmt"
	"slices"
	"strings"
)

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
// (columns gene_panel_id, gene) from the target-DB gene_panel view, whose
// genes are already resolved to the reference symbols. The trailing UNION
// appends the reserved 'WES' panel = every reference gene with a real Entrez
// id, matching prod's whole-exome expansion used by the off_panel check.
func genePanelToGeneDerivedSQL(targetDB, refDB string) string {
	return fmt.Sprintf(
		"SELECT DISTINCT gp.gene_panel_id AS gene_panel_id, gp.hugo_gene_symbol AS gene "+
			"FROM `%s`.gene_panel gp WHERE gp.gene_panel_id != '' "+
			"UNION ALL "+
			"SELECT DISTINCT 'WES' AS gene_panel_id, g.hugo_gene_symbol AS gene "+
			"FROM `%s`.gene g WHERE g.entrez_gene_id > 0",
		targetDB, refDB)
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

// TimelineCoreColumns are the staged timeline columns that are not event
// data: the provenance columns and the event's own fields, which
// ImportTimelineData reads from the header's leading PATIENT_ID,
// START_DATE, [STOP_DATE,] EVENT_TYPE.
var TimelineCoreColumns = map[string]bool{
	"study_id": true, "source_file": true, "alteration_type": true, "datatype": true, "profile_stable_id": true,
	"patient_id": true, "start_date": true, "stop_date": true, "event_type": true,
}

// clinicalEventDataDerivedSQL reproduces prod's clinical_event_data_derived: the
// per-event key/value attributes. Prod columns: patient_unique_id, key, value,
// start_date, stop_date, event_type, cancer_study_identifier.
//
// ImportTimelineData stores every column after the event's core fields as
// event data, SAMPLE_ID included: key = the header as the file writes it,
// value = the cell, skipping empty cells (NA is kept); of repeated headers
// the last non-empty cell wins. Every String column of the staged timeline
// that is not core is unpivoted; its key is the file's header recorded in
// source_header, else the upper-cased staged name (files staged before
// headers were recorded).
func clinicalEventDataDerivedSQL(c *client, rawDB string) string {
	cols := stringColumns(c, rawDB, "timeline")
	var kv []string
	for _, col := range cols {
		if !TimelineCoreColumns[col] && !OrderColumns[col] {
			kv = append(kv, fmt.Sprintf("('%s', t.`%s`)", col, col))
		}
	}
	if len(kv) == 0 || !slices.Contains(cols, "source_file") {
		return "SELECT '' AS patient_unique_id, '' AS key, '' AS value, toInt32(0) AS start_date, " +
			"toInt32(0) AS stop_date, '' AS event_type, '' AS cancer_study_identifier WHERE 0"
	}
	tl, _ := c.TableColumns("timeline")
	rowNum := "toUInt64(0)"
	if tl["row_num"] {
		rowNum = "t.row_num"
	}
	events := fmt.Sprintf(
		"SELECT concat(t.study_id, '_', t.patient_id) AS patient_unique_id, kv.1 AS column_name, kv.2 AS value, "+
			"toInt32OrZero(t.start_date) AS start_date, toInt32OrZero(t.stop_date) AS stop_date, "+
			"t.event_type AS event_type, t.study_id AS cancer_study_identifier, "+
			"t.source_file AS source_file, %s AS row_num "+
			"FROM `%s`.timeline t ARRAY JOIN [%s] AS kv WHERE kv.2 != ''",
		rowNum, rawDB, strings.Join(kv, ", "))
	out := "SELECT patient_unique_id, key, value, start_date, stop_date, event_type, cancer_study_identifier FROM "
	if hc, _ := c.TableColumns("source_header"); !hc["header"] {
		return out + "(SELECT *, upper(column_name) AS key FROM (" + events + "))"
	}
	// Rows are told apart by (source_file, row_num); without row_num the
	// repeated-header rule is not applied.
	dedup := ""
	if tl["row_num"] {
		dedup = " ORDER BY position DESC LIMIT 1 BY cancer_study_identifier, source_file, row_num, key"
	}
	return fmt.Sprintf(
		"%s(SELECT e.*, if(h.column_name = '', upper(e.column_name), h.header) AS key, h.position AS position "+
			"FROM (%s) e LEFT JOIN (SELECT study_id, source_file, column_name, header, position FROM `%s`.source_header "+
			"WHERE table_name = 'timeline') h "+
			"ON h.study_id = e.cancer_study_identifier AND h.source_file = e.source_file AND h.column_name = e.column_name%s)",
		out, events, rawDB, dedup)
}

// stringColumns lists a raw table's String columns in table order (none
// when the table is absent).
func stringColumns(c *client, rawDB, table string) []string {
	text, _ := c.Client.Execute(fmt.Sprintf(
		"SELECT name FROM system.columns WHERE database = '%s' AND table = '%s' AND type = 'String' "+
			"ORDER BY position FORMAT TabSeparated", rawDB, table))
	var out []string
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
