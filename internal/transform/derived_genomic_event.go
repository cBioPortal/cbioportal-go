package transform

import "fmt"

// genomicEventDerivedSQL builds prod's `genomic_event_derived` table as a view:
// the unified alteration table combining mutations, discrete CNA, and structural
// variants into one row-per-alteration shape. Column names/order match prod's
// clickhouse.sql (genomic_event_derived) so the column-store API can read it
// unchanged. Genes are the reference genes the importers store each record
// under (gene_key_map); records whose gene does not resolve are absent.
//
// Divergences from prod (which builds from OLTP mirror tables we do not have):
//   - gene_panel_stable_id comes from the target-DB sample_profile view per
//     (sample, profile), 'WES' when the panel is NULL or the pair is absent.
//   - discrete CNA events come from the target-DB `cna` view (long and wide
//     DISCRETE profiles); like cna_event, only AMP/HOMDEL are events.
//   - cna_cytoband is the gene's cytoband on the study's reference genome
//     (hg19 unless the study names another) when the reference DB has
//     reference_genome_gene, else empty.
func genomicEventDerivedSQL(rawDB, refDB, targetDB string, hasGenomeGenes bool) string {
	// A custom-driver value is kept as given unless empty or NA, which the
	// importer does not store (alteration_driver_annotation).
	drv := func(col string) string {
		return fmt.Sprintf("if(lower(trimBoth(%[1]s)) IN ('', 'na'), '', %[1]s)", col)
	}
	target := "`" + targetDB + "`"

	// Mutation rows: the MAF records the importer stores (resolved gene,
	// MutationFilter) of MUTATION_EXTENDED and MUTATION_UNCALLED profiles,
	// as in the `mutation` view.
	mutationOf := func(table string) string {
		return fmt.Sprintf(`SELECT
  concat(m.study_id, '_', m.tumor_sample_barcode) AS sample_unique_id,
  gk.resolved_hugo_gene_symbol AS hugo_gene_symbol,
  toInt32(gk.resolved_entrez_gene_id) AS entrez_gene_id,
  ifNull(sp.gene_panel_id, 'WES') AS gene_panel_stable_id,
  m.study_id AS cancer_study_identifier,
  concat(m.study_id, '_', m.profile_stable_id) AS genetic_profile_stable_id,
  'mutation' AS variant_type,
  if(startsWith(m.hgvsp_short, 'p.'), substring(m.hgvsp_short, 3), m.hgvsp_short) AS mutation_variant,
  m.variant_classification AS mutation_type,
  m.mutation_status AS mutation_status,
  %[2]s AS driver_filter,
  m.cbp_driver_annotation AS driver_filter_annotation,
  %[3]s AS driver_tiers_filter,
  m.cbp_driver_tiers_annotation AS driver_tiers_filter_annotation,
  CAST(NULL AS Nullable(Int8)) AS cna_alteration,
  '' AS cna_cytoband,
  '' AS sv_event_info,
  concat(m.study_id, '_', s.patient_id) AS patient_unique_id,
  (ifNull(sp.gene_panel_id, 'WES'), gk.resolved_hugo_gene_symbol) NOT IN (SELECT gene_panel_id, gene FROM %[4]s.gene_panel_to_gene_derived) AS off_panel
FROM %[1]s.%[7]s m%[6]s
LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM %[1]s.clinical_sample) s ON m.study_id = s.study_id AND m.tumor_sample_barcode = s.sample_id
LEFT JOIN %[4]s.sample_profile sp ON m.study_id = sp.study_id AND m.tumor_sample_barcode = sp.sample_id AND m.profile_stable_id = sp.profile_stable_id
WHERE %[5]s`,
			rawDB, drv("m.cbp_driver"), drv("m.cbp_driver_tiers"), target,
			mutationFilterSQL("m", "gk.resolved_entrez_gene_id", targetDB),
			geneKeyJoin(targetDB, table, "m", "hugo_symbol", "entrez_gene_id"), table)
	}
	mutation := mutationOf("mutation_extended") + "\nUNION ALL\n" + mutationOf("mutation_uncalled")

	// Discrete CNA rows (cna_event): AMP and HOMDEL of the `cna` view.
	cytoband, cytobandJoin := "''", ""
	if hasGenomeGenes {
		cytoband = "ifNull(rgg.cytoband, '')"
		cytobandJoin = fmt.Sprintf(`
LEFT JOIN (SELECT study_id, if(reference_genome = '', 'hg19', reference_genome) AS genome FROM %[1]s.cancer_study) cs ON cs.study_id = c.study_id
LEFT JOIN (SELECT g.entrez_gene_id AS entrez_gene_id, g.cytoband AS cytoband, lower(r.name) AS genome FROM `+"`%[2]s`"+`.reference_genome_gene g
  INNER JOIN `+"`%[2]s`"+`.reference_genome r ON r.reference_genome_id = g.reference_genome_id) rgg
  ON rgg.entrez_gene_id = toInt64OrZero(c.entrez_gene_id) AND rgg.genome = lower(cs.genome)`, rawDB, refDB)
	}
	cna := fmt.Sprintf(`SELECT
  concat(c.study_id, '_', c.sample_id) AS sample_unique_id,
  c.hugo_symbol AS hugo_gene_symbol,
  toInt32OrZero(c.entrez_gene_id) AS entrez_gene_id,
  ifNull(sp.gene_panel_id, 'WES') AS gene_panel_stable_id,
  c.study_id AS cancer_study_identifier,
  concat(c.study_id, '_', c.profile_stable_id) AS genetic_profile_stable_id,
  'cna' AS variant_type,
  'NA' AS mutation_variant,
  'NA' AS mutation_type,
  '' AS mutation_status,
  c.cbp_driver AS driver_filter,
  c.cbp_driver_annotation AS driver_filter_annotation,
  c.cbp_driver_tiers AS driver_tiers_filter,
  c.cbp_driver_tiers_annotation AS driver_tiers_filter_annotation,
  toInt8OrNull(c.alteration) AS cna_alteration,
  %[3]s AS cna_cytoband,
  '' AS sv_event_info,
  concat(c.study_id, '_', s.patient_id) AS patient_unique_id,
  (ifNull(sp.gene_panel_id, 'WES'), c.hugo_symbol) NOT IN (SELECT gene_panel_id, gene FROM %[2]s.gene_panel_to_gene_derived) AS off_panel
FROM %[2]s.cna c
LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM %[1]s.clinical_sample) s ON c.study_id = s.study_id AND c.sample_id = s.sample_id
LEFT JOIN %[2]s.sample_profile sp ON c.study_id = sp.study_id AND c.sample_id = sp.sample_id AND c.profile_stable_id = sp.profile_stable_id%[4]s
WHERE c.alteration IN ('-2', '2')`, rawDB, target, cytoband, cytobandJoin)

	// Structural variant rows, one per resolved site gene (site2 only when it
	// differs from site1), as prod's two INNER JOINs to gene. sv_event_info
	// carries event_info; mutation_status reuses sv_status as prod does.
	sv := func(site, where string) string {
		return fmt.Sprintf(`SELECT
  concat(v.study_id, '_', v.sample_id) AS sample_unique_id,
  v.%[3]s_hugo_symbol AS hugo_gene_symbol,
  toInt32(assumeNotNull(v.%[3]s_entrez_gene_id)) AS entrez_gene_id,
  ifNull(sp.gene_panel_id, 'WES') AS gene_panel_stable_id,
  v.study_id AS cancer_study_identifier,
  concat(v.study_id, '_', v.profile_stable_id) AS genetic_profile_stable_id,
  'structural_variant' AS variant_type,
  'NA' AS mutation_variant,
  'NA' AS mutation_type,
  v.sv_status AS mutation_status,
  v.cbp_driver AS driver_filter,
  v.cbp_driver_annotation AS driver_filter_annotation,
  v.cbp_driver_tiers AS driver_tiers_filter,
  v.cbp_driver_tiers_annotation AS driver_tiers_filter_annotation,
  CAST(NULL AS Nullable(Int8)) AS cna_alteration,
  '' AS cna_cytoband,
  v.event_info AS sv_event_info,
  concat(v.study_id, '_', s.patient_id) AS patient_unique_id,
  (ifNull(sp.gene_panel_id, 'WES'), v.%[3]s_hugo_symbol) NOT IN (SELECT gene_panel_id, gene FROM %[2]s.gene_panel_to_gene_derived) AS off_panel
FROM %[2]s.structural_variant v
LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM %[1]s.clinical_sample) s ON v.study_id = s.study_id AND v.sample_id = s.sample_id
LEFT JOIN %[2]s.sample_profile sp ON v.study_id = sp.study_id AND v.sample_id = sp.sample_id AND v.profile_stable_id = sp.profile_stable_id
WHERE %[4]s`, rawDB, target, site, where)
	}

	return mutation + "\nUNION ALL\n" + cna + "\nUNION ALL\n" +
		sv("site1", "v.site1_entrez_gene_id IS NOT NULL") + "\nUNION ALL\n" +
		sv("site2", "v.site2_entrez_gene_id IS NOT NULL AND (v.site1_entrez_gene_id IS NULL OR v.site2_entrez_gene_id != v.site1_entrez_gene_id)")
}
