package transform

import (
	"fmt"
	"strings"
)

// computedClinicalAttributes adds the sample attributes cbioportal-core derives
// at import time to the clinical_sample view and clinical_attribute_meta:
//   - MUTATION_COUNT: distinct non-germline variants (chr, start, end, ref, alt)
//     of the stored (gene-resolved, filtered) mutations per sample of each
//     mutation profile, 0 for profiled samples without any
//     (DaoMutation.createMutationCountClinicalData). Samples profiled only
//     through the `*_sequenced` case list get none: the importer computes the
//     count before the derived-table build adds them to sample_profile.
//   - FRACTION_GENOME_ALTERED: share of segmented length with |segment mean| >=
//     0.2, rounded to 4 decimals (DaoCopyNumberSegment), as Java's
//     Double.toString writes it ("1.0", "0.8079", "2.0E-4").
//   - SAMPLE_COUNT (patient): the patient's number of samples
//     (DaoPatient.createSampleCountClinicalData).
//
// Computed values replace any staged value of the same attribute and sample.
func computedClinicalAttributes(c *client, rawDB, targetDB string, hasMutations, hasSegments bool) error {
	var computed []string
	if hasMutations {
		computed = append(computed, fmt.Sprintf(
			"SELECT sp.study_id AS study_id, sp.sample_id AS sample_id, 'MUTATION_COUNT' AS attr_id, "+
				"toString(uniqExactIf((m.chromosome, m.start_position, m.end_position, m.reference_allele, m.tumor_seq_allele2), m.study_id != '')) AS attr_value "+
				"FROM `%[2]s`.sample_profile sp "+
				"LEFT JOIN (SELECT m.* FROM `%[1]s`.mutation_extended m%[4]s WHERE %[3]s AND upper(m.mutation_status) != 'GERMLINE') m "+
				"ON m.study_id = sp.study_id AND m.tumor_sample_barcode = sp.sample_id AND m.profile_stable_id = sp.profile_stable_id "+
				"WHERE sp.alteration_type = 'MUTATION_EXTENDED' AND sp.case_list_only = 0 GROUP BY sp.study_id, sp.sample_id",
			rawDB, targetDB, mutationFilterSQL("m", "gk.resolved_entrez_gene_id"),
			geneKeyJoin(targetDB, "mutation_extended", "m", "hugo_symbol", "entrez_gene_id")))
	}
	if hasSegments {
		computed = append(computed, fmt.Sprintf(
			"SELECT study_id, sample_id, attr_id, %s AS attr_value FROM ("+
				"SELECT study_id, id AS sample_id, 'FRACTION_GENOME_ALTERED' AS attr_id, "+
				"round(sumIf(toFloat64OrZero(loc_end) - toFloat64OrZero(loc_start), abs(toFloat64OrZero(seg_mean)) >= 0.2) / "+
				"sum(toFloat64OrZero(loc_end) - toFloat64OrZero(loc_start)), 4) AS fga "+
				"FROM `%s`.cna_seg GROUP BY study_id, id HAVING sum(toFloat64OrZero(loc_end) - toFloat64OrZero(loc_start)) > 0)",
			javaDoubleString4("fga"), rawDB))
	}
	if len(computed) == 0 {
		return nil
	}
	// Materialized: the mutation count scans every mutation, and the clinical
	// views are read on every study-view query.
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE OR REPLACE TABLE `%s`.`clinical_sample_computed` ENGINE = MergeTree ORDER BY (study_id, sample_id, attr_id) AS %s",
		targetDB, strings.Join(computed, " UNION ALL "))); err != nil {
		return fmt.Errorf("computed clinical attributes: %w", err)
	}
	union := fmt.Sprintf("SELECT study_id, sample_id, attr_id, attr_value FROM `%s`.clinical_sample_computed", targetDB)
	base, err := clinicalLongSQL(c, rawDB, "clinical_sample", "sample_id")
	if err != nil {
		return err
	}
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE OR REPLACE VIEW `%[1]s`.`clinical_sample` AS "+
			"SELECT * FROM (%[2]s) b WHERE (b.study_id, b.sample_id, b.attr_id) NOT IN (SELECT study_id, sample_id, attr_id FROM (%[3]s)) "+
			"UNION ALL %[3]s",
		targetDB, base, union)); err != nil {
		return fmt.Errorf("computed clinical attributes: %w", err)
	}
	if err := samplesPerPatient(c, rawDB, targetDB); err != nil {
		return err
	}
	var meta []string
	if hasMutations {
		meta = append(meta, fmt.Sprintf(
			"SELECT DISTINCT study_id, 'MUTATION_COUNT' AS clinical_attribute_id, 'Mutation Count' AS display_name, 'Mutation Count' AS description, 'NUMBER' AS datatype, '30' AS priority, 0 AS patient_attribute "+
				"FROM `%s`.sample_profile WHERE alteration_type = 'MUTATION_EXTENDED'", targetDB))
	}
	if hasSegments {
		meta = append(meta, fmt.Sprintf(
			"SELECT DISTINCT study_id, 'FRACTION_GENOME_ALTERED' AS clinical_attribute_id, 'Fraction Genome Altered' AS display_name, 'Fraction Genome Altered' AS description, 'NUMBER' AS datatype, '20' AS priority, 0 AS patient_attribute "+
				"FROM `%s`.cna_seg", rawDB))
	}
	meta = append(meta, fmt.Sprintf(
		"SELECT DISTINCT study_id, 'SAMPLE_COUNT' AS clinical_attribute_id, 'Number of Samples Per Patient' AS display_name, "+
			"'Number of Samples Per Patient' AS description, 'STRING' AS datatype, '1' AS priority, 1 AS patient_attribute "+
			"FROM `%s`.clinical_sample", rawDB))
	staged := fmt.Sprintf(
		"SELECT study_id, attr_id AS clinical_attribute_id, display_name, description, datatype, priority, patient_attribute FROM `%s`.clinical_attribute_meta", rawDB)
	_, err = c.Execute(fmt.Sprintf(
		"CREATE OR REPLACE VIEW `%[1]s`.`clinical_attribute_meta` AS %[2]s UNION ALL "+
			"SELECT * FROM (%[3]s) WHERE (study_id, clinical_attribute_id) NOT IN (SELECT study_id, attr_id FROM `%[4]s`.clinical_attribute_meta)",
		targetDB, staged, strings.Join(meta, " UNION ALL "), rawDB))
	return err
}

// samplesPerPatient adds the SAMPLE_COUNT patient attribute to the
// clinical_patient view, replacing any staged value.
func samplesPerPatient(c *client, rawDB, targetDB string) error {
	computed := fmt.Sprintf(
		"SELECT study_id, patient_id, 'SAMPLE_COUNT' AS attr_id, toString(uniqExact(sample_id)) AS attr_value "+
			"FROM `%s`.clinical_sample WHERE sample_id != '' AND patient_id != '' GROUP BY study_id, patient_id", rawDB)
	base := "SELECT '' AS study_id, '' AS patient_id, '' AS attr_id, '' AS attr_value WHERE 0"
	if ok, _ := c.TableExists("clinical_patient"); ok {
		sel, err := clinicalLongSQL(c, rawDB, "clinical_patient", "patient_id")
		if err != nil {
			return err
		}
		base = sel
	}
	_, err := c.Execute(fmt.Sprintf(
		"CREATE OR REPLACE VIEW `%[1]s`.`clinical_patient` AS "+
			"SELECT * FROM (%[2]s) b WHERE b.attr_id != 'SAMPLE_COUNT' UNION ALL %[3]s",
		targetDB, base, computed))
	return err
}

// javaDoubleString4 writes a value in [0, 1] rounded to 4 decimals as Java's
// Double.toString does: at least one fractional digit, and scientific
// notation below 1e-3.
func javaDoubleString4(x string) string {
	return fmt.Sprintf("multiIf(%[1]s = floor(%[1]s), concat(toString(toInt64(%[1]s)), '.0'), "+
		"%[1]s < 0.001, concat(toString(toInt64(round(%[1]s * 10000))), '.0E-4'), toString(%[1]s))", x)
}
