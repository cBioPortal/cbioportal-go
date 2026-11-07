package transform

import (
	"fmt"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// ApplyFromProd builds every object the API reads from a clone of
// cBioPortal's production ClickHouse database (prodDB: cbioportal-core's
// normalized schema plus the `*_derived` tables) instead of staged study
// files: the portal objects in targetDB and the staging-shaped objects the
// API reads from the raw database in rawDB. prodDB also serves as the
// reference database (gene, gene_alias, type_of_cancer, reference_genome*).
//
// Production's `*_derived` tables are served through views (no copy). The
// normalized tables are reshaped into our model: small and per-request
// objects are materialized with sort keys that match the API's filters;
// the molecular matrices stay views over genetic_alteration_derived and
// generic_assay_data_derived, whose sort keys the API's study / profile /
// gene filters reach through the views.
//
// The objects have the column names and types of Apply's, and the same
// meaning: rows hold only stored values (production's clinical_data_derived
// blank cells are left out), copy-number events of the discrete matrix
// carry production's driver annotations, and so on.
func ApplyFromProd(base *ch.Client, prodDB, rawDB, targetDB string) ([]ViewCount, error) {
	for _, db := range []string{targetDB, rawDB} {
		if err := base.EnsureDatabase(db); err != nil {
			return nil, err
		}
	}
	b := &prodBuilder{c: base, p: "`" + prodDB + "`", t: "`" + targetDB + "`", r: "`" + rawDB + "`"}
	text, err := base.Execute(fmt.Sprintf(
		"SELECT count() FROM system.tables WHERE database = '%s' AND name = 'resource' FORMAT TabSeparated", prodDB))
	if err != nil {
		return nil, err
	}
	b.unifiedResource = strings.TrimSpace(text) != "0"
	text, err = base.Execute(fmt.Sprintf(
		"SELECT count() FROM system.columns WHERE database = '%s' AND table = 'cancer_study' AND name = 'license' FORMAT TabSeparated",
		prodDB))
	if err != nil {
		return nil, err
	}
	b.studyLicense = strings.TrimSpace(text) != "0"
	for _, step := range b.steps() {
		if err := b.build(step); err != nil {
			return nil, err
		}
	}
	// Tables report their row count; views over the clone (billions of rows
	// for the matrices) are listed without one.
	var counts []ViewCount
	for _, o := range b.built {
		if o.orderBy == "" {
			counts = append(counts, ViewCount{Name: o.full() + " (view)"})
			continue
		}
		text, err := base.Execute(fmt.Sprintf(
			"SELECT total_rows FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TabSeparated",
			strings.Trim(o.db, "`"), o.name))
		if err != nil {
			return nil, fmt.Errorf("counting %s: %w", o.full(), err)
		}
		var n uint64
		fmt.Sscan(strings.TrimSpace(text), &n)
		counts = append(counts, ViewCount{Name: o.full(), Rows: n})
	}
	return counts, nil
}

// prodObject is one view or table built from the production clone. A table
// (orderBy set) is materialized, replacing any earlier object of that name.
type prodObject struct {
	db, name string
	orderBy  string
	sql      string
}

type prodBuilder struct {
	c       *ch.Client
	p, t, r string // quoted production, target and raw database names
	built   []prodObject
	// unifiedResource: production has the one resource table of schema
	// 3.1.0 rather than resource_study, resource_patient and resource_sample.
	unifiedResource bool
	// studyLicense: production's cancer_study has the optional license
	// column (an SPDX id).
	studyLicense bool
}

// studyLicenseSQL is the cancer_study license, empty when unset or when the
// production schema has no such column.
func (b *prodBuilder) studyLicenseSQL() string {
	if b.studyLicense {
		return "ifNull(c.license, '') AS license"
	}
	return "'' AS license"
}

func (o prodObject) full() string { return o.db + ".`" + o.name + "`" }

func (b *prodBuilder) build(o prodObject) error {
	full := o.full()
	engine, err := b.c.Execute(fmt.Sprintf(
		"SELECT engine FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TabSeparated",
		strings.Trim(o.db, "`"), o.name))
	if err != nil {
		return err
	}
	engine = strings.TrimSpace(engine)
	var stmt string
	if o.orderBy == "" {
		if engine != "" && engine != "View" {
			if _, err := b.c.Execute("DROP TABLE " + full + " SYNC"); err != nil {
				return err
			}
		}
		stmt = fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", full, o.sql)
	} else {
		if engine == "View" {
			if _, err := b.c.Execute("DROP VIEW " + full); err != nil {
				return err
			}
		}
		stmt = fmt.Sprintf("CREATE OR REPLACE TABLE %s ENGINE = MergeTree ORDER BY %s AS %s", full, o.orderBy, o.sql)
	}
	if _, err := b.c.Execute(stmt); err != nil {
		return fmt.Errorf("building %s: %w", full, err)
	}
	b.built = append(b.built, o)
	return nil
}

// resourceTable builds the resource table, with the columns of the staged
// build: from production's resource table, or with no display name or
// metadata from the per-level tables of a schema before 3.1.0.
func (b *prodBuilder) resourceTable() prodObject {
	const order = "(study_id, resource_id, resource_scope)"
	if b.unifiedResource {
		return b.table("resource", order,
			"SELECT cs.study_id AS study_id, r.resource_id AS resource_id, r.resource_scope AS resource_scope, "+
				"r.patient_id AS patient_id, r.sample_id AS sample_id, r.url AS url, r.display_name AS display_name, "+
				"r.metadata AS metadata, '' AS source_file, toUInt64(r.internal_id) AS row_num "+
				"FROM {p}.resource r INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = r.cancer_study_id "+
				"WHERE r.resource_scope IN ('STUDY', 'PATIENT', 'SAMPLE')")
	}
	const none = "CAST(NULL AS Nullable(String))"
	return b.table("resource", order,
		"SELECT cs.study_id AS study_id, r.resource_id AS resource_id, 'STUDY' AS resource_scope, "+
			none+" AS patient_id, "+none+" AS sample_id, r.url AS url, "+none+" AS display_name, "+none+" AS metadata, "+
			"'' AS source_file, toUInt64(0) AS row_num "+
			"FROM {p}.resource_study r INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = r.internal_id "+
			"UNION ALL SELECT p.study_id, r.resource_id, 'PATIENT', CAST(p.patient_id AS Nullable(String)), "+none+", "+
			"r.url, "+none+", "+none+", '', toUInt64(0) "+
			"FROM {p}.resource_patient r INNER JOIN {t}.prod_patient p ON p.internal_id = r.internal_id "+
			"UNION ALL SELECT s.study_id, r.resource_id, 'SAMPLE', CAST(s.patient_id AS Nullable(String)), "+
			"CAST(s.sample_id AS Nullable(String)), r.url, "+none+", "+none+", '', toUInt64(0) "+
			"FROM {p}.resource_sample r INNER JOIN {t}.prod_sample s ON s.internal_id = r.internal_id")
}

// sql substitutes {p} (production clone), {t} (target) and {r} (raw).
func (b *prodBuilder) sql(s string) string {
	return strings.NewReplacer("{p}", b.p, "{t}", b.t, "{r}", b.r).Replace(s)
}

func (b *prodBuilder) table(name, orderBy, sql string) prodObject {
	return prodObject{db: b.t, name: name, orderBy: orderBy, sql: b.sql(sql)}
}

func (b *prodBuilder) view(name, sql string) prodObject {
	return prodObject{db: b.t, name: name, sql: b.sql(sql)}
}

func (b *prodBuilder) rawView(name, sql string) prodObject {
	return prodObject{db: b.r, name: name, sql: b.sql(sql)}
}

func (b *prodBuilder) rawTable(name, orderBy, sql string) prodObject {
	return prodObject{db: b.r, name: name, orderBy: orderBy, sql: b.sql(sql)}
}

// naText is a production text value as the importers store a missing one:
// "NA" when NULL or empty.
func naText(col string) string {
	return fmt.Sprintf("if(ifNull(%[1]s, '') = '', 'NA', %[1]s)", col)
}

// partInt is a production integer as our staging-derived objects carry it: the
// number as text, "-1" when NULL.
func partInt(col string) string {
	return fmt.Sprintf("ifNull(toString(%s), '-1')", col)
}

// matrixTables maps the molecular alteration types stored in production's
// genetic_alteration_derived to the staging matrix the API reads them from.
var matrixTables = [][2]string{
	{"COPY_NUMBER_ALTERATION", "cna_matrix"},
	{"MRNA_EXPRESSION", "mrna_expression_matrix"},
	{"METHYLATION", "methylation_matrix"},
	{"PROTEIN_LEVEL", "protein_level_matrix"},
}

func (b *prodBuilder) steps() []prodObject {
	var out []prodObject
	add := func(o ...prodObject) { out = append(out, o...) }

	// Keys from production's internal ids to stable ids.
	add(
		b.table("prod_study", "cancer_study_id",
			"SELECT cancer_study_id, assumeNotNull(cancer_study_identifier) AS study_id FROM {p}.cancer_study"),
		b.table("prod_patient", "internal_id",
			"SELECT p.internal_id AS internal_id, cs.study_id AS study_id, p.stable_id AS patient_id "+
				"FROM {p}.patient p INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = p.cancer_study_id"),
		b.table("prod_sample", "internal_id",
			"SELECT s.internal_id AS internal_id, p.study_id AS study_id, s.stable_id AS sample_id, "+
				"p.patient_id AS patient_id, p.internal_id AS patient_internal_id "+
				"FROM {p}.sample s INNER JOIN {t}.prod_patient p ON p.internal_id = s.patient_id"),
		b.table("prod_profile", "genetic_profile_id",
			"SELECT gp.genetic_profile_id AS genetic_profile_id, gp.stable_id AS molecular_profile_id, cs.study_id AS study_id, "+
				"replaceOne(gp.stable_id, concat(cs.study_id, '_'), '') AS profile_stable_id, "+
				"gp.genetic_alteration_type AS alteration_type, gp.datatype AS datatype, "+
				"ifNull(gp.patient_level, 0) = 1 AS patient_level "+
				"FROM {p}.genetic_profile gp INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = gp.cancer_study_id"),
	)

	// Study-level metadata.
	add(
		b.table("cancer_study", "study_id",
			"SELECT cs.study_id AS study_id, c.name AS name, '' AS short_name, c.description AS description, "+
				"c.type_of_cancer_id AS cancer_type_id, ifNull(c.groups, '') AS groups, ifNull(c.pmid, '') AS pmid, "+
				"ifNull(c.citation, '') AS citation, "+b.studyLicenseSQL()+", ifNull(rg.name, '') AS reference_genome, toUInt8(0) AS add_global_case_list, "+
				"c.import_date AS import_date, toUInt8(c.public = 1) AS public_study, toInt64(ifNull(c.status, 1)) AS status, "+
				"toUInt64(c.cancer_study_id) AS internal_id FROM {p}.cancer_study c INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = c.cancer_study_id "+
				"LEFT JOIN {p}.reference_genome rg ON rg.reference_genome_id = c.reference_genome_id"),
		// Every cancer type is in the reference type_of_cancer.
		b.view("type_of_cancer",
			"SELECT '' AS type_of_cancer_id, '' AS name, '' AS dedicated_color, '' AS short_name, '' AS parent WHERE 0"),
		b.table("cancer_study_tags", "study_id",
			"SELECT cs.study_id AS study_id, t.tags AS tags FROM {p}.cancer_study_tags t "+
				"INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = t.cancer_study_id"),
		b.table("clinical_attribute_meta", "(study_id, clinical_attribute_id)",
			"SELECT cs.study_id AS study_id, m.attr_id AS clinical_attribute_id, m.display_name AS display_name, "+
				"m.description AS description, m.datatype AS datatype, m.priority AS priority, toUInt8(m.patient_attribute) AS patient_attribute "+
				"FROM {p}.clinical_attribute_meta m INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = m.cancer_study_id"),
		b.table("molecular_profile", "(study_id, molecular_profile_id)",
			"SELECT p.molecular_profile_id AS molecular_profile_id, p.study_id AS study_id, "+
				"p.alteration_type AS molecular_alteration_type, p.datatype AS datatype, gp.name AS name, "+
				"ifNull(gp.description, '') AS description, toUInt8(gp.show_profile_in_analysis_tab) AS show_profile_in_analysis_tab, "+
				"p.profile_stable_id AS profile_stable_id, ifNull(gp.generic_assay_type, '') AS generic_assay_type, "+
				"toFloat64OrNull(toString(toFloat32(gp.pivot_threshold))) AS pivot_threshold, gp.sort_order AS sort_order, toUInt8(p.patient_level) AS patient_level "+
				"FROM {p}.genetic_profile gp INNER JOIN {t}.prod_profile p ON p.genetic_profile_id = gp.genetic_profile_id"),
		b.table("patient", "(study_id, patient_id)", "SELECT study_id, patient_id, toUInt64(internal_id) AS internal_id FROM {t}.prod_patient"),
		b.table("sample", "(study_id, sample_id)",
			"SELECT study_id, sample_id, patient_id, toUInt64(internal_id) AS internal_id FROM {t}.prod_sample"),
		b.table("sample_list", "(study_id, list_id, sample_id)",
			"SELECT cs.study_id AS study_id, sl.stable_id AS list_id, sl.category AS category, sl.name AS name, "+
				"ifNull(sl.description, '') AS description, s.sample_id AS sample_id, toUInt64(s.internal_id) AS internal_id, "+
				"toUInt64(sl.list_id) AS list_internal_id "+
				"FROM {p}.sample_list sl INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = sl.cancer_study_id "+
				"INNER JOIN {p}.sample_list_list sll ON sll.list_id = sl.list_id "+
				"INNER JOIN {t}.prod_sample s ON s.internal_id = sll.sample_id"),
		// Panels are global in production; study_id stays empty.
		b.table("gene_panel", "(gene_panel_id, hugo_gene_symbol)",
			"SELECT '' AS study_id, gp.stable_id AS gene_panel_id, ifNull(gp.description, '') AS description, "+
				"g.hugo_gene_symbol AS hugo_gene_symbol, toString(g.entrez_gene_id) AS entrez_gene_id "+
				"FROM {p}.gene_panel gp INNER JOIN {p}.gene_panel_list gpl ON gpl.internal_id = gp.internal_id "+
				"INNER JOIN {p}.gene g ON g.entrez_gene_id = gpl.gene_id"),
	)

	// Clinical data in long form (attributes with a stored value).
	add(
		b.table("clinical_sample", "(study_id, sample_id, attr_id)",
			"SELECT s.study_id AS study_id, s.sample_id AS sample_id, c.attr_id AS attr_id, c.attr_value AS attr_value, "+
				"toUInt64(c.internal_id) AS internal_id FROM {p}.clinical_sample c FINAL INNER JOIN {t}.prod_sample s ON s.internal_id = c.internal_id"),
		b.table("clinical_patient", "(study_id, patient_id, attr_id)",
			"SELECT p.study_id AS study_id, p.patient_id AS patient_id, c.attr_id AS attr_id, c.attr_value AS attr_value, "+
				"toUInt64(c.internal_id) AS internal_id FROM {p}.clinical_patient c FINAL INNER JOIN {t}.prod_patient p ON p.internal_id = c.internal_id"),
	)

	// Profiled samples with their gene panel (NULL: whole exome / genome).
	add(b.table("sample_profile", "(study_id, profile_stable_id, sample_id)",
		"SELECT s.study_id AS study_id, s.sample_id AS sample_id, p.profile_stable_id AS profile_stable_id, "+
			"p.molecular_profile_id AS molecular_profile_id, p.alteration_type AS alteration_type, p.datatype AS datatype, "+
			"CAST(if(sp.panel_id IS NULL, NULL, gp.stable_id) AS Nullable(String)) AS gene_panel_id "+
			"FROM {p}.sample_profile sp FINAL INNER JOIN {t}.prod_sample s ON s.internal_id = sp.sample_id "+
			"INNER JOIN {t}.prod_profile p ON p.genetic_profile_id = sp.genetic_profile_id "+
			"LEFT JOIN {p}.gene_panel gp ON gp.internal_id = sp.panel_id"))

	// Mutations of MUTATION_EXTENDED and MUTATION_UNCALLED profiles, from
	// production's joined mutation_derived. Protein positions are written as
	// the MAF column's "start-end", with "?" for an unknown (negative) end,
	// which the API reads as -1, and nothing when both are unknown.
	add(b.table("mutation", "(study_id, molecular_profile_id, sample_id)", `SELECT
  m.studyId AS study_id, m.sampleId AS sample_id, ifNull(m.`+"`GENE.hugoGeneSymbol`"+`, '') AS hugo_symbol,
  toString(m.entrezGeneId) AS entrez_gene_id, ifNull(m.chr, '') AS chromosome,
  ifNull(toString(m.startPosition), '') AS start_position, ifNull(toString(m.endPosition), '') AS end_position,
  ifNull(m.referenceAllele, '') AS reference_allele, ifNull(m.tumorSeqAllele, '') AS tumor_seq_allele2,
  ifNull(m.mutationType, '') AS variant_classification, ifNull(m.variantType, '') AS variant_type,
  ifNull(m.mutationStatus, '') AS mutation_status, ifNull(m.validationStatus, '') AS validation_status,
  ifNull(m.proteinChange, '') AS protein_change,
  ifNull(toString(m.tumorAltCount), '') AS t_alt_count, ifNull(toString(m.tumorRefCount), '') AS t_ref_count,
  ifNull(m.driverFilter, '') AS cbp_driver, ifNull(m.driverFilterAnnotation, '') AS cbp_driver_annotation,
  ifNull(m.driverTiersFilter, '') AS cbp_driver_tiers, ifNull(m.driverTiersFilterAnnotation, '') AS cbp_driver_tiers_annotation,
  ifNull(m.annotationJSON, '') AS namespace_columns, m.molecularProfileId AS molecular_profile_id, m.patientId AS patient_id,
  ifNull(m.center, '') AS center, ifNull(toString(m.normalAltCount), '') AS n_alt_count,
  ifNull(toString(m.normalRefCount), '') AS n_ref_count, ifNull(m.aminoAcidChange, '') AS amino_acid_change,
  ifNull(m.ncbiBuild, '') AS ncbi_build, ifNull(m.refseqMrnaId, '') AS refseq_mrna_id,
  if(m.proteinPosStart IS NULL OR (m.proteinPosStart < 0 AND ifNull(m.proteinPosEnd, -1) < 0), '',
    concat(if(m.proteinPosStart < 0, '?', toString(m.proteinPosStart)), '-',
      if(ifNull(m.proteinPosEnd, -1) < 0, '?', toString(m.proteinPosEnd)))) AS protein_position,
  ifNull(toString(m.`+"`alleleSpecificCopyNumber.ascnIntegerCopyNumber`"+`), '') AS ascn_integer_copy_number,
  ifNull(m.`+"`alleleSpecificCopyNumber.ascnMethod`"+`, '') AS ascn_method,
  ifNull(toString(m.`+"`alleleSpecificCopyNumber.ccfExpectedCopiesUpper`"+`), '') AS ascn_ccf_expected_copies_upper,
  ifNull(toString(m.`+"`alleleSpecificCopyNumber.ccfExpectedCopies`"+`), '') AS ascn_ccf_expected_copies,
  ifNull(m.`+"`alleleSpecificCopyNumber.clonal`"+`, '') AS ascn_clonal,
  ifNull(toString(m.`+"`alleleSpecificCopyNumber.minorCopyNumber`"+`), '') AS ascn_minor_copy_number,
  ifNull(toString(m.`+"`alleleSpecificCopyNumber.expectedAltCopies`"+`), '') AS ascn_expected_alt_copies,
  ifNull(toString(m.`+"`alleleSpecificCopyNumber.totalCopyNumber`"+`), '') AS ascn_total_copy_number
FROM {p}.mutation_derived m
WHERE m.molecularProfileId IN (SELECT molecular_profile_id FROM {t}.prod_profile
  WHERE alteration_type IN ('MUTATION_EXTENDED', 'MUTATION_UNCALLED'))`))

	// Discrete copy number: production's AMP / HOMDEL events (cna_event, with
	// their driver annotations) and, for the other levels, the discrete
	// profiles' cells of genetic_alteration_derived. Each other level is its
	// own branch with a constant alteration, so a query for AMP / HOMDEL only
	// (the usual one) skips the matrix altogether.
	var discreteLevelBranches []string
	for _, level := range []string{"-1", "0", "1"} {
		discreteLevelBranches = append(discreteLevelBranches, fmt.Sprintf(
			"SELECT ga.cancer_study_identifier AS study_id, "+
				"substring(ga.sample_unique_id, length(ga.cancer_study_identifier) + 2) AS sample_id, "+
				"ga.hugo_gene_symbol AS hugo_symbol, toString(g.entrez_gene_id) AS entrez_gene_id, "+
				"'%[1]s' AS alteration, ga.profile_type AS profile_stable_id, "+
				"'' AS cbp_driver, '' AS cbp_driver_tiers, '' AS cbp_driver_annotation, '' AS cbp_driver_tiers_annotation, "+
				"'' AS namespace_columns "+
				"FROM {p}.genetic_alteration_derived ga INNER JOIN {p}.gene g ON g.hugo_gene_symbol = ga.hugo_gene_symbol "+
				"WHERE ga.alteration_value = '%[1]s' AND (ga.cancer_study_identifier, ga.profile_type) IN "+
				"(SELECT study_id, profile_stable_id FROM {t}.prod_profile WHERE alteration_type = 'COPY_NUMBER_ALTERATION' AND datatype = 'DISCRETE')",
			level))
	}
	add(
		b.table("prod_cna_event", "(study_id, profile_stable_id, sample_id)",
			"SELECT s.study_id AS study_id, s.sample_id AS sample_id, g.hugo_gene_symbol AS hugo_symbol, "+
				"toString(ce.entrez_gene_id) AS entrez_gene_id, toString(ce.alteration) AS alteration, "+
				"p.profile_stable_id AS profile_stable_id, "+driverValue("ifNull(ada.driver_filter, '')")+" AS cbp_driver, "+
				driverValue("ifNull(ada.driver_tiers_filter, '')")+" AS cbp_driver_tiers, "+
				"ifNull(ada.driver_filter_annotation, '') AS cbp_driver_annotation, "+
				"ifNull(ada.driver_tiers_filter_annotation, '') AS cbp_driver_tiers_annotation, "+
				"ifNull(sce.annotation_json, '') AS namespace_columns "+
				"FROM {p}.sample_cna_event sce INNER JOIN {p}.cna_event ce ON ce.cna_event_id = sce.cna_event_id "+
				"INNER JOIN {t}.prod_sample s ON s.internal_id = sce.sample_id "+
				"INNER JOIN {t}.prod_profile p ON p.genetic_profile_id = sce.genetic_profile_id "+
				"INNER JOIN {p}.gene g ON g.entrez_gene_id = ce.entrez_gene_id "+
				"LEFT JOIN {p}.alteration_driver_annotation ada ON ada.alteration_event_id = sce.cna_event_id "+
				"AND ada.genetic_profile_id = sce.genetic_profile_id AND ada.sample_id = sce.sample_id"),
		b.view("cna", "SELECT * FROM {t}.prod_cna_event UNION ALL "+strings.Join(discreteLevelBranches, " UNION ALL ")),
	)

	// Structural variants, each site under its gene (unresolved: NULL id and
	// empty symbol), text fields "NA" and integers "-1" when missing.
	sv := []string{
		"s.study_id AS study_id", "s.sample_id AS sample_id", "p.profile_stable_id AS profile_stable_id",
		"g1.hugo_gene_symbol AS site1_hugo_symbol",
		"if(g1.hugo_gene_symbol = '', CAST(NULL AS Nullable(Int64)), v.site1_entrez_gene_id) AS site1_entrez_gene_id",
		"g2.hugo_gene_symbol AS site2_hugo_symbol",
		"if(g2.hugo_gene_symbol = '', CAST(NULL AS Nullable(Int64)), v.site2_entrez_gene_id) AS site2_entrez_gene_id",
		"v.sv_status AS sv_status",
		driverValue("ifNull(ada.driver_filter, '')") + " AS cbp_driver",
		driverValue("ifNull(ada.driver_tiers_filter, '')") + " AS cbp_driver_tiers",
		"ifNull(ada.driver_filter_annotation, '') AS cbp_driver_annotation",
		"ifNull(ada.driver_tiers_filter_annotation, '') AS cbp_driver_tiers_annotation",
		"ifNull(v.annotation_json, '') AS namespace_columns",
	}
	for _, n := range svStringColumns {
		sv = append(sv, naText("v.`"+n+"`")+" AS `"+n+"`")
	}
	for _, n := range svIntColumns {
		src := n
		if n == "sv_length" {
			src = "length"
		}
		sv = append(sv, partInt("v.`"+src+"`")+" AS `"+n+"`")
	}
	add(b.table("structural_variant", "(study_id, profile_stable_id, sample_id)",
		"SELECT "+strings.Join(sv, ", ")+" FROM {p}.structural_variant v "+
			"INNER JOIN {t}.prod_sample s ON s.internal_id = v.sample_id "+
			"INNER JOIN {t}.prod_profile p ON p.genetic_profile_id = v.genetic_profile_id "+
			"LEFT JOIN (SELECT entrez_gene_id, hugo_gene_symbol FROM {p}.gene) g1 ON g1.entrez_gene_id = v.site1_entrez_gene_id "+
			"LEFT JOIN (SELECT entrez_gene_id, hugo_gene_symbol FROM {p}.gene) g2 ON g2.entrez_gene_id = v.site2_entrez_gene_id "+
			"LEFT JOIN {p}.alteration_driver_annotation ada ON ada.alteration_event_id = v.internal_id "+
			"AND ada.genetic_profile_id = v.genetic_profile_id AND ada.sample_id = v.sample_id"))

	add(b.table("seg", "(study_id, sample_id)",
		"SELECT s.study_id AS study_id, s.sample_id AS sample_id, c.chr AS chrom, toString(c.start) AS start, "+
			"toString(c.end) AS end, toString(c.num_probes) AS num_probes, toString(c.segment_mean) AS segment_mean, "+
			"toUInt64(c.seg_id) AS seg_id FROM {p}.copy_number_seg c INNER JOIN {t}.prod_sample s ON s.internal_id = c.sample_id"))

	// The gene keys of the matrix views: each gene of the reference under
	// its own symbol and Entrez id, for every gene-keyed matrix profile.
	source := "multiIf("
	for _, m := range matrixTables {
		source += fmt.Sprintf("p.alteration_type = '%s', '%s', ", m[0], m[1])
	}
	source += "'')"
	add(b.view("gene_key_map",
		"SELECT "+source+" AS source, "+
			"p.study_id AS study_id, p.profile_stable_id AS profile_stable_id, g.hugo_gene_symbol AS hugo_symbol, "+
			"toString(g.entrez_gene_id) AS entrez_gene_id, g.entrez_gene_id AS resolved_entrez_gene_id, "+
			"g.hugo_gene_symbol AS resolved_hugo_gene_symbol, toUInt8(0) AS mirna "+
			"FROM {t}.prod_profile p CROSS JOIN {p}.gene g WHERE source != ''"))

	// Production's derived tables, read in place.
	add(
		b.view("sample_derived", "SELECT * FROM {p}.sample_derived"),
		b.view("clinical_data_derived", "SELECT * FROM {p}.clinical_data_derived WHERE attribute_value != ''"),
		b.view("genomic_event_derived",
			"SELECT * REPLACE (if(variant_type = 'cna', '', mutation_status) AS mutation_status) FROM {p}.genomic_event_derived"),
		b.view("genetic_alteration_derived", "SELECT * FROM {p}.genetic_alteration_derived"),
		b.view("generic_assay_data_derived",
			"SELECT * REPLACE (toUInt8(patient_level) AS patient_level) FROM {p}.generic_assay_data_derived"),
		b.view("sample_to_gene_panel_derived", "SELECT * FROM {p}.sample_to_gene_panel_derived"),
		b.view("gene_panel_to_gene_derived", "SELECT * FROM {p}.gene_panel_to_gene_derived"),
		b.view("clinical_event_derived", "SELECT * FROM {p}.clinical_event_derived"),
		b.view("clinical_event_data_derived", "SELECT * FROM {p}.clinical_event_data_derived"),
	)

	// Study resources.
	add(
		b.table("resource_definition", "(study_id, resource_id)",
			"SELECT cs.study_id AS study_id, d.resource_id AS resource_id, d.display_name AS display_name, "+
				"ifNull(d.description, '') AS description, d.resource_type AS resource_type, "+
				"toUInt8(ifNull(d.open_by_default, 0)) AS open_by_default, toInt32(d.priority) AS priority, "+
				"d.custom_metadata AS custom_metadata "+
				"FROM {p}.resource_definition d INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = d.cancer_study_id"),
	)
	add(b.resourceTable())
	add(
		b.view("resource_study", "SELECT study_id, resource_id, url FROM {t}.resource WHERE resource_scope = 'STUDY'"),
		b.view("resource_patient", "SELECT study_id, assumeNotNull(patient_id) AS patient_id, resource_id, url "+
			"FROM {t}.resource WHERE resource_scope = 'PATIENT'"),
		b.view("resource_sample", "SELECT study_id, assumeNotNull(patient_id) AS patient_id, assumeNotNull(sample_id) AS sample_id, "+
			"resource_id, url FROM {t}.resource WHERE resource_scope = 'SAMPLE'"),
	)

	// GISTIC regions and their genes, MutSig genes.
	add(
		b.table("gistic_to_gene", "(study_id, gistic_roi_id, gene_index)",
			"SELECT cs.study_id AS study_id, toUInt64(gg.gistic_roi_id) AS gistic_roi_id, "+
				"toUInt64(row_number() OVER (PARTITION BY gg.gistic_roi_id ORDER BY gg.entrez_gene_id)) AS gene_index, "+
				"gg.entrez_gene_id AS entrez_gene_id, g.hugo_gene_symbol AS hugo_gene_symbol "+
				"FROM {p}.gistic_to_gene gg INNER JOIN {p}.gistic r ON r.gistic_roi_id = gg.gistic_roi_id "+
				"INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = r.cancer_study_id "+
				"INNER JOIN {p}.gene g ON g.entrez_gene_id = gg.entrez_gene_id"),
		b.table("gistic", "(study_id, gistic_roi_id)",
			"SELECT cs.study_id AS study_id, toUInt64(r.gistic_roi_id) AS gistic_roi_id, toInt32(r.chromosome) AS chromosome, "+
				"r.cytoband AS cytoband, r.wide_peak_start AS wide_peak_start, r.wide_peak_end AS wide_peak_end, "+
				"r.q_value AS q_value, toString(r.q_value) AS q_value_text, r.amp = 1 AS amp "+
				"FROM {p}.gistic r INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = r.cancer_study_id "+
				"WHERE (cs.study_id, toUInt64(r.gistic_roi_id)) IN (SELECT study_id, gistic_roi_id FROM {t}.gistic_to_gene)"),
		b.table("mutsig", "(study_id, `rank`)",
			"SELECT cs.study_id AS study_id, toInt32(m.`rank`) AS `rank`, m.entrez_gene_id AS entrez_gene_id, "+
				"g.hugo_gene_symbol AS hugo_gene_symbol, toInt32(m.NumBasesCovered) AS num_bases_covered, "+
				"toInt32(m.NumMutations) AS num_mutations, m.p_value AS p_value, m.q_value AS q_value, "+
				"toString(m.p_value) AS p_value_text, toString(m.q_value) AS q_value_text "+
				"FROM {p}.mut_sig m INNER JOIN {t}.prod_study cs ON cs.cancer_study_id = m.cancer_study_id "+
				"INNER JOIN {p}.gene g ON g.entrez_gene_id = m.entrez_gene_id"),
	)

	// Staging-shaped objects the API reads from the raw database.
	add(b.rawView("genetic_profile",
		"SELECT p.molecular_profile_id AS molecular_profile_id, p.study_id AS study_id, p.profile_stable_id AS profile_stable_id, "+
			"p.alteration_type AS alteration_type, p.datatype AS datatype, gp.name AS name, ifNull(gp.description, '') AS description, "+
			"if(gp.show_profile_in_analysis_tab = 1, 'true', 'false') AS show_in_analysis, "+
			"ifNull(gp.generic_assay_type, '') AS generic_assay_type, '' AS namespaces, '' AS gene_panel, "+
			"if(p.patient_level, 'true', 'false') AS patient_level "+
			"FROM {t}.prod_profile p INNER JOIN {p}.genetic_profile gp ON gp.genetic_profile_id = p.genetic_profile_id"))
	// The samples of each matrix profile in matrix order (production's
	// genetic_profile_samples, internal sample ids with a trailing comma),
	// shaped like the ingest's header rows, numbered and stored in matrix order.
	add(b.rawTable("profile_sample", "(study_id, profile_stable_id, position)",
		"SELECT p.study_id AS study_id, '' AS source_file, p.profile_stable_id AS profile_stable_id, "+
			"'header' AS kind, s.sample_id AS sample_id, gps.position AS position, "+
			"toUInt32(0) AS file_num, toUInt64(gps.position) AS row_num "+
			"FROM (SELECT genetic_profile_id, toInt64(id) AS internal_id, position "+
			"FROM (SELECT genetic_profile_id, arrayFilter(x -> x != '', splitByChar(',', ordered_sample_list)) AS ids "+
			"FROM {p}.genetic_profile_samples FINAL) ARRAY JOIN ids AS id, arrayEnumerate(ids) AS position) gps "+
			"INNER JOIN {t}.prod_profile p ON p.genetic_profile_id = gps.genetic_profile_id "+
			"INNER JOIN {t}.prod_sample s ON s.internal_id = gps.internal_id"))
	// Gene matrices: one row per sample x gene x profile with a stored value.
	// Rows are ordered by gene, so the file-order columns the API orders by
	// are constant.
	for _, m := range matrixTables {
		add(b.rawView(m[1], fmt.Sprintf(
			"SELECT ga.cancer_study_identifier AS study_id, ga.profile_type AS profile_stable_id, "+
				"ga.hugo_gene_symbol AS hugo_symbol, toString(g.entrez_gene_id) AS entrez_gene_id, "+
				"substring(ga.sample_unique_id, length(ga.cancer_study_identifier) + 2) AS sample_id, "+
				"ifNull(ga.alteration_value, '') AS value, '%s' AS alteration_type, "+
				"toUInt32(0) AS file_num, toUInt64(0) AS row_num, '' AS _part, toUInt64(0) AS _part_offset "+
				"FROM {p}.genetic_alteration_derived ga LEFT JOIN (SELECT entrez_gene_id, hugo_gene_symbol FROM {p}.gene) g "+
				"ON g.hugo_gene_symbol = ga.hugo_gene_symbol "+
				"WHERE (ga.cancer_study_identifier, ga.profile_type) IN "+
				"(SELECT study_id, profile_stable_id FROM {t}.prod_profile WHERE alteration_type = '%s')", m[0], m[0])))
	}
	// Generic assay cells; entities read in sort order, so the file-order
	// columns the API orders entities by are constant.
	add(
		b.rawView("generic_assay_matrix",
			"SELECT left(d.profile_stable_id, length(d.profile_stable_id) - length(d.profile_type) - 1) AS study_id, "+
				"d.profile_type AS profile_stable_id, d.entity_stable_id AS entity_stable_id, "+
				"substring(d.sample_unique_id, length(d.profile_stable_id) - length(d.profile_type) + 1) AS sample_id, "+
				"d.value AS value, '' AS name, '' AS description, '' AS url, "+
				"toUInt32(0) AS file_num, toUInt64(0) AS row_num, '' AS _part, toUInt64(0) AS _part_offset "+
				"FROM {p}.generic_assay_data_derived d"),
		b.rawView("generic_assay_profile_entity",
			"SELECT p.study_id AS study_id, p.profile_stable_id AS profile_stable_id, e.entity_stable_id AS entity_stable_id "+
				"FROM {p}.generic_assay_profile_entity_derived e INNER JOIN {t}.prod_profile p ON p.molecular_profile_id = e.profile_stable_id"),
		b.rawView("generic_assay_entity_meta",
			"SELECT entity_stable_id, entity_type, properties FROM {p}.generic_assay_meta_derived"),
	)
	// Timeline events, one row per event with its attributes in storage order.
	add(b.rawTable("timeline", "(study_id, patient_id)",
		"SELECT p.study_id AS study_id, p.patient_id AS patient_id, e.event_type AS event_type, "+
			"toString(e.start_date) AS start_date, ifNull(toString(e.stop_date), '') AS stop_date, "+
			"ifNull(d.attrs, []) AS event_attributes, toUInt64(e.clinical_event_id) AS clinical_event_id "+
			"FROM {p}.clinical_event e INNER JOIN {t}.prod_patient p ON p.internal_id = e.patient_id "+
			"LEFT JOIN (SELECT clinical_event_id, groupArray((key, value)) AS attrs FROM "+
			"(SELECT clinical_event_id, key, value FROM {p}.clinical_event_data WHERE value != '' ORDER BY clinical_event_id, _part, _part_offset) "+
			"GROUP BY clinical_event_id) d ON d.clinical_event_id = e.clinical_event_id"))
	return out
}
