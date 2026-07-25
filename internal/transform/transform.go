// Package transform builds the normalized cBioPortal model as ClickHouse views
// over the raw staging tables. Nothing is copied — the transform runs on read.
// The headline view is `mutation`: cBioPortal's default MutationFilter as a
// WHERE clause, with Hugo→Entrez resolution against the reference DB.
package transform

import (
	"fmt"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

type ViewCount struct {
	Name  string
	Rows  uint64
}

// Apply builds every applicable view in targetDB reading from rawDB, resolving
// gene symbols against refDB when present.
func Apply(c *ch.Client, rawDB, targetDB, refDB string) ([]ViewCount, error) {
	if _, err := c.ExecuteServerwide(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", targetDB)); err != nil {
		return nil, err
	}
	hasRefText, _ := c.Execute(fmt.Sprintf(
		"SELECT count() FROM system.tables WHERE database = '%s' AND name = 'gene' FORMAT TabSeparated", refDB))
	hasRef := strings.TrimSpace(hasRefText) != "0" && strings.TrimSpace(hasRefText) != ""

	var created []string
	make := func(name, sel string) error {
		if _, err := c.Execute(fmt.Sprintf("CREATE OR REPLACE VIEW `%s`.`%s` AS %s", targetDB, name, sel)); err != nil {
			return fmt.Errorf("creating view %s: %w", name, err)
		}
		created = append(created, name)
		return nil
	}
	exists := func(t string) bool { ok, _ := c.TableExists(t); return ok }

	if exists("cancer_study") {
		if err := make("cancer_study", fmt.Sprintf(
			"SELECT study_id, name, short_name, description, type_of_cancer AS cancer_type_id, groups, pmid, citation, reference_genome, add_global_case_list = 'true' AS add_global_case_list FROM `%s`.cancer_study", rawDB)); err != nil {
			return nil, err
		}
	}
	if exists("type_of_cancer") {
		if err := make("type_of_cancer", fmt.Sprintf(
			"SELECT DISTINCT type_of_cancer AS type_of_cancer_id, name, dedicated_color, parent FROM `%s`.type_of_cancer", rawDB)); err != nil {
			return nil, err
		}
	}
	if exists("clinical_attribute_meta") {
		if err := make("clinical_attribute_meta", fmt.Sprintf(
			"SELECT study_id, attr_id AS clinical_attribute_id, display_name, description, datatype, priority, patient_attribute FROM `%s`.clinical_attribute_meta", rawDB)); err != nil {
			return nil, err
		}
	}
	if exists("genetic_profile") {
		if err := make("molecular_profile", fmt.Sprintf(
			"SELECT molecular_profile_id, study_id, alteration_type AS molecular_alteration_type, datatype, name, description, show_in_analysis = 'true' AS show_profile_in_analysis_tab, profile_stable_id, generic_assay_type FROM `%s`.genetic_profile", rawDB)); err != nil {
			return nil, err
		}
	}
	// sample_list = the study's case lists plus the auto-generated
	// "<studyId>_all" list (every sample), which cBioPortal synthesizes for
	// studies with add_global_case_list.
	if exists("sample_list") || exists("clinical_sample") {
		parts := []string{}
		if exists("sample_list") {
			parts = append(parts, fmt.Sprintf(
				"SELECT study_id, list_id, category, name, description, sample_id FROM `%s`.sample_list", rawDB))
		}
		if exists("clinical_sample") {
			parts = append(parts, fmt.Sprintf(
				"SELECT study_id, concat(study_id, '_all') AS list_id, 'all_cases_in_study' AS category, "+
					"'All samples' AS name, 'All samples' AS description, sample_id "+
					"FROM (SELECT DISTINCT study_id, sample_id FROM `%s`.clinical_sample WHERE sample_id != '')", rawDB))
		}
		if err := make("sample_list", strings.Join(parts, " UNION ALL ")); err != nil {
			return nil, err
		}
	}
	if exists("gene_panel") {
		entrez, join := "''", ""
		if hasRef {
			entrez = "if(g.entrez_gene_id > 0, toString(g.entrez_gene_id), '')"
			join = fmt.Sprintf(" LEFT JOIN `%s`.gene g ON upper(p.hugo_symbol) = upper(g.hugo_gene_symbol)", refDB)
		}
		if err := make("gene_panel", fmt.Sprintf(
			"SELECT p.study_id AS study_id, p.gene_panel_id AS gene_panel_id, p.description AS description, p.hugo_symbol AS hugo_gene_symbol, %s AS entrez_gene_id FROM `%s`.gene_panel p%s", entrez, rawDB, join)); err != nil {
			return nil, err
		}
	}
	if exists("clinical_patient") {
		if err := make("patient", fmt.Sprintf(
			"SELECT DISTINCT study_id, patient_id FROM `%s`.clinical_patient WHERE patient_id != ''", rawDB)); err != nil {
			return nil, err
		}
	}
	if exists("clinical_sample") {
		if err := make("sample", fmt.Sprintf(
			"SELECT DISTINCT study_id, sample_id, patient_id FROM `%s`.clinical_sample WHERE sample_id != ''", rawDB)); err != nil {
			return nil, err
		}
	}
	if exists("mutation_extended") {
		if err := make("mutation", mutationSQL(c, rawDB, refDB, hasRef)); err != nil {
			return nil, err
		}
	}
	if exists("structural_variant") {
		svc, _ := c.TableColumns("structural_variant")
		svStatus, svDriver, svTiers := "''", "''", "''"
		if svc["sv_status"] {
			svStatus = "sv_status"
		}
		if svc["cbp_driver"] {
			svDriver = "ifNull(nullIf(nullIf(cbp_driver, 'NA'), 'Unknown'), '')"
		}
		if svc["cbp_driver_tiers"] {
			svTiers = "ifNull(nullIf(cbp_driver_tiers, 'NA'), '')"
		}
		if err := make("structural_variant", fmt.Sprintf(
			"SELECT study_id, sample_id, site1_hugo_symbol, toInt64OrNull(site1_entrez_gene_id) AS site1_entrez_gene_id, site2_hugo_symbol, toInt64OrNull(site2_entrez_gene_id) AS site2_entrez_gene_id, "+
				"%s AS sv_status, %s AS cbp_driver, %s AS cbp_driver_tiers FROM `%s`.structural_variant", svStatus, svDriver, svTiers, rawDB)); err != nil {
			return nil, err
		}
	}
	cnaMatrix, cnaLong := exists("cna_matrix"), exists("cna_discrete_long")
	if cnaMatrix || cnaLong {
		if err := make("cna", cnaSQL(c, rawDB, cnaMatrix, cnaLong)); err != nil {
			return nil, err
		}
	}
	if exists("cna_seg") {
		if err := make("seg", fmt.Sprintf(
			"SELECT study_id, id AS sample_id, chrom, loc_start AS start, loc_end AS end, num_mark AS num_probes, seg_mean AS segment_mean FROM `%s`.cna_seg", rawDB)); err != nil {
			return nil, err
		}
	}
	if exists("clinical_sample") {
		if err := clinicalLong(c, rawDB, targetDB, "clinical_sample", "sample_id"); err != nil {
			return nil, err
		}
		created = append(created, "clinical_sample")
	}
	if exists("clinical_patient") {
		if err := clinicalLong(c, rawDB, targetDB, "clinical_patient", "patient_id"); err != nil {
			return nil, err
		}
		created = append(created, "clinical_patient")
	}

	// Prod-aligned column-store `*_derived` views (built from our staging with
	// prod's exact column names), created after the base views they read.
	type derived struct {
		name  string
		guard bool
		sql   string
	}
	for _, d := range []derived{
		{"sample_derived", exists("clinical_sample"), sampleDerivedSQL(rawDB, refDB)},
		{"clinical_data_derived", exists("clinical_sample") || exists("clinical_patient"), clinicalDataDerivedSQL(rawDB, refDB)},
		{"genomic_event_derived", exists("mutation_extended") && exists("cna_discrete_long") && exists("structural_variant"), genomicEventDerivedSQL(rawDB, refDB)},
		{"genetic_alteration_derived", exists("cna_discrete_long") && exists("cna_matrix") && exists("methylation_matrix") && exists("mrna_expression_matrix"), geneticAlterationDerivedSQL(rawDB, refDB)},
		{"generic_assay_data_derived", exists("generic_assay_matrix"), genericAssayDataDerivedSQL(rawDB, refDB)},
		{"sample_to_gene_panel_derived", exists("gene_panel_matrix"), sampleToGenePanelDerivedSQL(rawDB, refDB)},
		{"gene_panel_to_gene_derived", exists("gene_panel"), genePanelToGeneDerivedSQL(rawDB, refDB)},
		{"clinical_event_derived", exists("timeline"), clinicalEventDerivedSQL(rawDB, refDB)},
		{"clinical_event_data_derived", exists("timeline"), clinicalEventDataDerivedSQL(rawDB, refDB)},
	} {
		if !d.guard {
			continue
		}
		if err := make(d.name, d.sql); err != nil {
			return nil, err
		}
	}

	var counts []ViewCount
	for _, v := range created {
		text, _ := c.Execute(fmt.Sprintf("SELECT count() FROM `%s`.`%s` FORMAT TabSeparated", targetDB, v))
		var n uint64
		fmt.Sscan(strings.TrimSpace(text), &n)
		counts = append(counts, ViewCount{Name: v, Rows: n})
	}
	return counts, nil
}

// namespaceColumnsExpr builds a ClickHouse expression that assembles the nested
// `namespaceColumns` JSON object ({"<Namespace>":{"<sub>":<value>}}) the mutation
// tables read. Namespace names come from the `namespaces` meta field (stored on
// genetic_profile); for each, the matching staging columns are `<lower(name)>_<sub>`.
// Each namespace object is emitted per-row only when at least one of its columns
// is non-empty, so an empty namespace declared by one study does not leak an
// empty column onto another study's rows. tablePrefix is the table alias (e.g.
// "m."). Returns "'{}'" when no namespace columns are present.
func namespaceColumnsExpr(c *ch.Client, rawDB string, has map[string]bool, tablePrefix string) string {
	names := distinctNamespaces(c, rawDB)
	var frags []string
	for _, name := range names {
		prefix := strings.ToLower(name) + "_"
		var subCols, nonEmpty []string
		for col := range has {
			if strings.HasPrefix(col, prefix) && len(col) > len(prefix) {
				sub := col[len(prefix):]
				subCols = append(subCols, fmt.Sprintf(`'"%s":', toJSONString(%s%s)`, sub, tablePrefix, col))
				nonEmpty = append(nonEmpty, fmt.Sprintf("%s%s != ''", tablePrefix, col))
			}
		}
		if len(subCols) == 0 {
			continue
		}
		// concat('"Name":{', '"sub":', toJSONString(col), ',', ..., '}')
		inner := strings.Join(subCols, ", ',', ")
		obj := fmt.Sprintf(`concat('"%s":{', %s, '}')`, name, inner)
		frags = append(frags, fmt.Sprintf("if(%s, %s, '')", strings.Join(nonEmpty, " OR "), obj))
	}
	if len(frags) == 0 {
		return "'{}'"
	}
	return fmt.Sprintf("concat('{', arrayStringConcat(arrayFilter(x -> x != '', [%s]), ','), '}')",
		strings.Join(frags, ", "))
}

// distinctNamespaces returns the distinct namespace names declared across all
// molecular profiles (the comma-separated `namespaces` meta field).
func distinctNamespaces(c *ch.Client, rawDB string) []string {
	if !hasColumn(c, rawDB, "genetic_profile", "namespaces") {
		return nil
	}
	text, _ := c.Execute(fmt.Sprintf(
		"SELECT DISTINCT arrayJoin(splitByChar(',', namespaces)) AS n FROM `%s`.genetic_profile WHERE namespaces != '' ORDER BY n FORMAT TabSeparated", rawDB))
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		n := strings.TrimSpace(line)
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// hasColumn reports whether a staging table has a column (schema evolution safe).
func hasColumn(c *ch.Client, rawDB, table, col string) bool {
	text, _ := c.Execute(fmt.Sprintf(
		"SELECT count() FROM system.columns WHERE database = '%s' AND table = '%s' AND name = '%s' FORMAT TabSeparated", rawDB, table, col))
	return strings.TrimSpace(text) == "1"
}

func mutationSQL(c *ch.Client, rawDB, refDB string, hasRef bool) string {
	entrez, join := "m.entrez_gene_id", ""
	if hasRef {
		entrez = "if(toInt64OrNull(m.entrez_gene_id) > 0, m.entrez_gene_id, if(g.entrez_gene_id > 0, toString(g.entrez_gene_id), if(ga.e > 0, toString(ga.e), '')))"
		join = fmt.Sprintf(
			" LEFT JOIN `%s`.gene g ON upper(m.hugo_symbol) = upper(g.hugo_gene_symbol)"+
				" LEFT JOIN (SELECT upper(gene_alias) AS a, any(entrez_gene_id) AS e FROM `%s`.gene_alias GROUP BY a HAVING uniqExact(entrez_gene_id) = 1) ga ON upper(m.hugo_symbol) = ga.a",
			refDB, refDB)
	}
	// Custom driver annotations (cbp_driver*): projected as normalized columns so
	// the API can populate Mutation.driverFilter/driverTiersFilter, which the
	// frontend reads directly to enable the custom-driver annotation controls.
	// Emit the columns unconditionally (empty when the staging table lacks them)
	// so the view shape is stable across datasets.
	has, _ := c.TableColumns("mutation_extended")
	driverCol := func(name string) string {
		if has[name] {
			return "ifNull(nullIf(nullIf(m." + name + ", 'NA'), 'Unknown'), '') AS " + name
		}
		return "'' AS " + name
	}
	drivers := driverCol("cbp_driver") + ", " + driverCol("cbp_driver_annotation") + ", " +
		driverCol("cbp_driver_tiers") + ", " + driverCol("cbp_driver_tiers_annotation")
	nsCol := namespaceColumnsExpr(c, rawDB, has, "m.")
	return fmt.Sprintf(
		"SELECT m.study_id AS study_id, m.tumor_sample_barcode AS sample_id, m.hugo_symbol AS hugo_symbol, "+
			"%s AS entrez_gene_id, m.chromosome AS chromosome, m.start_position AS start_position, "+
			"m.end_position AS end_position, m.reference_allele AS reference_allele, m.tumor_seq_allele2 AS tumor_seq_allele2, "+
			"m.variant_classification AS variant_classification, m.variant_type AS variant_type, m.mutation_status AS mutation_status, "+
			"m.validation_status AS validation_status, "+
			"if(startsWith(m.hgvsp_short, 'p.'), substring(m.hgvsp_short, 3), m.hgvsp_short) AS protein_change, "+
			"m.t_alt_count AS t_alt_count, m.t_ref_count AS t_ref_count, "+drivers+", "+nsCol+" AS namespace_columns "+
			"FROM `%s`.mutation_extended m%s "+
			"WHERE NOT startsWith(lower(m.mutation_status), 'none') AND NOT startsWith(lower(m.mutation_status), 'loh') "+
			"AND NOT startsWith(lower(m.mutation_status), 'wildtype') AND NOT startsWith(lower(m.validation_status), 'redacted') "+
			"AND lower(m.variant_classification) NOT IN ('silent', 'intron', '3''utr', '5''utr', '3''flank', 'igr', 'rna') "+
			"AND ( lower(m.variant_classification) != '5''flank' OR m.entrez_gene_id = '7015' OR upper(m.hugo_symbol) = 'TERT' )",
		entrez, rawDB, join)
}

func cnaSQL(c *ch.Client, rawDB string, fromMatrix, fromLong bool) string {
	var parts []string
	if fromMatrix {
		cols, _ := c.TableColumns("cna_matrix")
		entrez := "''"
		if cols["entrez_gene_id"] {
			entrez = "entrez_gene_id"
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT study_id, sample_id, hugo_symbol, %s AS entrez_gene_id, value AS alteration, profile_stable_id, '' AS cbp_driver, '' AS cbp_driver_tiers, '{}' AS namespace_columns FROM `%s`.cna_matrix WHERE datatype = 'DISCRETE' AND value IN ('-2', '-1', '1', '2')", entrez, rawDB))
	}
	if fromLong {
		lcols, _ := c.TableColumns("cna_discrete_long")
		driver, tiers := "''", "''"
		if lcols["cbp_driver"] {
			driver = "ifNull(nullIf(nullIf(cbp_driver, 'NA'), 'Unknown'), '')"
		}
		if lcols["cbp_driver_tiers"] {
			tiers = "ifNull(nullIf(cbp_driver_tiers, 'NA'), '')"
		}
		ns := namespaceColumnsExpr(c, rawDB, lcols, "")
		parts = append(parts, fmt.Sprintf(
			"SELECT study_id, sample_id, hugo_symbol, entrez_gene_id, value AS alteration, profile_stable_id, %s AS cbp_driver, %s AS cbp_driver_tiers, %s AS namespace_columns FROM `%s`.cna_discrete_long WHERE value IN ('-2', '-1', '1', '2')", driver, tiers, ns, rawDB))
	}
	return strings.Join(parts, " UNION ALL ")
}

func clinicalLong(c *ch.Client, rawDB, targetDB, srcTable, idCol string) error {
	cols, err := c.TableColumns(srcTable)
	if err != nil {
		return err
	}
	skip := map[string]bool{"study_id": true, "source_file": true, "alteration_type": true,
		"datatype": true, "profile_stable_id": true, "patient_id": true, "sample_id": true}
	var names []string
	for n := range cols {
		if !skip[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var sel string
	if len(names) == 0 {
		sel = fmt.Sprintf("SELECT study_id, `%s` AS %s, '' AS attr_id, '' AS attr_value FROM `%s`.`%s` WHERE 1 = 0", idCol, idCol, rawDB, srcTable)
	} else {
		pairs := make([]string, len(names))
		for i, n := range names {
			pairs[i] = fmt.Sprintf("('%s', `%s`)", strings.ToUpper(n), n)
		}
		sel = fmt.Sprintf(
			"SELECT study_id, `%s` AS %s, kv.1 AS attr_id, kv.2 AS attr_value FROM `%s`.`%s` ARRAY JOIN [%s] AS kv WHERE kv.2 != ''",
			idCol, idCol, rawDB, srcTable, strings.Join(pairs, ", "))
	}
	_, err = c.Execute(fmt.Sprintf("CREATE OR REPLACE VIEW `%s`.`%s` AS %s", targetDB, srcTable, sel))
	return err
}
