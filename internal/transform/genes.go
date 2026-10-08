package transform

import (
	"fmt"
	"strings"
)

// geneMode selects which cbioportal-core importer's gene lookup a data
// source follows. Every importer stores alterations under the resolved gene's
// Entrez id, so the API reports the reference gene's current Hugo symbol.
type geneMode int

const (
	// geneMatrix is ImportTabDelimData.saveLine (wide CNA, expression,
	// methylation): a valid Entrez id, else the Hugo symbol (text before '|'),
	// else a unique alias. Lines with a non-numeric Entrez id or a '///' / '---'
	// symbol are skipped. When any matched gene (including every gene the
	// symbol is an alias of) is a microRNA, the line is stored under each
	// microRNA gene.
	geneMatrix geneMode = iota
	// geneCnaLong is ImportCnaDiscreteLongData.getGene: Entrez id (digits
	// only), else the Hugo symbol or a unique alias.
	geneCnaLong
	// geneMutation is ImportExtendedMutationData: a given non-zero Entrez id
	// must exist (else the record is skipped); without one, the symbol is
	// resolved non-ambiguously; "Unknown" is intergenic and skipped.
	geneMutation
	// geneSV is ImportStructuralVariantData.setCanonicalGene: the Entrez id
	// when it exists, else the non-ambiguous symbol (unless "NA").
	geneSV
	// genePanel is GenePanelUtil.extractGenes: a numeric entry is an Entrez
	// id, otherwise the Hugo symbol, otherwise the first gene of the alias.
	genePanel
	// geneRPPA is one gene symbol of an RPPA line (ImportTabDelimData
	// parseRPPAGenes): the non-ambiguous gene of the symbol; "NA" is no gene.
	geneRPPA
)

// disambiguatedSymbols is cbioportal-core's gene_symbol_disambiguation.txt:
// the gene an ambiguous alias resolves to in getNonAmbiguousGene.
var disambiguatedSymbols = [][2]string{{"MLL2", "8085"}, {"CDC2", "983"}}

// resolveGeneSQL resolves the `sym` / `ent` columns of keys (any SELECT with
// String columns sym and ent) against the reference genes the way the given
// importer does. Every keys column is kept, plus gene_entrez (Int64),
// gene_hugo (the reference symbol), gene_mirna (1 for a microRNA gene) and
// gene_direct (1 when the symbol is the
// gene's own Hugo symbol or the Entrez id matched). Unresolved keys are
// dropped; a microRNA matrix line yields one row per microRNA gene.
func resolveGeneSQL(refDB string, mode geneMode, keys string) string {
	sym := "k.sym"
	if mode == geneMatrix || mode == geneCnaLong {
		// "deal with multiple symbols separate by |, use the first one"
		sym = "if(position(k.sym, '|') > 1, substring(k.sym, 1, position(k.sym, '|') - 1), k.sym)"
	}
	const (
		eFound = "ge._ge != 0"
		hFound = "gh._he != 0"
		sFound = "gs._ge != 0"
	)
	disamb := "CAST([] AS Array(Int64))"
	for _, d := range disambiguatedSymbols {
		disamb = fmt.Sprintf("if(k.sym = '%s', [toInt64(%s)], %s)", d[0], d[1], disamb)
	}
	// DaoGeneOptimized.getNonAmbiguousGene over the full symbol.
	nonAmbiguous := fmt.Sprintf(
		"multiIf(match(k.sym, '^[0-9]+$') AND %s, [gs._ge], %s, [gh._he], length(ga._ae) = 1, ga._ae, length(ga._ae) > 1, %s, CAST([] AS Array(Int64)))",
		sFound, hFound, disamb)
	none := "CAST([] AS Array(Int64))"
	var cands string
	switch mode {
	case geneMatrix:
		valid := "NOT (k.ent != '' AND NOT match(k.ent, '^[0-9]+$')) AND NOT (k.sym != '' AND (position(k.sym, '///') > 0 OR position(k.sym, '---') > 0)) AND (k.sym != '' OR k.ent != '')"
		parse := fmt.Sprintf("if(k.ent != '' AND %s, [ge._ge], if(k.sym != '', if(%s, [gh._he], ga._ae), %s))", eFound, hFound, none)
		all := fmt.Sprintf("arrayDistinct(arrayConcat(%s, if(k.sym != '' AND k.sym = k._symc, ga._ae, %s)))", parse, none)
		cands = fmt.Sprintf(
			"if(NOT (%[1]s) OR empty(%[2]s), %[4]s, if(notEmpty(arrayFilter(x -> has(mir._mirs, x), %[3]s)), arrayFilter(x -> has(mir._mirs, x), %[3]s), if(length(%[2]s) = 1, %[2]s, %[4]s)))",
			valid, parse, all, none)
	case geneCnaLong:
		cands = fmt.Sprintf(
			"if(match(k.ent, '^[0-9]+$') AND %s, [ge._ge], if(k.sym != '' AND position(k.sym, '///') = 0 AND position(k.sym, '---') = 0, if(%s, [gh._he], if(length(ga._ae) = 1, ga._ae, %s)), %s))",
			eFound, hFound, none, none)
	case geneMutation:
		cands = fmt.Sprintf(
			"if(k.ent != '' AND k.ent != '0', if(match(k.ent, '^\\\\+?[0-9]+$') AND %s, [ge._ge], %s), if(k.sym = '' OR k.sym = 'Unknown', %s, %s))",
			eFound, none, none, nonAmbiguous)
	case geneSV:
		cands = fmt.Sprintf(
			"if(match(k.ent, '^[+-]?[0-9]+$') AND %s, [ge._ge], if(k.sym = '' OR k.sym = 'NA', %s, %s))",
			eFound, none, nonAmbiguous)
	case genePanel:
		cands = fmt.Sprintf(
			"if(match(k.sym, '^[+-]?[0-9]+$'), if(%s, [gs._ge], %s), if(%s, [gh._he], if(notEmpty(ga._ae), [ga._ae[1]], %s)))",
			sFound, none, hFound, none)
	case geneRPPA:
		cands = fmt.Sprintf("if(k.sym = '' OR upper(k.sym) = 'NA', %s, %s)", none, nonAmbiguous)
	}
	mirJoin := ""
	if mode == geneMatrix {
		mirJoin = fmt.Sprintf(" CROSS JOIN (SELECT groupArray(entrez_gene_id) AS _mirs FROM `%s`.gene WHERE type = 'miRNA') mir", refDB)
	}
	return fmt.Sprintf(
		"SELECT x.* EXCEPT (_symc, _symu, _entn, _symn), gg.hugo_gene_symbol AS gene_hugo, "+
			"toUInt8(x._entn = x.gene_entrez OR upper(gg.hugo_gene_symbol) = x._symu) AS gene_direct, toUInt8(gg.type = 'miRNA') AS gene_mirna FROM ("+
			"SELECT k.*, arrayJoin(%[2]s) AS gene_entrez FROM ("+
			"SELECT kk.*, %[3]s AS _symc, upper(_symc) AS _symu, toInt64OrZero(kk.ent) AS _entn, toInt64OrZero(kk.sym) AS _symn FROM (%[4]s) kk) k "+
			"LEFT JOIN (SELECT entrez_gene_id AS _ge FROM `%[1]s`.gene) ge ON k._entn = ge._ge "+
			"LEFT JOIN (SELECT entrez_gene_id AS _ge FROM `%[1]s`.gene) gs ON k._symn = gs._ge "+
			"LEFT JOIN (SELECT upper(hugo_gene_symbol) AS _hs, any(entrez_gene_id) AS _he FROM `%[1]s`.gene GROUP BY _hs) gh ON k._symu = gh._hs "+
			"LEFT JOIN (SELECT upper(gene_alias) AS _as, arraySort(groupUniqArray(entrez_gene_id)) AS _ae FROM `%[1]s`.gene_alias GROUP BY _as) ga ON k._symu = ga._as"+
			"%[5]s) x INNER JOIN `%[1]s`.gene gg ON gg.entrez_gene_id = x.gene_entrez",
		refDB, cands, strings.ReplaceAll(sym, "k.sym", "kk.sym"), keys, mirJoin)
}

// geneKeySource is one staging table whose gene columns are resolved into
// gene_key_map.
type geneKeySource struct {
	table  string
	mode   geneMode
	symCol string
	entCol string
	where  string // keeps only these rows of the table, if set
}

// buildGeneKeyMap materializes `gene_key_map`: for every distinct gene key
// (study_id, profile_stable_id, raw hugo_symbol, raw entrez_gene_id) of each
// gene-keyed staging table, the reference gene it is stored under, following
// that table's importer (source = staging table name; structural variants use
// source 'structural_variant' for both sites). Unresolvable keys are absent.
// For wide matrices only the first key per (profile, gene) is kept — the
// importer stores one row per gene and ignores later duplicates: the key of
// the earliest line (smallest row_num), then (for tables staged without row
// numbers) a key naming the gene directly, then the smallest symbol. mirna = 1 marks matrix lines stored through ImportTabDelimData's
// microRNA branch, which creates no CNA events. Without a reference DB every
// key maps to itself.
func buildGeneKeyMap(c *client, rawDB, targetDB, refDB string, hasRef bool) error {
	resolve := func(mode geneMode, keys string) string {
		if !hasRef {
			return "SELECT *, toInt64OrZero(ent) AS gene_entrez, sym AS gene_hugo, toUInt8(1) AS gene_direct, toUInt8(0) AS gene_mirna FROM (" + keys + ")"
		}
		return resolveGeneSQL(refDB, mode, keys)
	}
	var parts []string
	add := func(src geneKeySource) {
		cols, _ := c.TableColumns(src.table)
		if !cols["study_id"] || !cols[src.symCol] && !cols[src.entCol] {
			return
		}
		col := func(n string) string {
			if cols[n] {
				return "`" + n + "`"
			}
			return "''"
		}
		prof := "''"
		if cols["profile_stable_id"] {
			prof = "profile_stable_id"
		}
		where := ""
		if src.where != "" {
			where = " WHERE " + src.where
		}
		keys := fmt.Sprintf(
			"SELECT DISTINCT study_id, %s AS profile_stable_id, %s AS sym, %s AS ent FROM `%s`.`%s`%s",
			prof, col(src.symCol), col(src.entCol), rawDB, src.table, where)
		firstRow := "toUInt64(0)"
		if src.mode == geneMatrix && cols["row_num"] {
			keys = fmt.Sprintf(
				"SELECT study_id, %s AS profile_stable_id, %s AS sym, %s AS ent, min(row_num) AS first_row FROM `%s`.`%s`%s "+
					"GROUP BY study_id, profile_stable_id, sym, ent",
				prof, col(src.symCol), col(src.entCol), rawDB, src.table, where)
			firstRow = "first_row"
		}
		sel := fmt.Sprintf(
			"SELECT '%s' AS source, study_id, profile_stable_id, sym AS hugo_symbol, ent AS entrez_gene_id, "+
				"gene_entrez AS resolved_entrez_gene_id, gene_hugo AS resolved_hugo_gene_symbol, toUInt8(0) AS mirna FROM (%s)",
			src.table, resolve(src.mode, keys))
		if src.mode == geneMatrix {
			sel = fmt.Sprintf(
				"SELECT source, study_id, profile_stable_id, hugo_symbol, entrez_gene_id, resolved_entrez_gene_id, resolved_hugo_gene_symbol, mirna FROM ("+
					"SELECT '%s' AS source, study_id, profile_stable_id, sym AS hugo_symbol, ent AS entrez_gene_id, "+
					"gene_entrez AS resolved_entrez_gene_id, gene_hugo AS resolved_hugo_gene_symbol, gene_mirna AS mirna, gene_direct, %s AS first_row FROM (%s)) "+
					"ORDER BY study_id, profile_stable_id, resolved_entrez_gene_id, first_row, gene_direct DESC, hugo_symbol, entrez_gene_id "+
					"LIMIT 1 BY study_id, profile_stable_id, resolved_entrez_gene_id",
				src.table, firstRow, resolve(src.mode, keys))
		}
		parts = append(parts, sel)
	}
	for _, t := range []string{"cna_matrix", "mrna_expression_matrix", "methylation_matrix"} {
		add(geneKeySource{table: t, mode: geneMatrix, symCol: "hugo_symbol", entCol: "entrez_gene_id"})
	}
	// Protein files keyed by gene (Hugo_Symbol / Entrez_Gene_Id) follow the
	// matrix rules; RPPA files (Composite.Element.REF) their own.
	protein, _ := c.TableColumns("protein_level_matrix")
	if protein["composite_element_ref"] {
		add(geneKeySource{table: "protein_level_matrix", mode: geneMatrix, symCol: "hugo_symbol", entCol: "entrez_gene_id",
			where: "composite_element_ref = ''"})
		if hasRef {
			if err := addPhosphoproteins(c, rawDB, refDB); err != nil {
				return err
			}
		}
		parts = append(parts, rppaGeneKeysSQL(rawDB, refDB, protein["entrez_gene_id"], protein["row_num"], hasRef))
	} else {
		add(geneKeySource{table: "protein_level_matrix", mode: geneMatrix, symCol: "hugo_symbol", entCol: "entrez_gene_id"})
	}
	add(geneKeySource{table: "cna_discrete_long", mode: geneCnaLong, symCol: "hugo_symbol", entCol: "entrez_gene_id"})
	add(geneKeySource{table: "mutation_extended", mode: geneMutation, symCol: "hugo_symbol", entCol: "entrez_gene_id"})
	add(geneKeySource{table: "mutation_uncalled", mode: geneMutation, symCol: "hugo_symbol", entCol: "entrez_gene_id"})
	if cols, _ := c.TableColumns("structural_variant"); cols["study_id"] {
		svKeys := []string{}
		for _, site := range []string{"site1", "site2"} {
			sym, ent := "''", "''"
			if cols[site+"_hugo_symbol"] {
				sym = site + "_hugo_symbol"
			}
			if cols[site+"_entrez_gene_id"] {
				ent = site + "_entrez_gene_id"
			}
			svKeys = append(svKeys, fmt.Sprintf(
				"SELECT DISTINCT study_id, profile_stable_id, %s AS sym, %s AS ent FROM `%s`.structural_variant", sym, ent, rawDB))
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT 'structural_variant' AS source, study_id, profile_stable_id, sym AS hugo_symbol, ent AS entrez_gene_id, "+
				"gene_entrez AS resolved_entrez_gene_id, gene_hugo AS resolved_hugo_gene_symbol, toUInt8(0) AS mirna FROM (%s)",
			resolve(geneSV, "SELECT DISTINCT * FROM ("+strings.Join(svKeys, " UNION ALL ")+")")))
	}
	if len(parts) == 0 {
		parts = append(parts, "SELECT '' AS source, '' AS study_id, '' AS profile_stable_id, '' AS hugo_symbol, '' AS entrez_gene_id, "+
			"toInt64(0) AS resolved_entrez_gene_id, '' AS resolved_hugo_gene_symbol, toUInt8(0) AS mirna WHERE 0")
	}
	_, err := c.Execute(fmt.Sprintf(
		"CREATE OR REPLACE TABLE `%s`.gene_key_map ENGINE = MergeTree ORDER BY (source, study_id, profile_stable_id, hugo_symbol, entrez_gene_id) AS %s",
		targetDB, strings.Join(parts, " UNION ALL ")))
	if err != nil {
		return fmt.Errorf("creating table gene_key_map: %w", err)
	}
	return nil
}

// geneKeyJoin joins alias a of a staging table to its gene_key_map rows as
// alias gk (INNER: unresolved rows are dropped, as the importers skip them).
func geneKeyJoin(targetDB, source, a, symCol, entCol string) string {
	return fmt.Sprintf(
		" INNER JOIN (SELECT * FROM `%[1]s`.gene_key_map WHERE source = '%[2]s') gk "+
			"ON gk.study_id = %[3]s.study_id AND gk.profile_stable_id = %[3]s.profile_stable_id "+
			"AND gk.hugo_symbol = %[3]s.%[4]s AND gk.entrez_gene_id = %[3]s.%[5]s",
		targetDB, source, a, symCol, entCol)
}
