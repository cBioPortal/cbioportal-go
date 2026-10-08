package transform

import (
	"fmt"
	"sort"
	"strings"
)

// mutationFilterSQL is cBioPortal's MutationFilter, as
// ImportExtendedMutationData applies it, as a WHERE condition over a MAF
// staging alias; entrez is the resolved Entrez id expression and targetDB
// holds the variant_classification_filter table. Records whose chromosome
// DaoGeneOptimized.normalizeChr does not recognize, RNA records, records
// whose validation status is Wildtype and records with a None, LOH or
// Wildtype mutation status or a Redacted validation status are skipped. A profile whose meta file sets
// variant_classification_filter skips exactly the listed
// Variant_Classification values; otherwise the default filter skips
// Silent, Intron, 3'UTR, 3'Flank, 5'UTR and IGR, and 5'Flank except in the
// promoter whitelist (TERT = 7015). The default filter and the statuses
// match by case-insensitive prefix, as MutationFilter.safeStringTest does.
// Of the records the importer merges into one (mutation_merge), only the
// one it keeps passes.
func mutationFilterSQL(a, entrez, targetDB string) string {
	return acceptedMutationSQL(a, entrez, targetDB) + fmt.Sprintf(
		" AND (%[1]s.study_id, %[1]s.profile_stable_id, %[1]s.file_num, %[1]s.row_num) NOT IN "+
			"(SELECT study_id, profile_stable_id, file_num, row_num FROM `%[2]s`.mutation_merge WHERE keep = 0)", a, targetDB)
}

// acceptedMutationSQL is mutationFilterSQL before merging: every record
// the MutationFilter accepts.
func acceptedMutationSQL(a, entrez, targetDB string) string {
	prefix := func(col string, values ...string) string {
		var or []string
		for _, v := range values {
			or = append(or, fmt.Sprintf("startsWith(lower(%s.%s), '%s')", a, col, strings.ReplaceAll(v, "'", "''")))
		}
		return "(" + strings.Join(or, " OR ") + ")"
	}
	profile := fmt.Sprintf("(%[1]s.study_id, %[1]s.profile_stable_id)", a)
	filters := fmt.Sprintf("`%s`.variant_classification_filter", targetDB)
	return normalizedChrSQL(a+".chromosome") + " != '' AND " + fmt.Sprintf(
		"NOT %[2]s AND NOT %[3]s AND lower(%[1]s.variant_classification) != 'rna' AND lower(%[1]s.validation_status) != 'wildtype' "+
			"AND if(%[4]s IN (SELECT study_id, profile_stable_id FROM %[5]s), "+
			"(%[1]s.study_id, %[1]s.profile_stable_id, %[1]s.variant_classification) NOT IN "+
			"(SELECT study_id, profile_stable_id, variant_classification FROM %[5]s), "+
			"NOT %[6]s AND (NOT %[7]s OR %[8]s = 7015))",
		a, prefix("mutation_status", "none", "loh", "wildtype"), prefix("validation_status", "redacted"),
		profile, filters, prefix("variant_classification", "silent", "intron", "3'utr", "3'flank", "5'utr", "igr"),
		prefix("variant_classification", "5'flank"), entrez)
}

// variantClassificationFilterSQL selects the variant_classification_filter
// table: per MAF profile whose meta file sets the key, one row per
// Variant_Classification value it filters out (a row with an empty value
// when the list is empty).
func variantClassificationFilterSQL(c *client, rawDB string) string {
	if !hasColumn(c, rawDB, "genetic_profile", "variant_classification_filter") {
		return "SELECT '' AS study_id, '' AS profile_stable_id, '' AS variant_classification WHERE 0"
	}
	return fmt.Sprintf(
		"SELECT study_id, profile_stable_id, arrayJoin(if(empty(f), [''], f)) AS variant_classification "+
			"FROM (SELECT study_id, profile_stable_id, JSONExtract(variant_classification_filter, 'Array(String)') AS f "+
			"FROM `%s`.genetic_profile WHERE variant_classification_filter != '')", rawDB)
}

// normalizedChrSQL is DaoGeneOptimized.normalizeChr over the upper-cased
// value: 1-24 with an optional CHR prefix, X and Y as 23 and 24, NA and MT;
// anything else reads as empty (the importer skips the record).
func normalizedChrSQL(x string) string {
	u := fmt.Sprintf("upper(%s)", x)
	return fmt.Sprintf("multiIf(%[1]s IN ('X', 'CHRX'), '23', %[1]s IN ('Y', 'CHRY'), '24', %[1]s IN ('NA', 'MT'), %[1]s, "+
		"match(%[1]s, '^(CHR)?([1-9]|1[0-9]|2[0-4])$'), replaceRegexpOne(%[1]s, '^CHR', ''), '')", u)
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
//   - MAF (ImportExtendedMutationData): samples with ≥1 mutation whose gene
//     resolves and that passes the default MutationFilter, plus the MAF's
//     `#sequenced_samples:` ids.
//   - SV (ImportStructuralVariantData): samples of the stored structural
//     variants (target-DB structural_variant).
//   - discrete-long CNA (ImportCnaDiscreteLongData): samples in the file.
//   - wide matrices (ImportTabDelimData: CNA, expression, methylation, GSVA,
//     sample-level generic assay): every sample column of the header.
//   - patient-level generic assay: every sample of each patient column.
//
// Non-matrix sources take the panel from the meta file's `gene_panel` key.
// Finally, as prod's derived-table build does, members of a `*_sequenced` case
// list that are in no MUTATION_EXTENDED profile of their study are added to
// `<study>_mutations` with a NULL panel (case_list_only = 1: the importer's
// own sample_profile, which MUTATION_COUNT is computed from, lacks them).
// Only samples present in clinical data
// and profiles present in genetic_profile are kept (prod's INNER JOINs).
//
// Header ids come from the ingest-captured `profile_sample` staging table; for
// raw databases ingested before it existed, the distinct sample ids of each
// matrix staging table stand in for the header.
//
// The SQL is always valid; profiled reports whether any sample can be
// profiled at all (profiles, clinical samples and a membership source staged).
func sampleProfileSQL(c *client, rawDB, targetDB string) (sql string, profiled bool) {
	exists := func(t string) bool { ok, _ := c.TableExists(t); return ok }
	profiled = exists("genetic_profile") && exists("clinical_sample")
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
			if !skip[n] && !OrderColumns[n] {
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
		// Only records whose gene resolves and that pass the filter are
		// stored, and only their samples are profiled. Columns the MAF
		// lacks read as empty (stagingSources).
		sources = append(sources, fmt.Sprintf(
			"SELECT DISTINCT r.study_id AS study_id, r.tumor_sample_barcode AS sample_id, r.profile_stable_id AS profile_stable_id, "+
				"'' AS panel, toUInt8(1) AS prio FROM `%s`.%s r%s WHERE %s",
			rawDB, t, geneKeyJoin(targetDB, t, "r", "hugo_symbol", "entrez_gene_id"),
			mutationFilterSQL("r", "gk.resolved_entrez_gene_id", targetDB)))
	}
	if exists("structural_variant") {
		// Samples of the structural variants actually stored.
		sources = append(sources, fmt.Sprintf(
			"SELECT DISTINCT study_id, sample_id, profile_stable_id, '' AS panel, toUInt8(1) AS prio FROM `%s`.structural_variant", targetDB))
	}
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
		profiled = false
		sources = append(sources, "SELECT '' AS study_id, '' AS sample_id, '' AS profile_stable_id, '' AS panel, toUInt8(1) AS prio WHERE 0")
	}

	base := fmt.Sprintf(
		"SELECT a.study_id AS study_id, a.sample_id AS sample_id, gp.profile_stable_id AS profile_stable_id, "+
			"gp.molecular_profile_id AS molecular_profile_id, gp.alteration_type AS alteration_type, gp.datatype AS datatype, "+
			"CAST(if(a.p = 0, nullIf(a.mpanel, ''), nullIf(%[1]s, '')) AS Nullable(String)) AS gene_panel_id, "+
			"toUInt8(0) AS case_list_only "+
			"FROM (SELECT study_id, sample_id, profile_stable_id, min(prio) AS p, argMin(panel, prio) AS mpanel "+
			"FROM (%[2]s) GROUP BY study_id, sample_id, profile_stable_id) a "+
			"INNER JOIN `%[3]s`.genetic_profile gp ON gp.study_id = a.study_id AND gp.profile_stable_id = a.profile_stable_id "+
			"INNER JOIN %[4]s s ON s.study_id = a.study_id AND s.sample_id = a.sample_id",
		gpCol("gene_panel"), strings.Join(sources, " UNION ALL "), rawDB, samples)

	if !exists("sample_list") {
		return base, profiled
	}
	missing := fmt.Sprintf(
		"SELECT sl.study_id AS study_id, sl.sample_id AS sample_id, gp.profile_stable_id AS profile_stable_id, "+
			"gp.molecular_profile_id AS molecular_profile_id, gp.alteration_type AS alteration_type, gp.datatype AS datatype, "+
			"CAST(NULL AS Nullable(String)) AS gene_panel_id, toUInt8(1) AS case_list_only "+
			"FROM (SELECT DISTINCT study_id, sample_id FROM `%[1]s`.sample_list WHERE list_id LIKE '%%_sequenced') sl "+
			"INNER JOIN `%[1]s`.genetic_profile gp ON gp.study_id = sl.study_id AND gp.molecular_profile_id = concat(sl.study_id, '_mutations') "+
			"INNER JOIN %[2]s s ON s.study_id = sl.study_id AND s.sample_id = sl.sample_id "+
			"WHERE (sl.study_id, sl.sample_id) NOT IN (SELECT study_id, sample_id FROM base WHERE alteration_type = 'MUTATION_EXTENDED')",
		rawDB, samples)
	return "WITH base AS (" + base + ") SELECT * FROM base UNION ALL " + missing, profiled
}
