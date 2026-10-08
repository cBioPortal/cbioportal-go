package transform

import (
	"fmt"
	"strings"
)

// mutationMergeSQL selects the `mutation_merge` table: the MAF records that
// ImportExtendedMutationData merges because they describe the same mutation
// of a sample in a profile (same gene, chromosome, positions, protein
// change, tumor allele and mutation type), as multi-center TCGA MAFs list
// one mutation once per sequencing center. The importer keeps one record
// per mutation (mergeMutationData, applied in file order to the record kept
// so far and the next one):
//
//   - when the matched normal barcodes differ, the next record if its
//     normal is a blood normal (TCGA-..-....-10...);
//   - else when the validation statuses differ, the next record if it is
//     Valid or VALIDATED;
//   - else when the mutation statuses differ, the next record if it is
//     Germline, or Somatic while the kept one is not Germline;
//
// and joins the records' centers (sorted, ";"-separated, NA left out when
// there are others) whenever the next record adds a center.
//
// Per merged record (study_id, profile_stable_id, file_num, row_num): keep
// is 1 for the record kept, which takes center, and 0 for the others.
// Records are identified by their position in the staged file, so MAFs
// staged without row numbers are not merged.
func mutationMergeSQL(c *client, rawDB, targetDB string) string {
	var parts []string
	for _, table := range []string{"mutation_extended", "mutation_uncalled"} {
		has, _ := c.TableColumns(table)
		if !has["row_num"] {
			continue
		}
		part := func(col string) string { return mafPartSQL("m." + col) }
		center, normal := "'NA'", "'NA'"
		if has["center"] {
			center = part("center")
		}
		if has["matched_norm_sample_barcode"] {
			normal = part("matched_norm_sample_barcode")
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT m.study_id AS study_id, m.profile_stable_id AS profile_stable_id, m.file_num AS file_num, m.row_num AS row_num, "+
				"tuple(m.tumor_sample_barcode, gk.resolved_entrez_gene_id, %[1]s, %[2]s, %[3]s, upper(%[4]s), upper(%[5]s), upper(m.variant_classification)) AS mkey, "+
				"%[6]s AS normal, %[7]s AS vs, %[8]s AS ms, %[9]s AS center "+
				"FROM `%[10]s`.%[11]s m%[12]s WHERE %[13]s",
			normalizedChrSQL("m.chromosome"), mafPositionSQL("m.start_position"), mafPositionSQL("m.end_position"),
			proteinChangeSQL(has), tumorSeqAlleleSQL("m"), normal, part("validation_status"), part("mutation_status"), center,
			rawDB, table, geneKeyJoin(targetDB, table, "m", "hugo_symbol", "entrez_gene_id"),
			acceptedMutationSQL("m", "gk.resolved_entrez_gene_id", targetDB)))
	}
	if len(parts) == 0 {
		return "SELECT '' AS study_id, '' AS profile_stable_id, toUInt32(0) AS file_num, toUInt64(0) AS row_num, " +
			"toUInt8(0) AS keep, '' AS center WHERE 0"
	}
	// The record kept so far and the next one, as tuples (file_num, row_num,
	// normal, validation status, mutation status, center).
	const (
		kept = "acc"
		next = "x"
	)
	f := func(t string, i int) string { return fmt.Sprintf("tupleElement(%s, %d)", t, i) }
	takeNext := fmt.Sprintf(
		"multiIf(lower(%[1]s) != lower(%[2]s), match(%[2]s, '^TCGA-..-....-10.*$'), "+
			"lower(%[3]s) != lower(%[4]s), lower(%[4]s) IN ('valid', 'validated'), "+
			"lower(%[5]s) != lower(%[6]s), lower(%[6]s) = 'germline' OR (lower(%[6]s) = 'somatic' AND lower(%[5]s) != 'germline'), 0)",
		f(kept, 3), f(next, 3), f(kept, 4), f(next, 4), f(kept, 5), f(next, 5))
	// Java's TreeSet of the centers split on ';' (trailing empty parts dropped).
	centers := func(c string) string {
		return fmt.Sprintf("arraySort(arrayDistinct(arraySlice(splitByChar(';', %[1]s), 1, arrayLastIndex(y -> y != '', splitByChar(';', %[1]s)))))", c)
	}
	union := fmt.Sprintf("arraySort(arrayDistinct(arrayConcat(%s, %s)))", centers(f(kept, 6)), centers(f(next, 6)))
	merged := fmt.Sprintf("if(length(%[1]s) > length(%[2]s), arrayStringConcat(if(length(%[1]s) > 1, arrayFilter(y -> y != 'NA', %[1]s), %[1]s), ';'), '')",
		union, centers(f(kept, 6)))
	step := fmt.Sprintf(
		"(%[1]s, %[2]s) -> (if(%[3]s, %[4]s, %[5]s), if(%[3]s, %[6]s, %[7]s), if(%[3]s, %[8]s, %[9]s), if(%[3]s, %[10]s, %[11]s), "+
			"if(%[3]s, %[12]s, %[13]s), if(%[14]s != '', %[14]s, if(%[3]s, %[15]s, %[16]s)))",
		kept, next, takeNext, f(next, 1), f(kept, 1), f(next, 2), f(kept, 2), f(next, 3), f(kept, 3), f(next, 4), f(kept, 4),
		f(next, 5), f(kept, 5), merged, f(next, 6), f(kept, 6))
	groups := "SELECT study_id, profile_stable_id, arraySort(r -> (tupleElement(r, 1), tupleElement(r, 2)), " +
		"groupArray((file_num, row_num, normal, vs, ms, center))) AS recs FROM (" + strings.Join(parts, " UNION ALL ") + ") " +
		"GROUP BY study_id, profile_stable_id, mkey HAVING count() > 1"
	folded := fmt.Sprintf("SELECT study_id, profile_stable_id, recs, arrayFold(%s, arraySlice(recs, 2), recs[1]) AS kept FROM (%s)", step, groups)
	return "SELECT study_id, profile_stable_id, tupleElement(r, 1) AS file_num, tupleElement(r, 2) AS row_num, " +
		"toUInt8(tupleElement(r, 1) = tupleElement(kept, 1) AND tupleElement(r, 2) = tupleElement(kept, 2)) AS keep, " +
		"if(keep = 1, tupleElement(kept, 6), '') AS center FROM (" + folded + ") ARRAY JOIN recs AS r"
}
