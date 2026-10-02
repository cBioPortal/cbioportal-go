package transform

import (
	"fmt"
	"strings"
)

// view is a named view definition.
type view struct {
	name string
	sql  string
}

// studyExtraViews builds the views for the study-level side data: resource
// definitions and resource data (study/patient/sample), GISTIC regions and
// their genes, MutSig genes, and study tags. Every view is always created —
// over an empty, correctly-typed SELECT when the study files were never
// ingested — so the API can query them unconditionally.
func studyExtraViews(c *client, rawDB, targetDB, refDB string, hasRef bool, exists func(string) bool) []view {
	trim := func(col string) string { return "trimBoth(`" + col + "`)" }
	var out []view

	// resource_definition: cbioportal-core ImportResourceDefinition — ids are
	// upper-cased, values trimmed, optional columns defaulted (description '',
	// open_by_default false, priority 1, custom_metadata NULL), and the first
	// definition of a duplicated id wins.
	rdEmpty := "SELECT '' AS study_id, '' AS resource_id, '' AS display_name, '' AS description, '' AS resource_type, " +
		"toUInt8(0) AS open_by_default, toInt32(1) AS priority, CAST(NULL AS Nullable(String)) AS custom_metadata WHERE 0"
	rdSQL := rdEmpty
	if cols, _ := c.TableColumns("resource_definition"); cols["resource_id"] && cols["display_name"] && cols["resource_type"] {
		desc, open, prio, meta := "''", "toUInt8(0)", "toInt32(1)", "CAST(NULL AS Nullable(String))"
		if cols["description"] {
			desc = fmt.Sprintf("if(%s, '', %s)", missingClinicalValue(trim("description")), trim("description"))
		}
		if cols["open_by_default"] {
			open = "lower(" + trim("open_by_default") + ") = 'true'"
		}
		if cols["priority"] {
			prio = "toInt32OrDefault(" + trim("priority") + ", toInt32(1))"
		}
		if cols["custom_metadata"] {
			meta = fmt.Sprintf("if(%s, CAST(NULL AS Nullable(String)), CAST(%s AS Nullable(String)))",
				missingClinicalValue(trim("custom_metadata")), trim("custom_metadata"))
		}
		rdSQL = fmt.Sprintf(
			"SELECT study_id, upper(%s) AS resource_id, %s AS display_name, %s AS description, %s AS resource_type, "+
				"%s AS open_by_default, %s AS priority, %s AS custom_metadata FROM `%s`.resource_definition "+
				"WHERE NOT %s AND %s IN ('SAMPLE', 'PATIENT', 'STUDY') "+
				"ORDER BY study_id, source_file LIMIT 1 BY study_id, resource_id",
			trim("resource_id"), trim("display_name"), desc, trim("resource_type"), open, prio, meta, rawDB,
			missingClinicalValue(trim("resource_id")), trim("resource_type"))
	}
	out = append(out, view{"resource_definition", rdSQL})

	// resource_{study,patient,sample}: cbioportal-core ImportResourceData —
	// rows with a missing resource id or URL are skipped, each row must refer
	// to a definition of the matching resource type, and exact duplicates
	// collapse to one.
	resource := func(table, kind string, idCols []string) view {
		sel := []string{"'' AS study_id"}
		for _, id := range idCols {
			sel = append(sel, "'' AS "+id)
		}
		sel = append(sel, "'' AS resource_id", "'' AS url")
		empty := "SELECT " + strings.Join(sel, ", ") + " WHERE 0"
		cols, _ := c.TableColumns(table)
		need := append([]string{"resource_id", "url"}, idCols...)
		for _, n := range need {
			if !cols[n] {
				return view{table, empty}
			}
		}
		sel = []string{"study_id"}
		where := []string{
			"NOT " + missingClinicalValue(trim("resource_id")),
			"NOT " + missingClinicalValue(trim("url")),
		}
		for _, id := range idCols {
			sel = append(sel, trim(id)+" AS "+id)
			where = append(where, trim(id)+" != ''")
		}
		sel = append(sel, trim("resource_id")+" AS resource_id", trim("url")+" AS url")
		where = append(where, fmt.Sprintf(
			"(study_id, %s) IN (SELECT study_id, resource_id FROM `%s`.resource_definition WHERE resource_type = '%s')",
			trim("resource_id"), targetDB, kind))
		return view{table, fmt.Sprintf("SELECT DISTINCT %s FROM `%s`.`%s` WHERE %s",
			strings.Join(sel, ", "), rawDB, table, strings.Join(where, " AND "))}
	}
	out = append(out,
		resource("resource_study", "STUDY", nil),
		resource("resource_patient", "PATIENT", []string{"patient_id"}),
		resource("resource_sample", "SAMPLE", []string{"patient_id", "sample_id"}))

	// gistic_to_gene / gistic: cbioportal-core GisticReader — one region per
	// row (wide peak = peak_start..peak_end, X/Y = 23/24, amp = 1), genes from
	// genes_in_region ('[', ']' dropped, comma separated, text before '|'),
	// resolved non-ambiguously (Entrez id, Hugo symbol, then unique alias),
	// miRNAs and duplicates dropped; regions with no resolved gene are skipped.
	g2gEmpty := "SELECT '' AS study_id, toUInt64(0) AS gistic_roi_id, toUInt64(0) AS gene_index, toInt64(0) AS entrez_gene_id, '' AS hugo_gene_symbol WHERE 0"
	gEmpty := "SELECT '' AS study_id, toUInt64(0) AS gistic_roi_id, toInt32(0) AS chromosome, '' AS cytoband, toInt64(0) AS wide_peak_start, " +
		"toInt64(0) AS wide_peak_end, toFloat64(0) AS q_value, '' AS q_value_text, toUInt8(0) AS amp WHERE 0"
	g2gSQL, gSQL := g2gEmpty, gEmpty
	if cols, _ := c.TableColumns("gistic"); hasRef && cols["chromosome"] && cols["peak_start"] && cols["peak_end"] &&
		cols["genes_in_region"] && cols["q_value"] && cols["cytoband"] && cols["amp"] {
		roi := "cityHash64(study_id, source_file, chromosome, peak_start, peak_end, cytoband, q_value, amp, genes_in_region)"
		tokens := fmt.Sprintf(
			"SELECT study_id, gistic_roi_id, gene_index, sym FROM ("+
				"SELECT study_id, %s AS gistic_roi_id, "+
				"arrayFilter(x -> x != '', arrayMap(x -> trimBoth(splitByChar('|', x)[1]), splitByChar(',', replaceRegexpAll(genes_in_region, '[\\\\[\\\\]]', '')))) AS syms "+
				"FROM `%s`.gistic) ARRAY JOIN syms AS sym, arrayEnumerate(syms) AS gene_index", roi, rawDB)
		g2gSQL = fmt.Sprintf(
			"SELECT study_id, gistic_roi_id, gene_index, entrez_gene_id, hugo_gene_symbol FROM (%s) "+
				"WHERE gene_type != 'miRNA' ORDER BY study_id, gistic_roi_id, gene_index LIMIT 1 BY study_id, gistic_roi_id, entrez_gene_id",
			resolveGenesSQL(refDB, tokens))
		// The region id is hashed in a subquery: the outer aliases shadow the
		// raw column names.
		chrom := "replaceRegexpOne(trimBoth(g.chromosome), '^(?i)chr', '')"
		gSQL = fmt.Sprintf(
			"SELECT g.study_id AS study_id, g.roi AS gistic_roi_id, "+
				"toInt32(multiIf(upper(%s) = 'X', 23, upper(%s) = 'Y', 24, toInt32OrZero(%s))) AS chromosome, "+
				"trimBoth(g.cytoband) AS cytoband, toInt64OrZero(trimBoth(g.peak_start)) AS wide_peak_start, "+
				"toInt64OrZero(trimBoth(g.peak_end)) AS wide_peak_end, toFloat64OrZero(trimBoth(g.q_value)) AS q_value, "+
				"trimBoth(g.q_value) AS q_value_text, "+
				"toInt32OrZero(trimBoth(g.amp)) = 1 AS amp FROM (SELECT *, %s AS roi FROM `%s`.gistic) g "+
				"WHERE (g.study_id, g.roi) IN (SELECT study_id, gistic_roi_id FROM `%s`.gistic_to_gene)",
			chrom, chrom, chrom, roi, rawDB, targetDB)
	}
	// The *_text columns keep the values as written, for exact decimal output
	// (ClickHouse's default float parsing is not correctly rounded).
	out = append(out, view{"gistic_to_gene", g2gSQL}, view{"gistic", gSQL})

	// mutsig: cbioportal-core MutSigReader — rows with q >= 0.1 or a gene that
	// does not resolve non-ambiguously are skipped; a gene keeps its best rank.
	msEmpty := "SELECT '' AS study_id, toInt32(0) AS `rank`, toInt64(0) AS entrez_gene_id, '' AS hugo_gene_symbol, " +
		"toInt32(0) AS num_bases_covered, toInt32(0) AS num_mutations, toFloat64(0) AS p_value, toFloat64(0) AS q_value, " +
		"'' AS p_value_text, '' AS q_value_text WHERE 0"
	msSQL := msEmpty
	if exists("mutsig") && hasRef {
		inner := fmt.Sprintf(
			"SELECT m.study_id AS study_id, toInt32OrZero(m.`rank`) AS `rank`, m.gene AS sym, toInt32OrZero(m.num_bases_covered) AS num_bases_covered, "+
				"toInt32OrZero(m.num_mutations) AS num_mutations, toFloat64OrZero(m.p_value) AS p_value, toFloat64OrZero(m.q_value) AS q_value, "+
				"m.p_value AS p_value_text, m.q_value AS q_value_text "+
				"FROM `%s`.mutsig m WHERE toFloat64OrDefault(m.q_value, 1.0) < 0.1", rawDB)
		msSQL = fmt.Sprintf(
			"SELECT study_id, `rank`, entrez_gene_id, hugo_gene_symbol, num_bases_covered, num_mutations, p_value, q_value, p_value_text, q_value_text FROM (%s) "+
				"ORDER BY study_id, `rank` LIMIT 1 BY study_id, entrez_gene_id",
			resolveGenesSQL(refDB, inner))
	}
	out = append(out, view{"mutsig", msSQL})

	tagsSQL := "SELECT '' AS study_id, '' AS tags WHERE 0"
	if exists("cancer_study_tags") {
		tagsSQL = fmt.Sprintf("SELECT study_id, tags FROM `%s`.cancer_study_tags", rawDB)
	}
	out = append(out, view{"cancer_study_tags", tagsSQL})
	return out
}

// resolveGenesSQL resolves the `sym` column of inner like cbioportal-core's
// DaoGeneOptimized.getNonAmbiguousGene: a numeric value is an Entrez id,
// otherwise a Hugo symbol (case-insensitive), otherwise an alias that maps to
// exactly one gene. Unresolved rows are dropped. The result has every inner
// column plus entrez_gene_id, hugo_gene_symbol and gene_type.
func resolveGenesSQL(refDB, inner string) string {
	return fmt.Sprintf(
		"SELECT r.*, gg.hugo_gene_symbol AS hugo_gene_symbol, gg.type AS gene_type FROM ("+
			"SELECT t.*, multiIf(match(t.sym, '^[0-9]+$'), g2.entrez_gene_id, g1.entrez_gene_id > 0, g1.entrez_gene_id, ga.e) AS entrez_gene_id "+
			"FROM (%[2]s) t "+
			"LEFT JOIN `%[1]s`.gene g1 ON upper(t.sym) = upper(g1.hugo_gene_symbol) "+
			"LEFT JOIN (SELECT upper(gene_alias) AS a, any(entrez_gene_id) AS e FROM `%[1]s`.gene_alias GROUP BY a HAVING uniqExact(entrez_gene_id) = 1) ga ON upper(t.sym) = ga.a "+
			"LEFT JOIN `%[1]s`.gene g2 ON toInt64OrZero(t.sym) = g2.entrez_gene_id"+
			") r INNER JOIN `%[1]s`.gene gg ON r.entrez_gene_id = gg.entrez_gene_id WHERE r.entrez_gene_id > 0",
		refDB, inner)
}
