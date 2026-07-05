package transform

import "fmt"

// genomicEventDerivedSQL builds prod's `genomic_event_derived` table as a view:
// the unified alteration table combining mutations, discrete CNA, and structural
// variants into one row-per-alteration shape. Column names/order match prod's
// clickhouse.sql (genomic_event_derived) so the column-store API can read it
// unchanged. Data comes from our file-staging tables in rawDB; gene symbols are
// resolved to entrez ids against refDB.gene when the raw entrez is missing.
//
// Divergences from prod (which builds from OLTP mirror tables we do not have):
//   - gene_panel_stable_id comes from gene_panel_matrix per sample, ” when
//     absent (prod substitutes 'WES').
//   - off_panel is a constant false (prod computes it from gene_panel_to_gene).
//   - cna_cytoband is ” (prod joins reference_genome_gene).
func genomicEventDerivedSQL(rawDB, refDB string) string {
	// Resolve entrez to Int32: use the raw entrez when positive, else fall back
	// to the reference gene matched by Hugo symbol (aliased g in each SELECT).
	entrez := func(raw string) string {
		return fmt.Sprintf(
			"toInt32OrZero(if(toInt64OrNull(%s) > 0, %s, toString(ifNull(g.entrez_gene_id, 0))))",
			raw, raw)
	}
	// Normalize a custom-driver filter value: ''/NA/Unknown all collapse to ''.
	drv := func(col string) string {
		return fmt.Sprintf("ifNull(nullIf(nullIf(%s, 'NA'), 'Unknown'), '')", col)
	}

	// Mutation rows. sample id = tumor_sample_barcode; mutation filter mirrors the
	// `mutation` transform view (cBioPortal default MutationFilter).
	mutation := fmt.Sprintf(`SELECT
  concat(m.study_id, '_', m.tumor_sample_barcode) AS sample_unique_id,
  m.hugo_symbol AS hugo_gene_symbol,
  %[3]s AS entrez_gene_id,
  ifNull(gpm.mutations, '') AS gene_panel_stable_id,
  m.study_id AS cancer_study_identifier,
  concat(m.study_id, '_', m.profile_stable_id) AS genetic_profile_stable_id,
  'mutation' AS variant_type,
  if(startsWith(m.hgvsp_short, 'p.'), substring(m.hgvsp_short, 3), m.hgvsp_short) AS mutation_variant,
  m.variant_classification AS mutation_type,
  m.mutation_status AS mutation_status,
  %[4]s AS driver_filter,
  m.cbp_driver_annotation AS driver_filter_annotation,
  %[5]s AS driver_tiers_filter,
  m.cbp_driver_tiers_annotation AS driver_tiers_filter_annotation,
  CAST(NULL AS Nullable(Int8)) AS cna_alteration,
  '' AS cna_cytoband,
  '' AS sv_event_info,
  concat(m.study_id, '_', s.patient_id) AS patient_unique_id,
  CAST(0 AS Bool) AS off_panel
FROM %[1]s.mutation_extended m
LEFT JOIN %[2]s.gene g ON upper(m.hugo_symbol) = upper(g.hugo_gene_symbol)
LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM %[1]s.clinical_sample) s ON m.study_id = s.study_id AND m.tumor_sample_barcode = s.sample_id
LEFT JOIN %[1]s.gene_panel_matrix gpm ON m.study_id = gpm.study_id AND m.tumor_sample_barcode = gpm.sample_id
WHERE NOT startsWith(lower(m.mutation_status), 'none') AND NOT startsWith(lower(m.mutation_status), 'loh')
  AND NOT startsWith(lower(m.mutation_status), 'wildtype') AND NOT startsWith(lower(m.validation_status), 'redacted')
  AND lower(m.variant_classification) NOT IN ('silent', 'intron', '3''utr', '5''utr', '3''flank', 'igr', 'rna')
  AND ( lower(m.variant_classification) != '5''flank' OR m.entrez_gene_id = '7015' OR upper(m.hugo_symbol) = 'TERT' )`,
		rawDB, refDB, entrez("m.entrez_gene_id"), drv("m.cbp_driver"), drv("m.cbp_driver_tiers"))

	// Discrete CNA rows. cna_alteration = the discrete level (-2..2); restricted to
	// real alterations. gene panel from the gistic/cna column of gene_panel_matrix.
	cna := fmt.Sprintf(`SELECT
  concat(c.study_id, '_', c.sample_id) AS sample_unique_id,
  c.hugo_symbol AS hugo_gene_symbol,
  %[3]s AS entrez_gene_id,
  if(gpm.gistic != '', gpm.gistic, gpm.cna) AS gene_panel_stable_id,
  c.study_id AS cancer_study_identifier,
  concat(c.study_id, '_', c.profile_stable_id) AS genetic_profile_stable_id,
  'cna' AS variant_type,
  '' AS mutation_variant,
  '' AS mutation_type,
  '' AS mutation_status,
  %[4]s AS driver_filter,
  c.cbp_driver_annotation AS driver_filter_annotation,
  %[5]s AS driver_tiers_filter,
  c.cbp_driver_tiers_annotation AS driver_tiers_filter_annotation,
  toInt8OrNull(c.value) AS cna_alteration,
  '' AS cna_cytoband,
  '' AS sv_event_info,
  concat(c.study_id, '_', s.patient_id) AS patient_unique_id,
  CAST(0 AS Bool) AS off_panel
FROM %[1]s.cna_discrete_long c
LEFT JOIN %[2]s.gene g ON upper(c.hugo_symbol) = upper(g.hugo_gene_symbol)
LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM %[1]s.clinical_sample) s ON c.study_id = s.study_id AND c.sample_id = s.sample_id
LEFT JOIN %[1]s.gene_panel_matrix gpm ON c.study_id = gpm.study_id AND c.sample_id = gpm.sample_id
WHERE c.value IN ('-2', '-1', '1', '2')`,
		rawDB, refDB, entrez("c.entrez_gene_id"), drv("c.cbp_driver"), drv("c.cbp_driver_tiers"))

	// Structural variant rows keyed on site1. sv_event_info carries event_info;
	// mutation_status reuses sv_status as prod does.
	sv := fmt.Sprintf(`SELECT
  concat(v.study_id, '_', v.sample_id) AS sample_unique_id,
  v.site1_hugo_symbol AS hugo_gene_symbol,
  %[3]s AS entrez_gene_id,
  ifNull(gpm.structural_variants, '') AS gene_panel_stable_id,
  v.study_id AS cancer_study_identifier,
  concat(v.study_id, '_', v.profile_stable_id) AS genetic_profile_stable_id,
  'structural_variant' AS variant_type,
  '' AS mutation_variant,
  '' AS mutation_type,
  v.sv_status AS mutation_status,
  %[4]s AS driver_filter,
  v.cbp_driver_annotation AS driver_filter_annotation,
  %[5]s AS driver_tiers_filter,
  v.cbp_driver_tiers_annotation AS driver_tiers_filter_annotation,
  CAST(NULL AS Nullable(Int8)) AS cna_alteration,
  '' AS cna_cytoband,
  v.event_info AS sv_event_info,
  concat(v.study_id, '_', s.patient_id) AS patient_unique_id,
  CAST(0 AS Bool) AS off_panel
FROM %[1]s.structural_variant v
LEFT JOIN %[2]s.gene g ON upper(v.site1_hugo_symbol) = upper(g.hugo_gene_symbol)
LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM %[1]s.clinical_sample) s ON v.study_id = s.study_id AND v.sample_id = s.sample_id
LEFT JOIN %[1]s.gene_panel_matrix gpm ON v.study_id = gpm.study_id AND v.sample_id = gpm.sample_id`,
		rawDB, refDB, entrez("v.site1_entrez_gene_id"), drv("v.cbp_driver"), drv("v.cbp_driver_tiers"))

	return mutation + "\nUNION ALL\n" + cna + "\nUNION ALL\n" + sv
}
