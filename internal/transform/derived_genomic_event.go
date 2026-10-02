package transform

import (
	"fmt"
	"strings"
)

// genomicEventDerivedSQL builds prod's `genomic_event_derived` table as a view:
// the unified alteration table combining mutations, discrete CNA, and structural
// variants into one row-per-alteration shape. Column names/order match prod's
// clickhouse.sql (genomic_event_derived) so the column-store API can read it
// unchanged. Data comes from our file-staging tables in rawDB; gene symbols are
// resolved to entrez ids against refDB.gene when the raw entrez is missing.
//
// Divergences from prod (which builds from OLTP mirror tables we do not have):
//   - gene_panel_stable_id comes from the target-DB sample_profile view per
//     (sample, profile), 'WES' when the panel is NULL or the pair is absent.
//   - discrete CNA events come from both the long and the wide (DISCRETE
//     datatype) staging tables; like cna_event, only AMP/HOMDEL are events.
//   - cna_cytoband is ” (prod joins reference_genome_gene).
func genomicEventDerivedSQL(rawDB, refDB, targetDB string) string {
	// Resolve entrez to Int32: use the raw entrez when positive, else fall back
	// to the reference gene matched by Hugo symbol (aliased g in each SELECT).
	entrez := func(raw string) string {
		return fmt.Sprintf(
			"toInt32OrZero(if(toInt64OrNull(%s) > 0, %s, toString(ifNull(g.entrez_gene_id, 0))))",
			raw, raw)
	}
	// A custom-driver value is kept as given unless empty or NA, which the
	// importer does not store (alteration_driver_annotation).
	drv := func(col string) string {
		return fmt.Sprintf("if(lower(trimBoth(%[1]s)) IN ('', 'na'), '', %[1]s)", col)
	}

	// Mutation rows. sample id = tumor_sample_barcode; mutation filter mirrors the
	// `mutation` transform view (cBioPortal default MutationFilter).
	mutation := fmt.Sprintf(`SELECT
  concat(m.study_id, '_', m.tumor_sample_barcode) AS sample_unique_id,
  m.hugo_symbol AS hugo_gene_symbol,
  %[3]s AS entrez_gene_id,
  ifNull(sp.gene_panel_id, 'WES') AS gene_panel_stable_id,
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
  (ifNull(sp.gene_panel_id, 'WES'), m.hugo_symbol) NOT IN (SELECT gene_panel_id, gene FROM %[6]s.gene_panel_to_gene_derived) AS off_panel
FROM %[1]s.mutation_extended m
LEFT JOIN %[2]s.gene g ON upper(m.hugo_symbol) = upper(g.hugo_gene_symbol)
LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM %[1]s.clinical_sample) s ON m.study_id = s.study_id AND m.tumor_sample_barcode = s.sample_id
LEFT JOIN %[6]s.sample_profile sp ON m.study_id = sp.study_id AND m.tumor_sample_barcode = sp.sample_id AND m.profile_stable_id = sp.profile_stable_id
WHERE %[7]s`,
		rawDB, refDB, entrez("m.entrez_gene_id"), drv("m.cbp_driver"), drv("m.cbp_driver_tiers"), "`"+targetDB+"`", mutationFilterSQL("m"))

	// Discrete CNA rows. cna_alteration = the discrete level (-2..2); restricted to
	// real alterations.
	cna := fmt.Sprintf(`SELECT
  concat(c.study_id, '_', c.sample_id) AS sample_unique_id,
  c.hugo_symbol AS hugo_gene_symbol,
  %[3]s AS entrez_gene_id,
  ifNull(sp.gene_panel_id, 'WES') AS gene_panel_stable_id,
  c.study_id AS cancer_study_identifier,
  concat(c.study_id, '_', c.profile_stable_id) AS genetic_profile_stable_id,
  'cna' AS variant_type,
  'NA' AS mutation_variant,
  'NA' AS mutation_type,
  '' AS mutation_status,
  %[4]s AS driver_filter,
  c.cbp_driver_annotation AS driver_filter_annotation,
  %[5]s AS driver_tiers_filter,
  c.cbp_driver_tiers_annotation AS driver_tiers_filter_annotation,
  toInt8OrNull(c.value) AS cna_alteration,
  '' AS cna_cytoband,
  '' AS sv_event_info,
  concat(c.study_id, '_', s.patient_id) AS patient_unique_id,
  (ifNull(sp.gene_panel_id, 'WES'), c.hugo_symbol) NOT IN (SELECT gene_panel_id, gene FROM %[6]s.gene_panel_to_gene_derived) AS off_panel
FROM %[1]s.cna_discrete_long c
LEFT JOIN %[2]s.gene g ON upper(c.hugo_symbol) = upper(g.hugo_gene_symbol)
LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM %[1]s.clinical_sample) s ON c.study_id = s.study_id AND c.sample_id = s.sample_id
LEFT JOIN %[6]s.sample_profile sp ON c.study_id = sp.study_id AND c.sample_id = sp.sample_id AND c.profile_stable_id = sp.profile_stable_id
WHERE c.value IN ('-2', '2')`,
		rawDB, refDB, entrez("c.entrez_gene_id"), drv("c.cbp_driver"), drv("c.cbp_driver_tiers"), "`"+targetDB+"`")

	// Structural variant rows keyed on site1 (breakpoints without a gene are
	// skipped, as prod's inner join to gene does). sv_event_info carries event_info;
	// mutation_status reuses sv_status as prod does.
	sv := fmt.Sprintf(`SELECT
  concat(v.study_id, '_', v.sample_id) AS sample_unique_id,
  v.site1_hugo_symbol AS hugo_gene_symbol,
  %[3]s AS entrez_gene_id,
  ifNull(sp.gene_panel_id, 'WES') AS gene_panel_stable_id,
  v.study_id AS cancer_study_identifier,
  concat(v.study_id, '_', v.profile_stable_id) AS genetic_profile_stable_id,
  'structural_variant' AS variant_type,
  'NA' AS mutation_variant,
  'NA' AS mutation_type,
  v.sv_status AS mutation_status,
  %[4]s AS driver_filter,
  v.cbp_driver_annotation AS driver_filter_annotation,
  %[5]s AS driver_tiers_filter,
  v.cbp_driver_tiers_annotation AS driver_tiers_filter_annotation,
  CAST(NULL AS Nullable(Int8)) AS cna_alteration,
  '' AS cna_cytoband,
  v.event_info AS sv_event_info,
  concat(v.study_id, '_', s.patient_id) AS patient_unique_id,
  (ifNull(sp.gene_panel_id, 'WES'), v.site1_hugo_symbol) NOT IN (SELECT gene_panel_id, gene FROM %[6]s.gene_panel_to_gene_derived) AS off_panel
FROM %[1]s.structural_variant v
LEFT JOIN %[2]s.gene g ON upper(v.site1_hugo_symbol) = upper(g.hugo_gene_symbol)
LEFT JOIN (SELECT DISTINCT study_id, sample_id, patient_id FROM %[1]s.clinical_sample) s ON v.study_id = s.study_id AND v.sample_id = s.sample_id
LEFT JOIN %[6]s.sample_profile sp ON v.study_id = sp.study_id AND v.sample_id = sp.sample_id AND v.profile_stable_id = sp.profile_stable_id`,
		rawDB, refDB, entrez("v.site1_entrez_gene_id"), drv("v.cbp_driver"), drv("v.cbp_driver_tiers"), "`"+targetDB+"`")

	// Wide discrete CNA matrices (one value per gene x sample).
	cnaWide := strings.NewReplacer(
		drv("c.cbp_driver")+" AS driver_filter", "'' AS driver_filter",
		"c.cbp_driver_annotation AS", "'' AS",
		drv("c.cbp_driver_tiers")+" AS driver_tiers_filter", "'' AS driver_tiers_filter",
		"c.cbp_driver_tiers_annotation AS", "'' AS",
	).Replace(strings.Replace(strings.Replace(cna,
		"FROM "+rawDB+".cna_discrete_long c", "FROM "+rawDB+".cna_matrix c", 1),
		"WHERE c.value IN ('-2', '2')",
		"WHERE c.value IN ('-2', '2') AND (c.study_id, c.profile_stable_id) IN "+
			"(SELECT study_id, profile_stable_id FROM "+rawDB+".genetic_profile WHERE datatype = 'DISCRETE')", 1))

	// Structural variant rows for the site2 gene, when it differs from site1.
	sv2 := strings.NewReplacer(
		"v.site1_hugo_symbol AS hugo_gene_symbol", "v.site2_hugo_symbol AS hugo_gene_symbol",
		"upper(v.site1_hugo_symbol) = upper(g.hugo_gene_symbol)", "upper(v.site2_hugo_symbol) = upper(g.hugo_gene_symbol)",
		"v.site1_hugo_symbol) NOT IN", "v.site2_hugo_symbol) NOT IN",
	).Replace(strings.Replace(sv, entrez("v.site1_entrez_gene_id"), entrez("v.site2_entrez_gene_id"), 1)) +
		"\nWHERE v.site2_hugo_symbol != '' AND upper(v.site2_hugo_symbol) != upper(v.site1_hugo_symbol)"

	return mutation + "\nUNION ALL\n" + cna + "\nUNION ALL\n" + cnaWide + "\nUNION ALL\n" + sv + "\nWHERE v.site1_hugo_symbol != ''" + "\nUNION ALL\n" + sv2
}
