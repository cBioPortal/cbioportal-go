package transform

import (
	"fmt"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// mutationFilterSQL is cBioPortal's default MutationFilter (no
// filtered_mutations override) as a WHERE condition over a MAF staging alias.
func mutationFilterSQL(a string) string {
	return fmt.Sprintf(
		"NOT startsWith(lower(%[1]s.mutation_status), 'none') AND NOT startsWith(lower(%[1]s.mutation_status), 'loh') "+
			"AND NOT startsWith(lower(%[1]s.mutation_status), 'wildtype') AND NOT startsWith(lower(%[1]s.validation_status), 'redacted') "+
			"AND lower(%[1]s.variant_classification) NOT IN ('silent', 'intron', '3''utr', '5''utr', '3''flank', 'igr', 'rna') "+
			"AND ( lower(%[1]s.variant_classification) != '5''flank' OR %[1]s.entrez_gene_id = '7015' OR upper(%[1]s.hugo_symbol) = 'TERT' )",
		a)
}

// sanitizeIdentSQL mirrors parser.SanitizeIdent in SQL so profile stable ids
// can be matched against gene_panel_matrix staging column names.
func sanitizeIdentSQL(expr string) string {
	return fmt.Sprintf("trim(BOTH '_' FROM replaceRegexpAll(lower(%s), '[^a-z0-9]', '_'))", expr)
}

// sampleProfileSQL builds the `sample_profile` view: one row per (sample,
// molecular profile) the sample was profiled in, with the gene panel used
// (NULL = whole exome/genome). It reproduces cbioportal-core's sample_profile
// table, which every profile importer upserts into, keyed on (sample, profile):
//
//   - gene panel matrix (ImportGenePanelProfileMap): each non-NA cell, column =
//     profile, value = panel (WXS/WGS/WXS/WGS → NULL). Imported last, so its
//     panel wins over every other source.
//   - MAF (ImportExtendedMutationData): samples with ≥1 mutation passing the
//     default MutationFilter, plus the MAF's `#sequenced_samples:` ids.
//   - SV (ImportStructuralVariantData) and discrete-long CNA
//     (ImportCnaDiscreteLongData): samples present in the file.
//   - wide matrices (ImportTabDelimData: CNA, expression, methylation, GSVA,
//     sample-level generic assay): every sample column of the header.
//   - patient-level generic assay: every sample of each patient column.
//
// Non-matrix sources take the panel from the meta file's `gene_panel` key.
// Finally, as prod's derived-table build does, members of a `*_sequenced` case
// list that are in no MUTATION_EXTENDED profile of their study are added to
// `<study>_mutations` with a NULL panel. Only samples present in clinical data
// and profiles present in genetic_profile are kept (prod's INNER JOINs).
//
// Header ids come from the ingest-captured `profile_sample` staging table; for
// raw databases ingested before it existed, the distinct sample ids of each
// matrix staging table stand in for the header.
func sampleProfileSQL(c *ch.Client, rawDB string) (string, bool) {
	exists := func(t string) bool { ok, _ := c.TableExists(t); return ok }
	if !exists("genetic_profile") || !exists("clinical_sample") {
		return "", false
	}
	gpCols, _ := c.TableColumns("genetic_profile")
	gpCol := func(name string) string {
		if gpCols[name] {
			return "gp." + name
		}
		return "''"
	}
	samples := fmt.Sprintf(
		"(SELECT DISTINCT study_id, sample_id, patient_id FROM `%s`.clinical_sample WHERE sample_id != '')", rawDB)

	// Sources: (study_id, sample_id, profile_stable_id, panel, prio); prio 0 is
	// the gene panel matrix, whose panel overrides the meta-file panel.
	var sources []string

	if exists("gene_panel_matrix") {
		cols, _ := c.TableColumns("gene_panel_matrix")
		skip := map[string]bool{"study_id": true, "source_file": true, "alteration_type": true,
			"datatype": true, "profile_stable_id": true, "sample_id": true}
		var names []string
		for n := range cols {
			if !skip[n] {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		if len(names) > 0 {
			pairs := make([]string, len(names))
			for i, n := range names {
				pairs[i] = fmt.Sprintf("('%s', m.`%s`)", n, n)
			}
			sources = append(sources, fmt.Sprintf(
				"SELECT u.study_id AS study_id, u.sample_id AS sample_id, pm.profile_stable_id AS profile_stable_id, "+
					"if(upper(u.panel) IN ('WXS', 'WGS', 'WXS/WGS'), '', u.panel) AS panel, toUInt8(0) AS prio "+
					"FROM (SELECT m.study_id AS study_id, m.sample_id AS sample_id, kv.1 AS col, kv.2 AS panel "+
					"FROM `%[1]s`.gene_panel_matrix m ARRAY JOIN [%[2]s] AS kv WHERE kv.2 != '' AND kv.2 != 'NA') u "+
					"INNER JOIN (SELECT DISTINCT study_id, profile_stable_id, "+
					"arrayJoin([%[3]s, %[4]s]) AS col FROM `%[1]s`.genetic_profile) pm "+
					"ON u.study_id = pm.study_id AND u.col = pm.col",
				rawDB, strings.Join(pairs, ", "),
				sanitizeIdentSQL("profile_stable_id"), sanitizeIdentSQL("concat(study_id, '_', profile_stable_id)")))
		}
	}

	record := func(table, sampleCol, where string) {
		if !exists(table) {
			return
		}
		w := ""
		if where != "" {
			w = " WHERE " + where
		}
		sources = append(sources, fmt.Sprintf(
			"SELECT DISTINCT r.study_id AS study_id, r.%s AS sample_id, r.profile_stable_id AS profile_stable_id, "+
				"'' AS panel, toUInt8(1) AS prio FROM `%s`.%s r%s",
			sampleCol, rawDB, table, w))
	}
	for _, t := range []string{"mutation_extended", "mutation_uncalled"} {
		if !exists(t) {
			continue
		}
		mc, _ := c.TableColumns(t)
		where := ""
		if mc["mutation_status"] && mc["validation_status"] && mc["variant_classification"] &&
			mc["entrez_gene_id"] && mc["hugo_symbol"] {
			where = mutationFilterSQL("r")
		}
		record(t, "tumor_sample_barcode", where)
	}
	record("structural_variant", "sample_id", "")
	record("cna_discrete_long", "sample_id", "")

	// Declared case ids: (study_id, id, profile_stable_id), where id is a
	// patient id for patient-level generic assay profiles.
	var declared []string
	if exists("profile_sample") {
		declared = append(declared, fmt.Sprintf(
			"SELECT study_id, sample_id AS id, profile_stable_id FROM `%s`.profile_sample", rawDB))
	} else {
		tables, _ := c.Execute(fmt.Sprintf(
			"SELECT name FROM system.tables WHERE database = '%s' AND endsWith(name, '_matrix') "+
				"AND name != 'gene_panel_matrix' ORDER BY name FORMAT TabSeparated", rawDB))
		for t := range strings.FieldsSeq(tables) {
			declared = append(declared, fmt.Sprintf(
				"SELECT DISTINCT study_id, sample_id AS id, profile_stable_id FROM `%s`.`%s`", rawDB, t))
		}
	}
	if len(declared) > 0 {
		patientLevel := fmt.Sprintf(
			"(SELECT study_id, profile_stable_id FROM `%s`.genetic_profile gp WHERE lower(%s) = 'true')",
			rawDB, gpCol("patient_level"))
		decl := "(" + strings.Join(declared, " UNION ALL ") + ")"
		sources = append(sources,
			fmt.Sprintf(
				"SELECT d.study_id AS study_id, d.id AS sample_id, d.profile_stable_id AS profile_stable_id, "+
					"'' AS panel, toUInt8(1) AS prio FROM %s d "+
					"WHERE (d.study_id, d.profile_stable_id) NOT IN %s", decl, patientLevel),
			fmt.Sprintf(
				"SELECT s.study_id AS study_id, s.sample_id AS sample_id, d.profile_stable_id AS profile_stable_id, "+
					"'' AS panel, toUInt8(1) AS prio FROM %s d "+
					"INNER JOIN %s s ON s.study_id = d.study_id AND s.patient_id = d.id "+
					"WHERE (d.study_id, d.profile_stable_id) IN %s", decl, samples, patientLevel))
	}
	if len(sources) == 0 {
		return "", false
	}

	base := fmt.Sprintf(
		"SELECT a.study_id AS study_id, a.sample_id AS sample_id, gp.profile_stable_id AS profile_stable_id, "+
			"gp.molecular_profile_id AS molecular_profile_id, gp.alteration_type AS alteration_type, gp.datatype AS datatype, "+
			"CAST(if(a.p = 0, nullIf(a.mpanel, ''), nullIf(%[1]s, '')) AS Nullable(String)) AS gene_panel_id "+
			"FROM (SELECT study_id, sample_id, profile_stable_id, min(prio) AS p, argMin(panel, prio) AS mpanel "+
			"FROM (%[2]s) GROUP BY study_id, sample_id, profile_stable_id) a "+
			"INNER JOIN `%[3]s`.genetic_profile gp ON gp.study_id = a.study_id AND gp.profile_stable_id = a.profile_stable_id "+
			"INNER JOIN %[4]s s ON s.study_id = a.study_id AND s.sample_id = a.sample_id",
		gpCol("gene_panel"), strings.Join(sources, " UNION ALL "), rawDB, samples)

	if !exists("sample_list") {
		return base, true
	}
	missing := fmt.Sprintf(
		"SELECT sl.study_id AS study_id, sl.sample_id AS sample_id, gp.profile_stable_id AS profile_stable_id, "+
			"gp.molecular_profile_id AS molecular_profile_id, gp.alteration_type AS alteration_type, gp.datatype AS datatype, "+
			"CAST(NULL AS Nullable(String)) AS gene_panel_id "+
			"FROM (SELECT DISTINCT study_id, sample_id FROM `%[1]s`.sample_list WHERE list_id LIKE '%%_sequenced') sl "+
			"INNER JOIN `%[1]s`.genetic_profile gp ON gp.study_id = sl.study_id AND gp.molecular_profile_id = concat(sl.study_id, '_mutations') "+
			"INNER JOIN %[2]s s ON s.study_id = sl.study_id AND s.sample_id = sl.sample_id "+
			"WHERE (sl.study_id, sl.sample_id) NOT IN (SELECT study_id, sample_id FROM base WHERE alteration_type = 'MUTATION_EXTENDED')",
		rawDB, samples)
	return "WITH base AS (" + base + ") SELECT * FROM base UNION ALL " + missing, true
}
