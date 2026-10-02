// Package transform builds the normalized cBioPortal model as ClickHouse views
// over the raw staging tables. Nothing is copied — the transform runs on read —
// except the small sample_profile membership table, materialized per run.
// The headline view is `mutation`: cBioPortal's default MutationFilter as a
// WHERE clause, with Hugo→Entrez resolution against the reference DB.
package transform

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

type ViewCount struct {
	Name string
	Rows uint64
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
	engineOf := func(name string) string {
		engine, _ := c.Execute(fmt.Sprintf(
			"SELECT engine FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TabSeparated", targetDB, name))
		return strings.TrimSpace(engine)
	}
	// Snapshots from a previous run are dropped so the views can be rebuilt.
	for _, m := range snapshotted {
		if e := engineOf(m.name); e != "" && e != "View" {
			if _, err := c.Execute(fmt.Sprintf("DROP TABLE `%s`.`%s`", targetDB, m.name)); err != nil {
				return nil, err
			}
		}
	}
	make := func(name, sel string) error {
		if _, err := c.Execute(fmt.Sprintf("CREATE OR REPLACE VIEW `%s`.`%s` AS %s", targetDB, name, sel)); err != nil {
			return fmt.Errorf("creating view %s: %w", name, err)
		}
		created = append(created, name)
		return nil
	}
	exists := func(t string) bool { ok, _ := c.TableExists(t); return ok }
	// materialize builds a table (atomically replaced) instead of a view, for
	// the derived tables the API reads on every request — as prod does.
	materialize := func(name, sel string) error {
		if engineOf(name) == "View" {
			if _, err := c.Execute(fmt.Sprintf("DROP VIEW `%s`.`%s`", targetDB, name)); err != nil {
				return err
			}
		}
		if _, err := c.Execute(fmt.Sprintf(
			"CREATE OR REPLACE TABLE `%s`.`%s` ENGINE = MergeTree ORDER BY tuple() AS %s", targetDB, name, sel)); err != nil {
			return fmt.Errorf("creating table %s: %w", name, err)
		}
		created = append(created, name)
		return nil
	}

	if exists("cancer_study") {
		if err := make("cancer_study", fmt.Sprintf(
			"SELECT study_id, name, short_name, description, type_of_cancer AS cancer_type_id, groups, pmid, citation, reference_genome, add_global_case_list = 'true' AS add_global_case_list FROM `%s`.cancer_study", rawDB)); err != nil {
			return nil, err
		}
	}
	// type_of_cancer holds the study-supplied cancer types (cbioportal-core
	// ImportTypesOfCancers: id and parent lower-cased, short name = the id as
	// given). It always exists so the API can union it with the reference
	// cancer types.
	tocSQL := "SELECT '' AS type_of_cancer_id, '' AS name, '' AS dedicated_color, '' AS short_name, '' AS parent WHERE 0"
	if exists("type_of_cancer") {
		tocSQL = fmt.Sprintf(
			"SELECT lower(trimBoth(type_of_cancer)) AS type_of_cancer_id, trimBoth(name) AS name, trimBoth(dedicated_color) AS dedicated_color, "+
				"trimBoth(type_of_cancer) AS short_name, lower(trimBoth(parent)) AS parent FROM `%s`.type_of_cancer "+
				"WHERE type_of_cancer_id != '' ORDER BY study_id LIMIT 1 BY type_of_cancer_id", rawDB)
	}
	if err := make("type_of_cancer", tocSQL); err != nil {
		return nil, err
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
			svDriver = driverValue("cbp_driver")
		}
		if svc["cbp_driver_tiers"] {
			svTiers = driverValue("cbp_driver_tiers")
		}
		// Missing entrez ids are resolved from the gene symbol, as the importer does.
		entrez := func(site string) string {
			if !hasRef {
				return fmt.Sprintf("toInt64OrNull(v.%s_entrez_gene_id)", site)
			}
			return fmt.Sprintf("if(toInt64OrNull(v.%[1]s_entrez_gene_id) > 0, toInt64OrNull(v.%[1]s_entrez_gene_id), "+
				"nullIf(toInt64(%[1]s_g.entrez_gene_id), 0))", site)
		}
		joins := ""
		if hasRef {
			joins = fmt.Sprintf(
				" LEFT JOIN `%[1]s`.gene site1_g ON v.site1_hugo_symbol != '' AND upper(v.site1_hugo_symbol) = upper(site1_g.hugo_gene_symbol)"+
					" LEFT JOIN `%[1]s`.gene site2_g ON v.site2_hugo_symbol != '' AND upper(v.site2_hugo_symbol) = upper(site2_g.hugo_gene_symbol)", refDB)
		}
		if err := make("structural_variant", fmt.Sprintf(
			"SELECT "+svPassThrough(svc)+"v.study_id AS study_id, v.sample_id AS sample_id, v.site1_hugo_symbol AS site1_hugo_symbol, %s AS site1_entrez_gene_id, "+
				"v.site2_hugo_symbol AS site2_hugo_symbol, %s AS site2_entrez_gene_id, "+
				"%s AS sv_status, %s AS cbp_driver, %s AS cbp_driver_tiers, %s AS namespace_columns FROM `%s`.structural_variant v%s",
			entrez("site1"), entrez("site2"),
			strings.ReplaceAll(svStatus, "sv_status", "v.sv_status"), strings.ReplaceAll(svDriver, "cbp_driver", "v.cbp_driver"),
			strings.ReplaceAll(svTiers, "cbp_driver_tiers", "v.cbp_driver_tiers"), namespaceColumnsExpr(c, rawDB, svc, "v."), rawDB, joins)); err != nil {
			return nil, err
		}
	}
	cnaMatrix, cnaLong := exists("cna_matrix"), exists("cna_discrete_long")
	if cnaMatrix || cnaLong {
		sql := cnaSQL(c, rawDB, cnaMatrix, cnaLong)
		if hasRef {
			// Blank entrez ids are resolved from the gene symbol, as the importer does.
			sql = fmt.Sprintf("SELECT u.* EXCEPT (entrez_gene_id), if(toInt64OrNull(u.entrez_gene_id) > 0, u.entrez_gene_id, "+
				"if(g.entrez_gene_id > 0, toString(g.entrez_gene_id), '')) AS entrez_gene_id FROM (%s) u "+
				"LEFT JOIN `%s`.gene g ON u.hugo_symbol != '' AND upper(u.hugo_symbol) = upper(g.hugo_gene_symbol)", sql, refDB)
		}
		if err := make("cna", sql); err != nil {
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

	// sample_profile is materialized rather than a view: it is small, read on
	// every study-view and alteration query, and expensive to derive (it scans
	// every profile's staging table), like prod's own sample_profile table.
	hasSampleProfile := false
	if sql, ok := sampleProfileSQL(c, rawDB); ok {
		engine, _ := c.Execute(fmt.Sprintf(
			"SELECT engine FROM system.tables WHERE database = '%s' AND name = 'sample_profile' FORMAT TabSeparated", targetDB))
		if strings.TrimSpace(engine) == "View" {
			if _, err := c.Execute(fmt.Sprintf("DROP VIEW `%s`.`sample_profile`", targetDB)); err != nil {
				return nil, err
			}
		}
		if _, err := c.Execute(fmt.Sprintf(
			"CREATE OR REPLACE TABLE `%s`.`sample_profile` ENGINE = MergeTree ORDER BY (study_id, profile_stable_id, sample_id) AS %s",
			targetDB, sql)); err != nil {
			return nil, fmt.Errorf("creating table sample_profile: %w", err)
		}
		created = append(created, "sample_profile")
		hasSampleProfile = true
	}
	if hasSampleProfile && exists("clinical_sample") && exists("clinical_attribute_meta") {
		if err := computedClinicalAttributes(c, rawDB, targetDB, exists("mutation_extended"), exists("cna_seg")); err != nil {
			return nil, err
		}
	}

	// Prod-aligned column-store `*_derived` views (built from our staging with
	// prod's exact column names), created after the base views they read.
	type derived struct {
		name  string
		guard bool
		sql   string
	}
	// Ordered so every table is built after the tables it reads.
	for _, d := range []derived{
		{"gene_panel_to_gene_derived", exists("gene_panel"), genePanelToGeneDerivedSQL(rawDB, refDB)},
		{"sample_to_gene_panel_derived", hasSampleProfile, sampleToGenePanelDerivedSQL(targetDB)},
		{"sample_derived", exists("clinical_sample"), sampleDerivedSQL(rawDB, refDB, targetDB)},
		{"clinical_data_derived", exists("clinical_sample") || exists("clinical_patient"), clinicalDataDerivedSQL(rawDB, refDB, targetDB)},
		{"genomic_event_derived", exists("mutation_extended") && exists("cna_discrete_long") && exists("structural_variant") && hasSampleProfile, genomicEventDerivedSQL(rawDB, refDB, targetDB)},
		{"genetic_alteration_derived", exists("cna_discrete_long") && exists("cna_matrix") && exists("methylation_matrix") && exists("mrna_expression_matrix"), geneticAlterationDerivedSQL(rawDB, refDB)},
		{"generic_assay_data_derived", exists("generic_assay_matrix"), genericAssayDataDerivedSQL(rawDB, refDB, targetDB)},
		{"clinical_event_derived", exists("timeline"), clinicalEventDerivedSQL(rawDB, refDB)},
		{"clinical_event_data_derived", exists("timeline"), clinicalEventDataDerivedSQL(rawDB, refDB)},
	} {
		if !d.guard {
			continue
		}
		if err := materialize(d.name, d.sql); err != nil {
			return nil, err
		}
	}

	for _, v := range studyExtraViews(c, rawDB, targetDB, refDB, hasRef, exists) {
		if err := make(v.name, v.sql); err != nil {
			return nil, err
		}
	}

	// The base views the API filters per request are snapshotted into tables
	// once every view and derived table reading them has been built; views
	// keep resolving them by name.
	for _, m := range snapshotted {
		if engineOf(m.name) != "View" {
			continue
		}
		if err := snapshotView(c, targetDB, m.name, m.key); err != nil {
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

// snapshotted lists the views Apply turns into tables, with their sort keys.
var snapshotted = []struct{ name, key string }{
	{"clinical_attribute_meta", "(study_id, clinical_attribute_id)"},
	{"clinical_sample", "(study_id, sample_id, attr_id)"},
	{"clinical_patient", "(study_id, patient_id, attr_id)"},
	{"mutation", "(study_id, molecular_profile_id, sample_id)"},
	{"cna", "(study_id, profile_stable_id, sample_id)"},
	{"structural_variant", "(study_id, profile_stable_id, sample_id)"},
}

// snapshotView replaces a view with a MergeTree table holding its rows.
func snapshotView(c *ch.Client, db, name, orderBy string) error {
	tmp := "__snapshot_" + name
	stmts := []string{
		fmt.Sprintf("CREATE OR REPLACE TABLE `%[1]s`.`%[2]s` ENGINE = MergeTree ORDER BY %[3]s AS SELECT * FROM `%[1]s`.`%[4]s`", db, tmp, orderBy, name),
		fmt.Sprintf("DROP VIEW `%s`.`%s`", db, name),
		fmt.Sprintf("RENAME TABLE `%[1]s`.`%[2]s` TO `%[1]s`.`%[3]s`", db, tmp, name),
	}
	for _, q := range stmts {
		if _, err := c.Execute(q); err != nil {
			return fmt.Errorf("materializing %s: %w", name, err)
		}
	}
	return nil
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
		// ASCN columns populate Mutation.alleleSpecificCopyNumber instead.
		if strings.EqualFold(name, "ASCN") {
			continue
		}
		prefix := strings.ToLower(name) + "_"
		var subCols, nonEmpty []string
		for col := range has {
			if strings.HasPrefix(col, prefix) && len(col) > len(prefix) {
				sub := col[len(prefix):]
				// Numeric values are emitted as JSON numbers, as the importer's
				// namespace parser stores them.
				v := tablePrefix + col
				subCols = append(subCols, fmt.Sprintf(
					`'"%s":', if(match(%s, '^-?[0-9]+(\\.[0-9]+)?([eE][-+]?[0-9]+)?$'), %s, toJSONString(%s))`, sub, v, v, v))
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
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
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
			return "if(lower(trimBoth(m." + name + ")) IN ('', 'na'), '', m." + name + ") AS " + name
		}
		return "'' AS " + name
	}
	// col projects an optional staging column, or def when the MAF lacks it.
	col := func(name, alias, def string) string {
		if has[name] {
			return "m." + name + " AS " + alias
		}
		return def + " AS " + alias
	}
	extras := strings.Join([]string{
		"concat(m.study_id, '_', m.profile_stable_id) AS molecular_profile_id",
		"ifNull(sp.patient_id, '') AS patient_id",
		col("center", "center", "''"),
		col("n_alt_count", "n_alt_count", "''"),
		col("n_ref_count", "n_ref_count", "''"),
		col("amino_acid_change", "amino_acid_change", "''"),
		col("ncbi_build", "ncbi_build", "''"),
		col("refseq", "refseq_mrna_id", "''"),
		col("protein_position", "protein_position", "''"),
		col("ascn_ascn_integer_copy_number", "ascn_integer_copy_number", "''"),
		col("ascn_ascn_method", "ascn_method", "''"),
		col("ascn_ccf_expected_copies_upper", "ascn_ccf_expected_copies_upper", "''"),
		col("ascn_ccf_expected_copies", "ascn_ccf_expected_copies", "''"),
		col("ascn_clonal", "ascn_clonal", "''"),
		col("ascn_minor_copy_number", "ascn_minor_copy_number", "''"),
		col("ascn_expected_alt_copies", "ascn_expected_alt_copies", "''"),
		col("ascn_total_copy_number", "ascn_total_copy_number", "''"),
	}, ", ")
	drivers := driverCol("cbp_driver") + ", " + driverCol("cbp_driver_annotation") + ", " +
		driverCol("cbp_driver_tiers") + ", " + driverCol("cbp_driver_tiers_annotation")
	nsCol := namespaceColumnsExpr(c, rawDB, has, "m.")
	return fmt.Sprintf(
		"SELECT m.study_id AS study_id, m.tumor_sample_barcode AS sample_id, m.hugo_symbol AS hugo_symbol, "+
			"%s AS entrez_gene_id, m.chromosome AS chromosome, m.start_position AS start_position, "+
			"m.end_position AS end_position, m.reference_allele AS reference_allele, m.tumor_seq_allele2 AS tumor_seq_allele2, "+
			"m.variant_classification AS variant_classification, m.variant_type AS variant_type, m.mutation_status AS mutation_status, "+
			"m.validation_status AS validation_status, "+
			proteinChangeSQL(has)+" AS protein_change, "+
			"m.t_alt_count AS t_alt_count, m.t_ref_count AS t_ref_count, "+drivers+", "+nsCol+" AS namespace_columns, "+extras+" "+
			"FROM `%s`.mutation_extended m%s "+
			"LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM `"+rawDB+"`.clinical_sample) sp "+
			"ON sp.study_id = m.study_id AND sp.sample_id = m.tumor_sample_barcode "+
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
			"SELECT study_id, sample_id, hugo_symbol, %s AS entrez_gene_id, value AS alteration, profile_stable_id, '' AS cbp_driver, '' AS cbp_driver_tiers, '' AS cbp_driver_annotation, '' AS cbp_driver_tiers_annotation, '{}' AS namespace_columns FROM `%s`.cna_matrix WHERE datatype = 'DISCRETE' AND value IN ('-2', '-1', '1', '2')", entrez, rawDB))
	}
	if fromLong {
		lcols, _ := c.TableColumns("cna_discrete_long")
		opt := func(name string) string {
			if lcols[name] {
				return driverValue(name)
			}
			return "''"
		}
		ns := namespaceColumnsExpr(c, rawDB, lcols, "")
		parts = append(parts, fmt.Sprintf(
			"SELECT study_id, sample_id, hugo_symbol, entrez_gene_id, value AS alteration, profile_stable_id, %s AS cbp_driver, %s AS cbp_driver_tiers, "+
				"%s AS cbp_driver_annotation, %s AS cbp_driver_tiers_annotation, %s AS namespace_columns FROM `%s`.cna_discrete_long WHERE value IN ('-2', '-1', '1', '2')",
			opt("cbp_driver"), opt("cbp_driver_tiers"), opt("cbp_driver_annotation"), opt("cbp_driver_tiers_annotation"), ns, rawDB))
	}
	return strings.Join(parts, " UNION ALL ")
}

// missingClinicalValue returns a SQL predicate that is true when a clinical
// value is one cBioPortal's clinical importer never stores
// (ImportClinicalData.MissingAttributeValues.has): blank after trimming, or —
// after deleting '[', '|' and ']' and turning spaces into underscores —
// case-insensitively one of NOT_APPLICABLE, NOT_AVAILABLE, PENDING,
// DISCREPANCY, COMPLETED, NULL, MISSING, NA. "[Unknown]" is kept.
func missingClinicalValue(col string) string {
	return fmt.Sprintf(
		"(replaceRegexpAll(%[1]s, '^[\\\\x00-\\\\x20]+|[\\\\x00-\\\\x20]+$', '') = '' OR "+
			"upper(replaceAll(replaceRegexpAll(%[1]s, '[\\\\[|\\\\]]', ''), ' ', '_')) IN "+
			"('NOT_APPLICABLE', 'NOT_AVAILABLE', 'PENDING', 'DISCREPANCY', 'COMPLETED', 'NULL', 'MISSING', 'NA'))",
		col)
}

func clinicalLong(c *ch.Client, rawDB, targetDB, srcTable, idCol string) error {
	sel, err := clinicalLongSQL(c, rawDB, srcTable, idCol)
	if err != nil {
		return err
	}
	_, err = c.Execute(fmt.Sprintf("CREATE OR REPLACE VIEW `%s`.`%s` AS %s", targetDB, srcTable, sel))
	return err
}

// clinicalLongSQL unpivots a wide clinical staging table into
// (study_id, <id>, attr_id, attr_value) rows, dropping missing values.
func clinicalLongSQL(c *ch.Client, rawDB, srcTable, idCol string) (string, error) {
	cols, err := c.TableColumns(srcTable)
	if err != nil {
		return "", err
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
			"SELECT study_id, `%s` AS %s, kv.1 AS attr_id, kv.2 AS attr_value FROM `%s`.`%s` ARRAY JOIN [%s] AS kv WHERE NOT %s",
			idCol, idCol, rawDB, srcTable, strings.Join(pairs, ", "), missingClinicalValue("kv.2"))
	}
	return sel, nil
}

// proteinChangeSQL follows ExtendedMutationUtil.getProteinChange: the first
// valid of HGVSp_Short and Amino_Acid_Change (else "MUTATED"), without a
// leading "p."; whitelisted 5'Flank (TERT promoter) variants read "Promoter".
func proteinChangeSQL(has map[string]bool) string {
	valid := func(c string) string {
		return fmt.Sprintf("(%[1]s != '' AND upper(%[1]s) NOT IN ('NULL', 'NA', '[NOT AVAILABLE]'))", c)
	}
	cands := []string{}
	for _, c := range []string{"hgvsp_short", "amino_acid_change"} {
		if has[c] {
			cands = append(cands, "m."+c)
		}
	}
	expr := "'MUTATED'"
	for _, cand := range slices.Backward(cands) {
		expr = fmt.Sprintf("if(%s, %s, %s)", valid(cand), cand, expr)
	}
	return fmt.Sprintf("if(lower(m.variant_classification) = '5''flank', 'Promoter', "+
		"if(startsWith(%[1]s, 'p.'), substring(%[1]s, 3), %[1]s))", expr)
}

// driverValue keeps a custom-driver value as given unless it is empty or NA,
// which the importer does not store.
func driverValue(col string) string {
	return fmt.Sprintf("if(lower(trimBoth(%[1]s)) IN ('', 'na'), '', %[1]s)", col)
}

// svPassThrough lists the staged structural-variant detail columns the view
// carries unchanged (positions, regions, read counts, annotations, ...).
func svPassThrough(cols map[string]bool) string {
	derived := map[string]bool{"study_id": true, "sample_id": true, "site1_hugo_symbol": true, "site1_entrez_gene_id": true,
		"site2_hugo_symbol": true, "site2_entrez_gene_id": true, "sv_status": true, "cbp_driver": true, "cbp_driver_tiers": true,
		"source_file": true, "alteration_type": true, "datatype": true}
	var out []string
	for _, c := range sortedColumnNames(cols) {
		if !derived[c] {
			out = append(out, "v.`"+c+"` AS `"+c+"`, ")
		}
	}
	return strings.Join(out, "")
}

func sortedColumnNames(cols map[string]bool) []string {
	out := make([]string, 0, len(cols))
	for c := range cols {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
