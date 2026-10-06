package transform

import (
	"fmt"
	"strings"
)

// MafExportColumns are the staged MAF columns a study export writes back,
// besides the resolved gene and the derived protein change.
var MafExportColumns = []string{
	"center", "ncbi_build", "chromosome", "start_position", "end_position", "strand",
	"variant_classification", "variant_type", "reference_allele", "tumor_seq_allele1", "tumor_seq_allele2",
	"dbsnp_rs", "dbsnp_val_status", "tumor_sample_barcode", "matched_norm_sample_barcode",
	"match_norm_seq_allele1", "match_norm_seq_allele2", "tumor_validation_allele1", "tumor_validation_allele2",
	"match_norm_validation_allele1", "match_norm_validation_allele2", "verification_status",
	"validation_status", "mutation_status", "sequencing_phase", "sequence_source", "validation_method",
	"score", "bam_file", "sequencer", "t_alt_count", "t_ref_count", "n_alt_count", "n_ref_count",
}

// MafRecordsSQL selects the records of one MAF profile that the mutation
// importer stores — the same gene resolution (gene_key_map in targetDB) and
// MutationFilter as the `mutation` table — with every MafExportColumns column
// (empty when the staged file lacks it), the reference gene as hugo / entrez,
// the stored protein_change, and the record's position in the staged file
// (ord, StagingOrderSQL). table is the staging table (mutation_extended or
// mutation_uncalled) as referenced in SQL; has lists its columns. The study
// and profile are the query parameters {study:String} and {profile:String}.
func MafRecordsSQL(table, source, targetDB string, has map[string]bool) string {
	cols := make([]string, 0, len(MafExportColumns))
	for _, c := range MafExportColumns {
		if has[c] {
			cols = append(cols, "m."+c+" AS "+c)
		} else {
			cols = append(cols, "'' AS "+c)
		}
	}
	return fmt.Sprintf(
		"SELECT %s, gk.resolved_hugo_gene_symbol AS hugo, gk.resolved_entrez_gene_id AS entrez, %s AS protein_change, "+
			"%s AS ord "+
			"FROM %s m%s WHERE m.study_id = {study:String} AND m.profile_stable_id = {profile:String} AND %s",
		strings.Join(cols, ", "), proteinChangeSQL(has), StagingOrderSQL("m.", has["row_num"], true), table,
		geneKeyJoin(targetDB, source, "m", "hugo_symbol", "entrez_gene_id"),
		mutationFilterSQL("m", "gk.resolved_entrez_gene_id"))
}
