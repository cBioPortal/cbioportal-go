package transform

import (
	"fmt"
	"strings"
)

// genetic_alteration_derived is prod's generic per-(sample, gene, profile)
// value table covering every non-mutation, non-SV, non-generic-assay
// molecular profile (copy number, expression, methylation and protein
// levels). Here it is a view over three tables, so a build does not write
// and sort one row per cell (billions on all of datahub):
//
//   - alterationLines holds a matrix staged as lines (ingest) one row per
//     gene and profile, with the line's values in its file's sample order,
//     sorted by study, profile and gene, so a read of a few genes or of one
//     profile reads only their rows;
//   - alterationSamples holds each such file's samples, the portal's copy
//     of the raw matrix_samples;
//   - alterationCells holds the rows of the other sources (long CNA files
//     and matrices staged cell by cell) as prod's table does.
//
// Each line's gene is resolved through gene_key_map as the importers store
// it (unresolved and duplicate-gene lines are not stored, an RPPA line is
// stored under each of its genes), so hugo_gene_symbol is the reference
// symbol. profile_type matches prod's INSERT semantics: the profile stable
// id with the study prefix stripped (e.g. `gistic`, `log2CNA`, `mrna`),
// which in our staging is already the bare `profile_stable_id` column.
// alteration_value drops NA and empty just as prod does (prod maps empty to
// NULL then filters out NA).
const (
	alterationLines   = "genetic_alteration_lines"
	alterationSamples = "genetic_alteration_samples"
	alterationCells   = "genetic_alteration_cells"
)

// alterationMatrices are the per-gene matrices of genetic_alteration_derived.
var alterationMatrices = []string{"cna_matrix", "methylation_matrix", "mrna_expression_matrix", "protein_level_matrix"}

// alterationLinesSQL selects the rows of alterationLines: each line of the
// matrices staged as lines whose gene is stored. Only a gene's first line
// is kept, as the importer stores it (gene_key_map's first_row).
func alterationLinesSQL(c *client, rawDB, targetDB string) string {
	var parts []string
	for _, m := range alterationMatrices {
		lines := matrixLines(c, m)
		if lines == "" {
			continue
		}
		cols, _ := c.TableColumns(lines)
		col := func(n string) string {
			if cols[n] {
				return "`" + n + "`"
			}
			return "''"
		}
		key := col("hugo_symbol")
		if cols["composite_element_ref"] {
			// RPPA lines are keyed by their Composite.Element.REF value.
			key = fmt.Sprintf("if(composite_element_ref != '', composite_element_ref, %s)", key)
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT l.study_id AS cancer_study_identifier, l.profile_stable_id AS profile_type, "+
				"gk.resolved_hugo_gene_symbol AS hugo_gene_symbol, '%[5]s' AS stage_table, l.source_file AS source_file, l.values AS values "+
				"FROM (SELECT study_id, source_file, profile_stable_id, %[1]s AS gene_key, %[2]s AS entrez_gene_id, `values`, row_base "+
				"FROM `%[3]s`.`%[4]s` WHERE cells > 0) l%[6]s"+
				" WHERE gk.first_row = 0 OR gk.first_row = l.row_base + 1",
			key, col("entrez_gene_id"), rawDB, lines, m, geneKeyJoin(targetDB, m, "l", "gene_key", "entrez_gene_id")))
	}
	if len(parts) == 0 {
		return "SELECT '' AS cancer_study_identifier, '' AS profile_type, '' AS hugo_gene_symbol, '' AS stage_table, " +
			"'' AS source_file, CAST([] AS Array(String)) AS values WHERE 0"
	}
	return strings.Join(parts, " UNION ALL ")
}

// alterationSamplesSQL selects the rows of alterationSamples: the samples
// of each file of the matrices staged as lines.
func alterationSamplesSQL(c *client, rawDB string) string {
	var tables []string
	for _, m := range alterationMatrices {
		if matrixLines(c, m) != "" {
			tables = append(tables, "'"+m+"'")
		}
	}
	if len(tables) == 0 {
		return "SELECT '' AS study_id, '' AS stage_table, '' AS source_file, CAST([] AS Array(String)) AS samples WHERE 0"
	}
	return fmt.Sprintf("SELECT study_id, stage_table, source_file, samples FROM `%s`.matrix_samples WHERE stage_table IN (%s)",
		rawDB, strings.Join(tables, ", "))
}

// alterationCellsSQL selects the rows of alterationCells: those of the long
// CNA files and of the matrices staged cell by cell.
func alterationCellsSQL(c *client, rawDB, targetDB string) string {
	part := func(table, from string) string {
		return fmt.Sprintf(
			"SELECT concat(t.study_id, '_', t.sample_id) AS sample_unique_id, "+
				"t.study_id AS cancer_study_identifier, "+
				"gk.resolved_hugo_gene_symbol AS hugo_gene_symbol, "+
				"t.profile_stable_id AS profile_type, "+
				"nullIf(t.value, '') AS alteration_value "+
				"FROM %s t%s "+
				"WHERE t.value NOT IN ('NA', '') AND t.sample_id != ''",
			from, geneKeyJoin(targetDB, table, "t", "gene_key", "entrez_gene_id"))
	}
	table := func(name string) string {
		return fmt.Sprintf("(SELECT *, hugo_symbol AS gene_key FROM `%s`.%s)", rawDB, name)
	}
	parts := []string{part("cna_discrete_long", table("cna_discrete_long"))}
	for _, m := range alterationMatrices {
		if matrixLines(c, m) != "" {
			continue
		}
		from := table(m)
		if m == "protein_level_matrix" {
			// RPPA lines are keyed by their Composite.Element.REF value.
			from = fmt.Sprintf("(SELECT *, if(composite_element_ref != '', composite_element_ref, hugo_symbol) AS gene_key "+
				"FROM `%s`.protein_level_matrix)", rawDB)
		}
		parts = append(parts, part(m, from))
	}
	return strings.Join(parts, " UNION ALL ")
}

// geneticAlterationDerivedViewSQL is the definition of the
// genetic_alteration_derived view: alterationLines unpacked into one row per
// sample with a value, and alterationCells.
func geneticAlterationDerivedViewSQL(targetDB string) string {
	return fmt.Sprintf(
		"SELECT concat(t.cancer_study_identifier, '_', cell.2) AS sample_unique_id, t.cancer_study_identifier AS cancer_study_identifier, "+
			"t.hugo_gene_symbol AS hugo_gene_symbol, t.profile_type AS profile_type, nullIf(cell.1, '') AS alteration_value "+
			"FROM (SELECT a.cancer_study_identifier AS cancer_study_identifier, a.hugo_gene_symbol AS hugo_gene_symbol, a.profile_type AS profile_type, "+
			"arrayZip(arrayResize(a.values, length(s.samples), ''), s.samples) AS cells "+
			"FROM `%[1]s`.`%[2]s` a INNER JOIN `%[1]s`.`%[3]s` s "+
			"ON s.study_id = a.cancer_study_identifier AND s.source_file = a.source_file AND s.stage_table = a.stage_table) t "+
			"ARRAY JOIN t.cells AS cell WHERE cell.1 NOT IN ('NA', '') AND cell.2 != '' "+
			"UNION ALL SELECT sample_unique_id, cancer_study_identifier, hugo_gene_symbol, profile_type, alteration_value FROM `%[1]s`.`%[4]s`",
		targetDB, alterationLines, alterationSamples, alterationCells)
}

// genericAssayDataDerivedSQL builds prod's `generic_assay_data_derived` from the
// generic_assay_matrix staging table. profile_stable_id is the FULL molecular
// profile id (`<study>_<suffix>`, from genetic_profile.molecular_profile_id) while
// profile_type is the bare suffix, matching prod. genetic_entity_id is
// synthesized deterministically from entity_stable_id since we have no genetic
// entity table. The matrix columns of a patient-level profile are patient ids:
// as ImportGenericAssayPatientLevelData stores the patient's value for each of
// its samples, those rows are expanded to every sample of the patient. For a
// sample-level profile the LEFT JOIN keeps rows whose sample id is not in
// clinical data (prod's INNER JOIN would drop them). hasProfileMeta says the
// staging genetic_profile has the generic_assay_type and patient_level columns.
// cancer_study_identifier, which prod's table lacks, keeps each study's
// rows in a partition of their own.
func genericAssayDataDerivedSQL(rawDB, refDB, targetDB string, hasProfileMeta bool) string {
	_ = refDB
	gaType, patientLevel := "''", "toUInt8(0)"
	if hasProfileMeta {
		gaType = "ifNull(gp.generic_assay_type, '')"
		patientLevel = "toUInt8(lower(toString(ifNull(gp.patient_level, ''))) IN ('true', '1'))"
	}
	inner := fmt.Sprintf("SELECT gam.study_id AS study_id, gam.sample_id AS case_id, gam.entity_stable_id AS entity_stable_id, "+
		"gam.value AS value, gam.profile_stable_id AS profile_type, gp.molecular_profile_id AS profile_stable_id, "+
		"gp.datatype AS datatype, %s AS generic_assay_type, %s AS patient_level "+
		"FROM `%s`.generic_assay_matrix gam "+
		"LEFT JOIN `%s`.genetic_profile gp "+
		"ON gp.study_id = gam.study_id AND gp.profile_stable_id = gam.profile_stable_id "+
		"WHERE gam.sample_id != ''", gaType, patientLevel, rawDB, rawDB)
	cols := func(sampleCol, patientCol string) string {
		return fmt.Sprintf("SELECT concat(m.study_id, '_', %s) AS sample_unique_id, "+
			"concat(m.study_id, '_', %s) AS patient_unique_id, "+
			"toString(cityHash64(m.entity_stable_id)) AS genetic_entity_id, "+
			"m.value AS value, m.generic_assay_type AS generic_assay_type, m.profile_stable_id AS profile_stable_id, "+
			"m.entity_stable_id AS entity_stable_id, m.datatype AS datatype, m.patient_level AS patient_level, "+
			"m.profile_type AS profile_type, m.study_id AS cancer_study_identifier ", sampleCol, patientCol)
	}
	return inTarget(targetDB, cols("m.case_id", "s.patient_id")+
		"FROM ("+inner+") m LEFT JOIN {{target}}.sample s ON s.study_id = m.study_id AND s.sample_id = m.case_id "+
		"WHERE m.patient_level = 0 "+
		"UNION ALL "+cols("s.sample_id", "m.case_id")+
		"FROM ("+inner+") m JOIN {{target}}.sample s ON s.study_id = m.study_id AND s.patient_id = m.case_id "+
		"WHERE m.patient_level = 1")
}
