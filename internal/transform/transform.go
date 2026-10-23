// Package transform builds the normalized cBioPortal model in a portal
// database from the raw staging tables, with SQL run in ClickHouse.
//
// Tables are built for what is expensive to derive or read on every request:
// the gene-symbol resolution (gene_key_map), the mutation filters and merged
// MAF rows (variant_classification_filter, mutation_merge), samples and
// patients with their internal ids, sample_profile, the computed clinical
// attributes (clinical_sample_computed) and the study-view `*_derived`
// tables. The rest of the model starts as views over staging; the views the
// API filters per request (mutation, cna, the clinical data and others, see
// snapshotted) are copied into tables once everything reading them is built.
// The `mutation` view applies cBioPortal's default MutationFilter and
// resolves Hugo symbols to Entrez ids against the reference database.
package transform

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/javaorder"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

type ViewCount struct {
	Name string
	Rows uint64
}

// Apply builds every view and table the API reads in targetDB from rawDB,
// resolving gene symbols against refDB when present. Objects whose staging
// data is absent are built empty, with the same columns (see stagingSources).
//
// The objects are built under staged names (stagePrefix) and published once
// all of them are complete, so a failed run leaves the portal database as it
// was, and the API keeps serving the previous objects while a run builds.
func Apply(base *ch.Client, rawDB, targetDB, refDB string) ([]ViewCount, error) {
	c := newClient(base, rawDB)
	if err := c.EnsureDatabase(targetDB); err != nil {
		return nil, err
	}
	if err := dropStaged(c, targetDB); err != nil {
		return nil, err
	}
	c.stage = newStaging(targetDB)
	counts, err := build(c, rawDB, targetDB, refDB)
	if err == nil && beforePublish != nil {
		err = beforePublish()
	}
	if err != nil {
		if dropErr := dropStaged(c, targetDB); dropErr != nil {
			return nil, fmt.Errorf("%w (and removing the staged objects: %v)", err, dropErr)
		}
		return nil, err
	}
	if err := publish(c, c.stage); err != nil {
		return nil, err
	}
	if err := dropStaged(c, targetDB); err != nil {
		return nil, err
	}
	return counts, nil
}

// beforePublish, when set (by tests), runs between the build and publishing.
var beforePublish func() error

// build creates every object of targetDB through c and returns their row
// counts.
func build(c *client, rawDB, targetDB, refDB string) ([]ViewCount, error) {
	hasRefText, _ := c.Execute(fmt.Sprintf(
		"SELECT count() FROM system.tables WHERE database = '%s' AND name = 'gene' FORMAT TabSeparated", refDB))
	hasRef := strings.TrimSpace(hasRefText) != "0" && strings.TrimSpace(hasRefText) != ""
	genomeText, _ := c.Execute(fmt.Sprintf(
		"SELECT count() FROM system.tables WHERE database = '%s' AND name IN ('reference_genome', 'reference_genome_gene') FORMAT TabSeparated", refDB))
	hasGenomeGenes := strings.TrimSpace(genomeText) == "2"

	if err := buildGeneKeyMap(c, rawDB, targetDB, refDB, hasRef); err != nil {
		return nil, err
	}
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE OR REPLACE TABLE `%s`.variant_classification_filter ENGINE = MergeTree ORDER BY (study_id, profile_stable_id) AS %s",
		targetDB, variantClassificationFilterSQL(c, rawDB))); err != nil {
		return nil, fmt.Errorf("creating table variant_classification_filter: %w", err)
	}
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE OR REPLACE TABLE `%s`.mutation_merge ENGINE = MergeTree ORDER BY (study_id, profile_stable_id, file_num, row_num) AS %s",
		targetDB, mutationMergeSQL(c, rawDB, targetDB))); err != nil {
		return nil, fmt.Errorf("creating table mutation_merge: %w", err)
	}

	var created []string
	engineOf := func(name string) string {
		if c.stage != nil {
			name = c.stage.name(name)
		}
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
	createView := func(name, sel string) error {
		if _, err := c.Execute(fmt.Sprintf("CREATE OR REPLACE VIEW `%s`.`%s` AS %s", targetDB, name, sel)); err != nil {
			return fmt.Errorf("creating view %s: %w", name, err)
		}
		created = append(created, name)
		return nil
	}
	exists := func(t string) bool { ok, _ := c.TableExists(t); return ok }
	// materialize builds a table sorted by orderBy (atomically replaced)
	// instead of a view, for what the API reads on every request, as prod
	// does.
	materialize := func(name, orderBy, sel string) error {
		if engineOf(name) == "View" {
			if _, err := c.Execute(fmt.Sprintf("DROP VIEW `%s`.`%s`", targetDB, name)); err != nil {
				return err
			}
		}
		if _, err := c.Execute(fmt.Sprintf(
			"CREATE OR REPLACE TABLE `%s`.`%s` ENGINE = MergeTree ORDER BY %s AS %s", targetDB, name, orderBy, sel)); err != nil {
			return fmt.Errorf("creating table %s: %w", name, err)
		}
		created = append(created, name)
		return nil
	}

	// status is cbioportal-core's DaoCancerStudy.Status ordinal: 1 (AVAILABLE)
	// once a study is loaded, unless staging marks it otherwise; internal_id
	// numbers the studies in import order.
	if err := createView("cancer_study", fmt.Sprintf(
		"SELECT c.study_id AS study_id, c.name AS name, c.short_name AS short_name, c.description AS description, "+
			"c.type_of_cancer AS cancer_type_id, c.groups AS groups, c.pmid AS pmid, c.citation AS citation, c.license AS license, "+
			"c.reference_genome AS reference_genome, c.add_global_case_list = 'true' AS add_global_case_list, "+
			"toInt64OrDefault(c.status, toInt64(1)) AS status, r.study_rank AS internal_id "+
			"FROM `%s`.cancer_study c LEFT JOIN (%s) r ON r.study_id = c.study_id", rawDB, studyRankSQL(rawDB))); err != nil {
		return nil, err
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
	if err := createView("type_of_cancer", tocSQL); err != nil {
		return nil, err
	}
	// Samples and patients carry the internal ids cbioportal-core assigns
	// them; the clinical tables and derived tables built below read them.
	for _, t := range []struct{ name, key, sql string }{
		{"sample", "(study_id, sample_id)", sampleSQL(rawDB)},
		{"patient", "(study_id, patient_id)", patientSQL(rawDB)},
	} {
		if err := materialize(t.name, t.key, t.sql); err != nil {
			return nil, err
		}
	}
	for _, v := range []view{
		{"clinical_attribute_meta", fmt.Sprintf(
			"SELECT study_id, attr_id AS clinical_attribute_id, display_name, description, datatype, priority, patient_attribute FROM `%s`.clinical_attribute_meta", rawDB)},
		{"molecular_profile", molecularProfileSQL(c, rawDB)},
		{"sample_list", sampleListSQL(c, rawDB)},
		{"gene_panel", genePanelSQL(rawDB, refDB, hasRef)},
		{"mutation", mutationSQL(c, rawDB, targetDB)},
		{"structural_variant", structuralVariantSQL(c, rawDB, targetDB)},
	} {
		if err := createView(v.name, v.sql); err != nil {
			return nil, err
		}
	}
	// Discrete CNA comes from the wide matrices and the long files; with
	// neither staged, the long-file stand-in yields the empty view.
	cnaMatrix, cnaLong := exists("cna_matrix"), exists("cna_discrete_long")
	if err := createView("cna", cnaSQL(c, rawDB, targetDB, cnaMatrix, cnaLong || !cnaMatrix)); err != nil {
		return nil, err
	}
	// seg_id numbers the segments as the importer stores them: in file order,
	// study by study.
	if err := createView("seg", fmt.Sprintf(
		"SELECT g.study_id AS study_id, g.id AS sample_id, g.chrom AS chrom, g.loc_start AS start, g.loc_end AS end, "+
			"g.num_mark AS num_probes, g.seg_mean AS segment_mean, "+
			"toUInt64(row_number() OVER (ORDER BY r.study_rank, g.pos)) AS seg_id "+
			"FROM (SELECT *, %s AS pos FROM `%s`.cna_seg) g LEFT JOIN (%s) r ON r.study_id = g.study_id",
		fileOrderSQL, rawDB, studyRankSQL(rawDB))); err != nil {
		return nil, err
	}
	if err := clinicalLong(c, rawDB, targetDB, "clinical_sample", "sample_id"); err != nil {
		return nil, err
	}
	created = append(created, "clinical_sample")
	if err := clinicalLong(c, rawDB, targetDB, "clinical_patient", "patient_id"); err != nil {
		return nil, err
	}
	created = append(created, "clinical_patient")

	// sample_profile is materialized rather than a view: it is small, read on
	// every study-view and alteration query, and expensive to derive (it scans
	// every profile's staging table), like prod's own sample_profile table.
	spSQL, hasSampleProfile := sampleProfileSQL(c, rawDB, targetDB)
	if err := materialize("sample_profile", "(study_id, profile_stable_id, sample_id)", spSQL); err != nil {
		return nil, err
	}
	if hasSampleProfile && exists("clinical_sample") && exists("clinical_attribute_meta") {
		if err := computedClinicalAttributes(c, rawDB, targetDB, exists("mutation_extended"), exists("cna_seg")); err != nil {
			return nil, err
		}
	}

	// Prod-aligned column-store `*_derived` views (built from our staging with
	// prod's exact column names), created after the base views they read.
	// Ordered so every table is built after the tables it reads.
	for _, d := range []view{
		{"gene_panel_to_gene_derived", genePanelToGeneDerivedSQL(targetDB, refDB)},
		{"sample_to_gene_panel_derived", sampleToGenePanelDerivedSQL(targetDB)},
		{"sample_derived", sampleDerivedSQL(rawDB, refDB, targetDB)},
		{"clinical_data_derived", clinicalDataDerivedSQL(rawDB, refDB, targetDB)},
		{"genomic_event_derived", genomicEventDerivedSQL(c, rawDB, refDB, targetDB, hasGenomeGenes)},
		{"genetic_alteration_derived", geneticAlterationDerivedSQL(rawDB, targetDB)},
		{"generic_assay_data_derived", genericAssayDataDerivedSQL(rawDB, refDB, targetDB,
			hasColumn(c, rawDB, "genetic_profile", "patient_level") && hasColumn(c, rawDB, "genetic_profile", "generic_assay_type"))},
		{"clinical_event_derived", clinicalEventDerivedSQL(rawDB, refDB)},
		{"clinical_event_data_derived", clinicalEventDataDerivedSQL(c, rawDB)},
	} {
		if err := materialize(d.name, "tuple()", d.sql); err != nil {
			return nil, err
		}
	}

	for _, v := range studyExtraViews(c, rawDB, targetDB, refDB, hasRef, exists) {
		if err := createView(v.name, v.sql); err != nil {
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
		if err := snapshotView(c, targetDB, m.name, m.key, m.idFrom); err != nil {
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

// snapshotted lists the views Apply turns into tables, with their sort keys
// and, for rows of a sample or patient, the table and id column their
// internal id (that of the sample or patient) comes from.
var snapshotted = []struct{ name, key, idFrom string }{
	{"clinical_attribute_meta", "(study_id, clinical_attribute_id)", ""},
	{"clinical_sample", "(study_id, sample_id, attr_id)", "sample.sample_id"},
	{"clinical_patient", "(study_id, patient_id, attr_id)", "patient.patient_id"},
	{"sample_list", "(study_id, list_id, sample_id)", "sample.sample_id"},
	{"mutation", "(study_id, molecular_profile_id, sample_id)", ""},
	{"cna", "(study_id, profile_stable_id, sample_id)", ""},
	{"structural_variant", "(study_id, profile_stable_id, sample_id)", ""},
	{"seg", "(study_id, sample_id)", ""},
}

// snapshotView replaces a view with a MergeTree table holding its rows, with
// the internal_id of idFrom ("table.id_column") added when set.
func snapshotView(c *client, db, name, orderBy, idFrom string) error {
	tmp := "__snapshot_" + name
	src := fmt.Sprintf("SELECT * FROM `%s`.`%s`", db, name)
	if table, col, ok := strings.Cut(idFrom, "."); ok {
		src = fmt.Sprintf("SELECT v.*, i.internal_id AS internal_id FROM `%[1]s`.`%[2]s` v "+
			"LEFT JOIN (SELECT study_id, %[4]s, min(internal_id) AS internal_id FROM `%[1]s`.`%[3]s` GROUP BY study_id, %[4]s) i "+
			"ON i.study_id = v.study_id AND i.%[4]s = v.%[4]s", db, name, table, col)
	}
	stmts := []string{
		fmt.Sprintf("CREATE OR REPLACE TABLE `%[1]s`.`%[2]s` ENGINE = MergeTree ORDER BY %[3]s AS %[4]s", db, tmp, orderBy, src),
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
func namespaceColumnsExpr(c *client, rawDB string, has map[string]bool, tablePrefix string) string {
	// The importer's namespace parser collects the namespaces, and each
	// namespace's columns, in HashMaps: the stored JSON has their order.
	names := javaorder.HashMapOrder(distinctNamespaces(c, rawDB))
	var frags []string
	for _, name := range names {
		// ASCN columns populate Mutation.alleleSpecificCopyNumber instead.
		if strings.EqualFold(name, "ASCN") {
			continue
		}
		prefix := strings.ToLower(name) + "_"
		var subs []string
		for _, col := range sortedColumnNames(has) {
			if strings.HasPrefix(col, prefix) && len(col) > len(prefix) {
				subs = append(subs, col[len(prefix):])
			}
		}
		var subCols []string
		for _, sub := range javaorder.HashMapOrder(subs) {
			// Numeric values are emitted as JSON numbers, as the importer's
			// namespace parser stores them; empty values are stored as
			// null, which the API leaves out.
			v := tablePrefix + prefix + sub
			subCols = append(subCols, fmt.Sprintf(
				`if(%[2]s = '', '', concat('"%[1]s":', if(match(%[2]s, '^-?[0-9]+(\\.[0-9]+)?([eE][-+]?[0-9]+)?$'), %[2]s, toJSONString(%[2]s))))`, sub, v))
		}
		if len(subCols) == 0 {
			continue
		}
		// Every record of a profile declaring the namespace carries it, empty
		// when the record has no value for any of its columns.
		obj := fmt.Sprintf(`concat('"%s":{', arrayStringConcat(arrayFilter(x -> x != '', [%s]), ','), '}')`,
			name, strings.Join(subCols, ", "))
		declares := fmt.Sprintf("(%[1]sstudy_id, %[1]sprofile_stable_id) IN (SELECT study_id, profile_stable_id FROM `%[2]s`.genetic_profile "+
			"WHERE has(arrayMap(x -> lower(trimBoth(x)), splitByChar(',', namespaces)), '%[3]s'))", tablePrefix, rawDB, strings.ToLower(name))
		frags = append(frags, fmt.Sprintf("if(%s, %s, '')", declares, obj))
	}
	if len(frags) == 0 {
		return "'{}'"
	}
	return fmt.Sprintf("concat('{', arrayStringConcat(arrayFilter(x -> x != '', [%s]), ','), '}')",
		strings.Join(frags, ", "))
}

// distinctNamespaces returns the distinct namespace names declared across all
// molecular profiles (the comma-separated `namespaces` meta field).
func distinctNamespaces(c *client, rawDB string) []string {
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
func hasColumn(c *client, rawDB, table, col string) bool {
	text, _ := c.Execute(fmt.Sprintf(
		"SELECT count() FROM system.columns WHERE database = '%s' AND table = '%s' AND name = '%s' FORMAT TabSeparated", rawDB, table, col))
	return strings.TrimSpace(text) == "1"
}

// mutationSQL builds the `mutation` view: the MAF records ImportExtendedMutationData
// stores — gene resolved through gene_key_map (unresolved records are skipped,
// hugo_symbol / entrez_gene_id are the reference gene's) — that pass the
// MutationFilter. Both MUTATION_EXTENDED and MUTATION_UNCALLED profiles are
// stored this way, each under its own molecular profile.
func mutationSQL(c *client, rawDB, targetDB string) string {
	return mutationTableSQL(c, rawDB, targetDB, "mutation_extended") + " UNION ALL " +
		mutationTableSQL(c, rawDB, targetDB, "mutation_uncalled")
}

// mutationTableSQL is mutationSQL over one MAF staging table.
func mutationTableSQL(c *client, rawDB, targetDB, table string) string {
	entrez := "toString(gk.resolved_entrez_gene_id)"
	join := geneKeyJoin(targetDB, table, "m", "hugo_symbol", "entrez_gene_id")
	// Custom driver annotations (cbp_driver*): projected as normalized columns so
	// the API can populate Mutation.driverFilter/driverTiersFilter, which the
	// frontend reads directly to enable the custom-driver annotation controls.
	// Emit the columns unconditionally (empty when the staging table lacks them)
	// so the view shape is stable across datasets.
	has, _ := c.TableColumns(table)
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
	// A merged record's centers are those of every record merged into it;
	// an empty center, like an empty status, reads "NA" (getPartString).
	center := "'NA'"
	if has["center"] {
		center = mafPartSQL("m.center")
	}
	extras := strings.Join([]string{
		"concat(m.study_id, '_', m.profile_stable_id) AS molecular_profile_id",
		"ifNull(sp.patient_id, '') AS patient_id",
		"if(mm.keep = 1, mm.center, " + center + ") AS center",
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
		"SELECT m.study_id AS study_id, m.tumor_sample_barcode AS sample_id, gk.resolved_hugo_gene_symbol AS hugo_symbol, "+
			"%s AS entrez_gene_id, "+normalizedChrSQL("m.chromosome")+" AS chromosome, "+mafPositionSQL("m.start_position")+" AS start_position, "+
			mafPositionSQL("m.end_position")+" AS end_position, "+mafPartSQL("m.reference_allele")+" AS reference_allele, "+
			tumorSeqAlleleSQL("m")+" AS tumor_seq_allele2, "+
			"m.variant_classification AS variant_classification, m.variant_type AS variant_type, "+mafPartSQL("m.mutation_status")+" AS mutation_status, "+
			mafPartSQL("m.validation_status")+" AS validation_status, "+
			proteinChangeSQL(has)+" AS protein_change, "+
			"m.t_alt_count AS t_alt_count, m.t_ref_count AS t_ref_count, "+drivers+", "+nsCol+" AS namespace_columns, "+extras+" "+
			"FROM `%s`.%s m%s "+
			"LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM `"+rawDB+"`.clinical_sample) sp "+
			"ON sp.study_id = m.study_id AND sp.sample_id = m.tumor_sample_barcode "+
			"LEFT JOIN `"+targetDB+"`.mutation_merge mm ON mm.study_id = m.study_id AND mm.profile_stable_id = m.profile_stable_id "+
			"AND mm.file_num = m.file_num AND mm.row_num = m.row_num "+
			"WHERE "+mutationFilterSQL("m", "gk.resolved_entrez_gene_id", targetDB),
		entrez, rawDB, table, join)
}

// mafPartSQL is a MAF value as the importer reads it
// (TabDelimitedFileUtil.getPartString): "NA" when empty or missing.
func mafPartSQL(col string) string {
	return fmt.Sprintf("if(%[1]s = '', 'NA', %[1]s)", col)
}

// mafPositionSQL is a MAF start or end position as ImportExtendedMutationData
// stores it: a negative or unreadable position is 0.
func mafPositionSQL(col string) string {
	return fmt.Sprintf("if(ifNull(toInt64OrNull(%[1]s), -1) < 0, '0', %[1]s)", col)
}

// tumorSeqAlleleSQL is the tumor allele the importer stores for a MAF record
// (alias a), MafUtil.resolveTumorSeqAllele over Reference_Allele,
// Tumor_Seq_Allele1 and Tumor_Seq_Allele2: the allele that differs from the
// reference, Tumor_Seq_Allele1 when both do (Tumor_Seq_Allele2 when one of
// them is a deletion "-" and the other a valid sequence and Tumor_Seq_Allele1
// is the "-"), empty when neither is given.
func tumorSeqAlleleSQL(a string) string {
	ref, t1, t2 := mafPartSQL(a+".reference_allele"), mafPartSQL(a+".tumor_seq_allele1"), mafPartSQL(a+".tumor_seq_allele2")
	return fmt.Sprintf("multiIf(upper(%[2]s) = 'NA' AND upper(%[3]s) = 'NA', '', "+
		"%[2]s = 'NA' OR %[2]s = %[1]s, %[3]s, %[3]s = 'NA' OR %[3]s = %[1]s, %[2]s, "+
		"(%[2]s = '-' OR %[3]s = '-') AND (match(upper(%[2]s), '^[ATGC]*$') OR match(upper(%[3]s), '^[ATGC]*$')), if(%[3]s = '-', %[2]s, %[3]s), "+
		"%[2]s)", ref, t1, t2)
}

// cnaSQL builds the `cna` view: the discrete copy-number values cbioportal-core
// stores in genetic_alteration (every integer level -2..2 per gene x sample, so
// GAIN/HETLOSS/DIPLOID queries work), from the DISCRETE wide matrices
// (ImportTabDelimData) and the discrete long files (ImportCnaDiscreteLongData,
// which keeps the first line per gene and sample). Both importers store a
// partial deletion -1.5 as a deep deletion -2 in cna_event. Genes are resolved through gene_key_map; unresolved lines are
// skipped, as are matrix lines stored through the microRNA branch, which
// ImportTabDelimData keeps out of cna_event.
func cnaSQL(c *client, rawDB, targetDB string, fromMatrix, fromLong bool) string {
	const levels = "('-2', '-1', '0', '1', '2')"
	var parts []string
	if fromMatrix {
		parts = append(parts, fmt.Sprintf(
			"SELECT c.study_id AS study_id, c.sample_id AS sample_id, gk.resolved_hugo_gene_symbol AS hugo_symbol, "+
				"toString(gk.resolved_entrez_gene_id) AS entrez_gene_id, if(c.value = '-1.5', '-2', c.value) AS alteration, c.profile_stable_id AS profile_stable_id, "+
				"'' AS cbp_driver, '' AS cbp_driver_tiers, '' AS cbp_driver_annotation, '' AS cbp_driver_tiers_annotation, '{}' AS namespace_columns "+
				"FROM `%s`.cna_matrix c%s WHERE c.datatype = 'DISCRETE' AND gk.mirna = 0 AND alteration IN %s",
			rawDB, geneKeyJoin(targetDB, "cna_matrix", "c", "hugo_symbol", "entrez_gene_id"), levels))
	}
	if fromLong {
		lcols, _ := c.TableColumns("cna_discrete_long")
		opt := func(name string) string {
			if lcols[name] {
				return driverValue("c." + name)
			}
			return "''"
		}
		ns := namespaceColumnsExpr(c, rawDB, lcols, "c.")
		parts = append(parts, fmt.Sprintf(
			"SELECT * FROM (SELECT c.study_id AS study_id, c.sample_id AS sample_id, gk.resolved_hugo_gene_symbol AS hugo_symbol, "+
				"toString(gk.resolved_entrez_gene_id) AS entrez_gene_id, if(c.value = '-1.5', '-2', c.value) AS alteration, "+
				"c.profile_stable_id AS profile_stable_id, %s AS cbp_driver, %s AS cbp_driver_tiers, "+
				"%s AS cbp_driver_annotation, %s AS cbp_driver_tiers_annotation, %s AS namespace_columns FROM `%s`.cna_discrete_long c%s "+
				"WHERE alteration IN %s) LIMIT 1 BY study_id, profile_stable_id, entrez_gene_id, sample_id",
			opt("cbp_driver"), opt("cbp_driver_tiers"), opt("cbp_driver_annotation"), opt("cbp_driver_tiers_annotation"), ns, rawDB,
			geneKeyJoin(targetDB, "cna_discrete_long", "c", "hugo_symbol", "entrez_gene_id"), levels))
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

func clinicalLong(c *client, rawDB, targetDB, srcTable, idCol string) error {
	sel, err := clinicalLongSQL(c, rawDB, srcTable, idCol)
	if err != nil {
		return err
	}
	_, err = c.Execute(fmt.Sprintf("CREATE OR REPLACE VIEW `%s`.`%s` AS %s", targetDB, srcTable, sel))
	return err
}

// clinicalLongSQL unpivots a wide clinical staging table into
// (study_id, <id>, attr_id, attr_value) rows, dropping missing values.
func clinicalLongSQL(c *client, rawDB, srcTable, idCol string) (string, error) {
	cols, err := c.TableColumns(srcTable)
	if err != nil {
		return "", err
	}
	skip := map[string]bool{"study_id": true, "source_file": true, "alteration_type": true,
		"datatype": true, "profile_stable_id": true, "patient_id": true, "sample_id": true}
	var names []string
	for n := range cols {
		if !skip[n] && !OrderColumns[n] {
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
		value := "kv.2"
		if srcTable == "clinical_patient" {
			value = survivalStatusSQL("kv.1", "kv.2")
		}
		sel = fmt.Sprintf(
			"SELECT study_id, `%s` AS %s, kv.1 AS attr_id, %s AS attr_value FROM `%s`.`%s` ARRAY JOIN [%s] AS kv WHERE NOT %s",
			idCol, idCol, value, rawDB, srcTable, strings.Join(pairs, ", "), missingClinicalValue("kv.2"))
		renames, err := attrIDRenames(c, rawDB)
		if err != nil {
			return "", err
		}
		if renames != "" {
			sel = fmt.Sprintf(
				"SELECT l.study_id AS study_id, l.%[1]s AS %[1]s, if(r.attr_id != '', r.attr_id, l.attr_id) AS attr_id, l.attr_value AS attr_value "+
					"FROM (%[2]s) l LEFT JOIN (%[3]s) r ON r.study_id = l.study_id AND r.col = l.attr_id",
				idCol, sel, renames)
		}
	}
	return sel, nil
}

// attrIDRenames selects (study_id, col, attr_id) for the clinical attributes
// whose id is not the upper-cased name of their staging column, e.g.
// "BMI>25" (column bmi_25) or "1P19Q_STATUS" (column _1p19q_status): the
// importer keeps the header's id as written. Empty when there are none.
func attrIDRenames(c *client, rawDB string) (string, error) {
	if ok, _ := c.TableExists("clinical_attribute_meta"); !ok {
		return "", nil
	}
	text, err := c.Execute(fmt.Sprintf(
		"SELECT DISTINCT study_id, attr_id FROM `%s`.clinical_attribute_meta ORDER BY study_id, attr_id FORMAT TabSeparated", rawDB))
	if err != nil {
		return "", err
	}
	var rows []string
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		study, attr, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if col := strings.ToUpper(parser.SanitizeIdent(attr)); col != attr {
			rows = append(rows, fmt.Sprintf("SELECT '%s' AS study_id, '%s' AS col, '%s' AS attr_id",
				ch.SQLLit(study), ch.SQLLit(col), ch.SQLLit(attr)))
		}
	}
	return strings.Join(rows, " UNION ALL "), nil
}

// molecularProfileSQL builds the molecular profiles from the staged meta
// files; the generic assay pivot threshold, sort order and patient level
// come from pivot_threshold_value, value_sort_order and patient_level where
// the staging table has them.
func molecularProfileSQL(c *client, rawDB string) string {
	extra := ""
	if hasColumn(c, rawDB, "genetic_profile", "sort_order") && hasColumn(c, rawDB, "genetic_profile", "pivot_threshold") &&
		hasColumn(c, rawDB, "genetic_profile", "patient_level") {
		extra = ", toFloat64OrNull(pivot_threshold) AS pivot_threshold, nullIf(trimBoth(sort_order), '') AS sort_order, " +
			"toUInt8(lower(trimBoth(patient_level)) = 'true') AS patient_level"
	}
	return fmt.Sprintf(
		"SELECT molecular_profile_id, study_id, alteration_type AS molecular_alteration_type, datatype, name, description, "+
			"show_in_analysis = 'true' AS show_profile_in_analysis_tab, profile_stable_id, generic_assay_type%s FROM `%s`.genetic_profile",
		extra, rawDB)
}

// survivalStatusSQL is cbioportal-core's SurvivalAttributeUtil as
// ImportClinicalData applies it to patient attributes: an OS_STATUS or
// DFS_STATUS value in the old format gets its status prefix ("DECEASED" ->
// "1:DECEASED", "LIVING" -> "0:LIVING"); other values are kept.
func survivalStatusSQL(attr, value string) string {
	return fmt.Sprintf("if(%[1]s IN ('OS_STATUS', 'DFS_STATUS'), multiIf("+
		"upper(replaceAll(trimBoth(%[2]s), '/', '_')) IN ('DECEASED', 'RECURRED_PROGRESSED', 'RECURRED', 'PROGRESSED'), concat('1:', trimBoth(%[2]s)), "+
		"upper(trimBoth(%[2]s)) IN ('LIVING', 'DISEASEFREE'), concat('0:', trimBoth(%[2]s)), %[2]s), %[2]s)", attr, value)
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

func sortedColumnNames(cols map[string]bool) []string {
	out := make([]string, 0, len(cols))
	for c := range cols {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
