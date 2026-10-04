package transform

import (
	"fmt"
	"strings"
)

// sampleListSQL builds the `sample_list` view (one row per list x sample).
//
// Study case lists follow cbioportal-core ImportSampleList: an empty category
// is "other"; each listed id must be a sample of the study, or else a patient
// id, which stands for all of that patient's samples; unknown ids and
// duplicates are dropped.
//
// A study whose meta file sets add_global_case_list: true also gets
// AddCaseList's "<study>_all" list (every sample, category "other", name and
// description "All cases in study"), unless a case list with that stable id
// already exists, which is kept.
func sampleListSQL(c *client, rawDB string) string {
	exists := func(t string) bool { ok, _ := c.TableExists(t); return ok }
	empty := "SELECT '' AS study_id, '' AS list_id, '' AS category, '' AS name, '' AS description, '' AS sample_id, " +
		"toUInt64(0) AS list_internal_id WHERE 0"
	if !exists("clinical_sample") {
		return empty
	}
	samples := fmt.Sprintf(
		"(SELECT DISTINCT study_id, sample_id, patient_id FROM `%s`.clinical_sample WHERE sample_id != '')", rawDB)
	var parts []string
	if exists("sample_list") {
		parts = append(parts, fmt.Sprintf(
			"SELECT l_study AS study_id, l_list AS list_id, if(trimBoth(l_category) = '', 'other', l_category) AS category, "+
				"l_name AS name, l_description AS description, i_sample AS sample_id FROM ("+
				"SELECT DISTINCT sl.l_study, sl.l_list, sl.l_category, sl.l_name, sl.l_description, ids.i_sample FROM (SELECT study_id AS l_study, list_id AS l_list, category AS l_category, name AS l_name, "+
				"description AS l_description, sample_id AS l_id FROM `%[1]s`.sample_list) sl INNER JOIN ("+
				"SELECT study_id AS i_study, sample_id AS i_id, sample_id AS i_sample FROM %[2]s "+
				"UNION ALL SELECT study_id AS i_study, patient_id AS i_id, sample_id AS i_sample FROM %[2]s "+
				"WHERE (study_id, patient_id) NOT IN (SELECT study_id, sample_id FROM %[2]s)"+
				") ids ON ids.i_study = sl.l_study AND ids.i_id = sl.l_id)",
			rawDB, samples))
	}
	if cols, _ := c.TableColumns("cancer_study"); cols["add_global_case_list"] {
		existing := ""
		if exists("sample_list") {
			existing = fmt.Sprintf(" AND concat(s.study_id, '_all') NOT IN (SELECT list_id FROM `%s`.sample_list)", rawDB)
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT s.study_id AS study_id, concat(s.study_id, '_all') AS list_id, 'other' AS category, "+
				"'All cases in study' AS name, 'All cases in study' AS description, s.sample_id AS sample_id FROM %s s "+
				"WHERE s.study_id IN (SELECT study_id FROM `%s`.cancer_study WHERE lower(trimBoth(add_global_case_list)) = 'true')%s",
			samples, rawDB, existing))
	}
	if len(parts) == 0 {
		return empty
	}
	return listInternalIDs(rawDB, strings.Join(parts, " UNION ALL "))
}

// listInternalIDs adds to sample list rows the list's internal id as
// cbioportal-core assigns it: studies in import order, a study's case list
// files in the order the importer reads the case_lists directory, the global
// "_all" list last.
func listInternalIDs(rawDB, lists string) string {
	return fmt.Sprintf(
		"SELECT l.*, li.list_internal_id AS list_internal_id FROM (%[1]s) l LEFT JOIN ("+
			"SELECT d.study_id AS study_id, d.list_id AS list_id, "+
			"toUInt64(row_number() OVER (ORDER BY r.study_rank, o.name = '', o.seq, d.list_id)) AS list_internal_id "+
			"FROM (SELECT DISTINCT study_id, list_id FROM (%[1]s)) d "+
			"LEFT JOIN (SELECT study_id, name, seq FROM `%[2]s`.import_order WHERE kind = 'case_list') o "+
			"ON o.study_id = d.study_id AND o.name = d.list_id "+
			"LEFT JOIN (%[3]s) r ON r.study_id = d.study_id"+
			") li ON li.study_id = l.study_id AND li.list_id = l.list_id",
		lists, rawDB, studyRankSQL(rawDB))
}

// genePanelSQL builds the `gene_panel` view: each panel gene resolved like
// GenePanelUtil.extractGenes (Entrez id, Hugo symbol, else the alias's first
// gene), stored once per panel under the reference gene. Unresolvable genes
// are left out.
func genePanelSQL(rawDB, refDB string, hasRef bool) string {
	if !hasRef {
		return fmt.Sprintf(
			"SELECT study_id, gene_panel_id, description, hugo_symbol AS hugo_gene_symbol, '' AS entrez_gene_id FROM `%s`.gene_panel", rawDB)
	}
	keys := fmt.Sprintf(
		"SELECT study_id, gene_panel_id, description, hugo_symbol AS sym, '' AS ent FROM `%s`.gene_panel", rawDB)
	return fmt.Sprintf(
		"SELECT study_id, gene_panel_id, description, gene_hugo AS hugo_gene_symbol, toString(gene_entrez) AS entrez_gene_id "+
			"FROM (%s) ORDER BY study_id, gene_panel_id, gene_entrez LIMIT 1 BY study_id, gene_panel_id, gene_entrez",
		resolveGeneSQL(refDB, genePanel, keys))
}

// svStringColumns and svIntColumns are the structural-variant fields
// StructuralVariantUtil parses with getPartString (empty or absent = "NA") and
// getPartInt ((int) Double.parseDouble, unparsable or absent = -1).
var (
	svStringColumns = []string{
		"site1_ensembl_transcript_id", "site1_chromosome", "site1_contig", "site1_region", "site1_description",
		"site2_ensembl_transcript_id", "site2_chromosome", "site2_contig", "site2_region", "site2_description",
		"site2_effect_on_frame", "ncbi_build", "dna_support", "rna_support", "annotation", "breakpoint_type",
		"connection_type", "event_info", "class", "comments",
	}
	svIntColumns = []string{
		"site1_position", "site1_region_number", "site2_position", "site2_region_number",
		"normal_read_count", "tumor_read_count", "normal_variant_count", "tumor_variant_count",
		"normal_paired_end_read_count", "tumor_paired_end_read_count", "normal_split_read_count",
		"tumor_split_read_count", "sv_length",
	}
)

// javaIntSQL is TabDelimitedFileUtil.getPartInt as a String: the truncated
// number, or "-1" when the value does not parse.
func javaIntSQL(col string) string {
	f := fmt.Sprintf("toFloat64OrNull(trimBoth(%s))", col)
	return fmt.Sprintf("if(isNull(%[1]s) OR NOT isFinite(assumeNotNull(%[1]s)), '-1', toString(toInt64(trunc(assumeNotNull(%[1]s)))))", f)
}

// structuralVariantSQL builds the `structural_variant` view: the records
// ImportStructuralVariantData stores. A record needs a sample of the study, an
// SV_Status other than empty/NA and at least one site whose gene resolves
// (gene_key_map); an unresolved site keeps a NULL Entrez id and an empty
// symbol, resolved sites carry the reference gene. Text fields that are empty
// or absent read "NA", integer fields -1, and records repeating the importer's
// SV key (sample, both sites' gene, chromosome, position, region number and
// transcript, event info) are stored once.
func structuralVariantSQL(c *client, rawDB, targetDB string) string {
	svc, _ := c.TableColumns("structural_variant")
	// Absent gene columns are added as empty strings so the gene-key joins
	// always have a column to match.
	var missing []string
	for _, n := range []string{"site1_hugo_symbol", "site1_entrez_gene_id", "site2_hugo_symbol", "site2_entrez_gene_id", "sv_status"} {
		if !svc[n] {
			missing = append(missing, "'' AS "+n)
		}
	}
	src := fmt.Sprintf("`%s`.structural_variant", rawDB)
	if len(missing) > 0 {
		src = fmt.Sprintf("(SELECT *, %s FROM `%s`.structural_variant)", strings.Join(missing, ", "), rawDB)
	}
	str := func(n string) string {
		if svc[n] {
			return fmt.Sprintf("if(v.`%[1]s` = '', 'NA', v.`%[1]s`)", n)
		}
		return "'NA'"
	}
	opt := func(n string, value func(string) string) string {
		if svc[n] {
			return value("v.`" + n + "`")
		}
		return "''"
	}
	handled := map[string]bool{"study_id": true, "sample_id": true, "profile_stable_id": true,
		"site1_hugo_symbol": true, "site1_entrez_gene_id": true, "site2_hugo_symbol": true, "site2_entrez_gene_id": true,
		"sv_status": true, "cbp_driver": true, "cbp_driver_tiers": true, "cbp_driver_annotation": true,
		"cbp_driver_tiers_annotation": true, "source_file": true, "alteration_type": true, "datatype": true}
	sel := []string{
		"v.study_id AS study_id", "v.sample_id AS sample_id", "v.profile_stable_id AS profile_stable_id",
		"if(g1.resolved_entrez_gene_id != 0, g1.resolved_hugo_gene_symbol, '') AS site1_hugo_symbol",
		"if(g1.resolved_entrez_gene_id != 0, toNullable(g1.resolved_entrez_gene_id), NULL) AS site1_entrez_gene_id",
		"if(g2.resolved_entrez_gene_id != 0, g2.resolved_hugo_gene_symbol, '') AS site2_hugo_symbol",
		"if(g2.resolved_entrez_gene_id != 0, toNullable(g2.resolved_entrez_gene_id), NULL) AS site2_entrez_gene_id",
		"v.sv_status AS sv_status",
		opt("cbp_driver", driverValue) + " AS cbp_driver",
		opt("cbp_driver_tiers", driverValue) + " AS cbp_driver_tiers",
		opt("cbp_driver_annotation", func(s string) string { return s }) + " AS cbp_driver_annotation",
		opt("cbp_driver_tiers_annotation", func(s string) string { return s }) + " AS cbp_driver_tiers_annotation",
		namespaceColumnsExpr(c, rawDB, svc, "v.") + " AS namespace_columns",
	}
	for _, n := range svStringColumns {
		sel = append(sel, str(n)+" AS `"+n+"`")
		handled[n] = true
	}
	for _, n := range svIntColumns {
		v := "'-1'"
		if svc[n] {
			v = javaIntSQL("v.`" + n + "`")
		}
		sel = append(sel, v+" AS `"+n+"`")
		handled[n] = true
	}
	for _, n := range sortedColumnNames(svc) {
		if !handled[n] {
			sel = append(sel, "v.`"+n+"` AS `"+n+"`")
		}
	}
	gk := func(alias, site string) string {
		return fmt.Sprintf(
			" LEFT JOIN (SELECT * FROM `%[1]s`.gene_key_map WHERE source = 'structural_variant') %[2]s "+
				"ON %[2]s.study_id = v.study_id AND %[2]s.profile_stable_id = v.profile_stable_id "+
				"AND %[2]s.hugo_symbol = v.%[3]s_hugo_symbol AND %[2]s.entrez_gene_id = v.%[3]s_entrez_gene_id",
			targetDB, alias, site)
	}
	return fmt.Sprintf(
		"SELECT %s FROM %s v%s%s "+
			"WHERE v.sample_id NOT IN ('', 'NA') AND upper(v.sv_status) NOT IN ('', 'NA') "+
			"AND (g1.resolved_entrez_gene_id != 0 OR g2.resolved_entrez_gene_id != 0) "+
			"AND (v.study_id, v.sample_id) IN (SELECT study_id, sample_id FROM `%s`.clinical_sample) "+
			"LIMIT 1 BY study_id, profile_stable_id, sample_id, site1_entrez_gene_id, site1_chromosome, site1_position, "+
			"site1_region_number, site1_ensembl_transcript_id, site2_entrez_gene_id, site2_chromosome, site2_position, "+
			"site2_region_number, site2_ensembl_transcript_id, event_info",
		strings.Join(sel, ", "), src, gk("g1", "site1"), gk("g2", "site2"), rawDB)
}
