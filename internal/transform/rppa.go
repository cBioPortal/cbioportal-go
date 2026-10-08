package transform

import (
	"fmt"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// RPPA files are protein matrices whose lines are keyed by
// Composite.Element.REF, "<gene symbols>|<antibody>" (e.g.
// "AKT1 AKT2 AKT3|Akt_pS473"). cbioportal-core's ImportTabDelimData
// (parseRPPAGenes, saveRppaValues) stores such a line under each gene its
// space-separated symbols name non-ambiguously; when the antibody names a
// phosphorylation site (pS473, pT308_Y310, ...), under the phosphoprotein
// "<GENE>_<SITE>" of each gene instead, a gene of type phosphoprotein it
// adds to the gene table when missing. Lines are skipped when the value has
// no antibody after '|', contains "///" or "---", or repeats an earlier
// line's antibody. A line naming a microRNA gene is stored under its
// microRNA genes only. As for every matrix, a gene keeps the first line
// stored under it.

// rppaResidue is the phosphorylation site pattern parseRPPAGenes looks for
// in the antibody name.
const rppaResidue = "p[STY][0-9]+(?:_[STY][0-9]+)*"

// rppaSymbolsSQL selects one row per gene symbol of each RPPA line the
// importer parses: study_id, profile_stable_id, line_sym (the
// Composite.Element.REF value), line_ent (the line's Entrez_Gene_Id, if the
// file has one), first_row (the line's first staged cell), gpos (the
// symbol's position in the line), residue (the antibody's phosphorylation
// site, empty for a total protein) and sym / ent, the symbol to resolve.
func rppaSymbolsSQL(rawDB string, hasEnt, hasRowNum bool) string {
	ent, first := "''", "toUInt64(0)"
	if hasEnt {
		ent = "entrez_gene_id"
	}
	if hasRowNum {
		first = "min(row_num)"
	}
	lines := fmt.Sprintf(
		"SELECT study_id, profile_stable_id, composite_element_ref AS line_sym, %s AS line_ent, %s AS first_row "+
			"FROM `%s`.protein_level_matrix WHERE composite_element_ref != '' GROUP BY study_id, profile_stable_id, line_sym, line_ent",
		ent, first, rawDB)
	// Java's String.split drops trailing empty parts: the antibody is the
	// second part, and a value without one is not parsed.
	parsed := "SELECT *, _parts[1] AS _genes, _parts[2] AS _antibody FROM (" +
		"SELECT *, splitByChar('|', line_sym) AS _parts FROM (" + lines + ") " +
		"WHERE position(line_sym, '///') = 0 AND position(line_sym, '---') = 0 AND (line_ent = '' OR match(line_ent, '^[0-9]+$'))) " +
		"WHERE arrayLastIndex(x -> x != '', _parts) >= 2 " +
		"ORDER BY first_row, line_sym LIMIT 1 BY study_id, profile_stable_id, _antibody"
	return fmt.Sprintf(
		"SELECT study_id, profile_stable_id, line_sym, line_ent, first_row, gpos, _syms[gpos] AS sym, '' AS ent, "+
			"extract(_antibody, '%s') AS residue "+
			"FROM (SELECT *, splitByChar(' ', _genes) AS _syms FROM (%s)) ARRAY JOIN arrayEnumerate(_syms) AS gpos",
		rppaResidue, parsed)
}

// rppaGenesSQL resolves rppaSymbolsSQL's symbols: one row per gene found,
// with gene_entrez / gene_hugo / gene_mirna (resolveGeneSQL) and phospho,
// the phosphoprotein symbol the line is stored under instead (empty for a
// total protein).
func rppaGenesSQL(rawDB, refDB string, hasEnt, hasRowNum bool) string {
	return "SELECT *, if(residue = '', '', upper(concat(gene_hugo, '_', residue))) AS phospho FROM (" +
		resolveGeneSQL(refDB, geneRPPA, rppaSymbolsSQL(rawDB, hasEnt, hasRowNum)) + ")"
}

// rppaGeneKeysSQL selects the gene_key_map rows of the RPPA lines: the
// gene each line is stored under, keyed by its Composite.Element.REF value
// (hugo_symbol) and Entrez_Gene_Id. Every phosphoprotein must exist in the
// reference (addPhosphoproteins). Without a reference database each line
// maps to itself.
func rppaGeneKeysSQL(rawDB, refDB string, hasEnt, hasRowNum, hasRef bool) string {
	if !hasRef {
		return fmt.Sprintf(
			"SELECT 'protein_level_matrix' AS source, study_id, profile_stable_id, line_sym AS hugo_symbol, line_ent AS entrez_gene_id, "+
				"toInt64OrZero(line_ent) AS resolved_entrez_gene_id, line_sym AS resolved_hugo_gene_symbol, toUInt8(0) AS mirna "+
				"FROM (SELECT DISTINCT study_id, profile_stable_id, line_sym, line_ent FROM (%s))",
			rppaSymbolsSQL(rawDB, hasEnt, hasRowNum))
	}
	stored := fmt.Sprintf(
		"SELECT q.study_id AS study_id, q.profile_stable_id AS profile_stable_id, q.line_sym AS line_sym, q.line_ent AS line_ent, "+
			"q.first_row AS first_row, q.gpos AS gpos, "+
			"if(q.phospho = '', q.gene_entrez, p._pe) AS entrez, if(q.phospho = '', q.gene_hugo, p._ph) AS hugo, "+
			"if(q.phospho = '', q.gene_mirna, toUInt8(p._pt = 'miRNA')) AS mirna "+
			"FROM (%s) q LEFT JOIN (SELECT upper(hugo_gene_symbol) AS _ps, any(entrez_gene_id) AS _pe, "+
			"any(hugo_gene_symbol) AS _ph, any(type) AS _pt FROM `%s`.gene GROUP BY _ps) p ON p._ps = q.phospho "+
			"WHERE q.phospho = '' OR p._ps != ''",
		rppaGenesSQL(rawDB, refDB, hasEnt, hasRowNum), refDB)
	return "SELECT 'protein_level_matrix' AS source, study_id, profile_stable_id, line_sym AS hugo_symbol, line_ent AS entrez_gene_id, " +
		"entrez AS resolved_entrez_gene_id, hugo AS resolved_hugo_gene_symbol, mirna FROM (" +
		"SELECT *, max(mirna) OVER (PARTITION BY study_id, profile_stable_id, line_sym, line_ent) AS line_mirna FROM (" + stored + ")) " +
		"WHERE mirna = 1 OR line_mirna = 0 " +
		"ORDER BY study_id, profile_stable_id, entrez, first_row, gpos LIMIT 1 BY study_id, profile_stable_id, entrez"
}

// addPhosphoproteins adds the phosphoproteins the RPPA lines are stored
// under to the reference genes when missing, as DaoGene.addGeneWithoutEntrezGeneId
// does: type phosphoprotein, the next unused negative Entrez id (from -1
// down) and genetic entity id, and the aliases "rppa-phospho",
// "phosphoprotein" and "phospho<GENE>". New genes are numbered in the
// order the lines name them.
func addPhosphoproteins(c *client, rawDB, refDB string) error {
	protein, _ := c.TableColumns("protein_level_matrix")
	text, err := c.Execute(fmt.Sprintf(
		"SELECT phospho, base FROM (SELECT phospho, argMin(gene_hugo, (study_id, profile_stable_id, first_row, gpos)) AS base, "+
			"min((study_id, profile_stable_id, first_row, gpos)) AS ord FROM (%s) WHERE phospho != '' GROUP BY phospho) "+
			"WHERE phospho NOT IN (SELECT upper(hugo_gene_symbol) FROM `%s`.gene) ORDER BY ord FORMAT TabSeparated",
		rppaGenesSQL(rawDB, refDB, protein["entrez_gene_id"], protein["row_num"]), refDB))
	if err != nil {
		return fmt.Errorf("listing RPPA phosphoproteins: %w", err)
	}
	type gene struct{ symbol, base string }
	var add []gene
	for line := range strings.Lines(text) {
		line = strings.TrimSuffix(line, "\n")
		symbol, base, _ := strings.Cut(line, "\t")
		add = append(add, gene{symbol, base})
	}
	if len(add) == 0 {
		return nil
	}
	used := map[int64]bool{}
	ids, err := c.Execute(fmt.Sprintf("SELECT entrez_gene_id FROM `%s`.gene WHERE entrez_gene_id < 0 FORMAT TabSeparated", refDB))
	if err != nil {
		return err
	}
	for l := range strings.Lines(ids) {
		var id int64
		fmt.Sscan(l, &id)
		used[id] = true
	}
	entityText, err := c.Execute(fmt.Sprintf("SELECT max(genetic_entity_id) FROM `%s`.gene FORMAT TabSeparated", refDB))
	if err != nil {
		return err
	}
	var entity int64
	fmt.Sscan(strings.TrimSpace(entityText), &entity)
	var genes, aliases []string
	next := int64(0)
	for _, g := range add {
		for next--; used[next]; next-- {
		}
		entity++
		genes = append(genes, fmt.Sprintf("(%d, '%s', %d, 'phosphoprotein')", next, ch.SQLLit(g.symbol), entity))
		for _, a := range []string{"rppa-phospho", "phosphoprotein", "phospho" + g.base} {
			aliases = append(aliases, fmt.Sprintf("(%d, '%s')", next, ch.SQLLit(a)))
		}
	}
	if _, err := c.Execute(fmt.Sprintf("INSERT INTO `%s`.gene (entrez_gene_id, hugo_gene_symbol, genetic_entity_id, type) VALUES %s",
		refDB, strings.Join(genes, ", "))); err != nil {
		return fmt.Errorf("adding phosphoproteins: %w", err)
	}
	if _, err := c.Execute(fmt.Sprintf("INSERT INTO `%s`.gene_alias (entrez_gene_id, gene_alias) VALUES %s",
		refDB, strings.Join(aliases, ", "))); err != nil {
		return fmt.Errorf("adding phosphoprotein aliases: %w", err)
	}
	return nil
}
