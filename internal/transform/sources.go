package transform

import (
	"fmt"
	"regexp"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// sourceColumn is a staging column the transform SQL reads unconditionally.
type sourceColumn struct {
	name, typ string
}

func strCols(names ...string) []sourceColumn {
	out := make([]sourceColumn, len(names))
	for i, n := range names {
		out[i] = sourceColumn{n, "String"}
	}
	return out
}

// stagingSources lists, per raw staging table the transform reads, the columns
// its SQL references without first checking that they exist. A study set
// lacking a data type has no such table, and a file may lack an optional
// column; the transform then reads an empty or column-completed stand-in so
// every view and table the API queries is still built, with the same column
// names and types as when the data is present.
var stagingSources = map[string][]sourceColumn{
	"cancer_study": append(strCols("study_id", "type_of_cancer", "name", "short_name", "description", "groups",
		"pmid", "citation", "reference_genome", "add_global_case_list", "status"),
		sourceColumn{"import_time", "DateTime64(6)"}),
	"clinical_attribute_meta": append(strCols("study_id", "attr_id", "display_name", "description", "datatype", "priority"),
		sourceColumn{"patient_attribute", "UInt8"}),
	"genetic_profile": strCols("molecular_profile_id", "study_id", "profile_stable_id", "alteration_type", "datatype",
		"name", "description", "show_in_analysis", "generic_assay_type"),
	"sample_list":      strCols("study_id", "list_id", "category", "name", "description", "sample_id"),
	"import_order":     append(strCols("study_id", "kind", "name"), sourceColumn{"seq", "UInt32"}),
	"clinical_sample":  strCols("study_id", "sample_id", "patient_id"),
	"clinical_patient": strCols("study_id", "patient_id"),
	"gene_panel":       strCols("study_id", "gene_panel_id", "description", "hugo_symbol"),
	"mutation_extended": strCols("study_id", "profile_stable_id", "tumor_sample_barcode", "hugo_symbol", "entrez_gene_id",
		"chromosome", "start_position", "end_position", "reference_allele", "tumor_seq_allele2",
		"variant_classification", "variant_type", "mutation_status", "validation_status", "t_alt_count", "t_ref_count",
		"hgvsp_short", "cbp_driver", "cbp_driver_annotation", "cbp_driver_tiers", "cbp_driver_tiers_annotation"),
	"structural_variant":     strCols("study_id", "profile_stable_id", "sample_id"),
	"cna_matrix":             strCols("study_id", "profile_stable_id", "datatype", "sample_id", "hugo_symbol", "entrez_gene_id", "value"),
	"cna_discrete_long":      strCols("study_id", "profile_stable_id", "sample_id", "hugo_symbol", "entrez_gene_id", "value"),
	"methylation_matrix":     strCols("study_id", "profile_stable_id", "sample_id", "hugo_symbol", "entrez_gene_id", "value"),
	"mrna_expression_matrix": strCols("study_id", "profile_stable_id", "sample_id", "hugo_symbol", "entrez_gene_id", "value"),
	"protein_level_matrix": strCols("study_id", "profile_stable_id", "sample_id", "hugo_symbol", "entrez_gene_id",
		"composite_element_ref", "value"),
	"generic_assay_matrix": strCols("study_id", "profile_stable_id", "sample_id", "entity_stable_id", "value"),
	"cna_seg":              strCols("study_id", "id", "chrom", "loc_start", "loc_end", "num_mark", "seg_mean"),
	"timeline": strCols("study_id", "patient_id", "sample_id", "event_type", "start_date", "stop_date",
		"surgery", "status", "event_type_detailed", "treatment_type", "subtype", "agent"),
}

// positionedSources are the staging tables whose rows the transform reads in
// file order (fileOrderSQL): their stand-ins carry the part name and offset,
// and file_num and row_num (0) where the table lacks them.
var positionedSources = map[string]bool{
	"cancer_study": true, "clinical_sample": true, "clinical_patient": true, "cna_seg": true,
}

// client is the ClickHouse client the transform runs its SQL through: every
// reference to a staging table listed in stagingSources that is absent from
// the raw database becomes an empty, correctly-typed subquery, and one to a
// table missing some of those columns becomes a subquery adding them as
// empty strings. Views built over a stand-in hold no rows until a later
// transform sees the real table. Metadata lookups (TableColumns, TableExists)
// still describe the raw database as it is.
type client struct {
	*ch.Client
	subst []sourceSubst
	stage *staging // nil: portal objects are written in place
}

type sourceSubst struct {
	ref  *regexp.Regexp
	with string
}

// newClient inspects the raw staging tables and prepares the stand-ins.
func newClient(c *ch.Client, rawDB string) *client {
	out := &client{Client: c}
	for table, cols := range stagingSources {
		have, _ := c.TableColumns(table)
		var with string
		if len(have) == 0 {
			sel := make([]string, len(cols))
			for i, col := range cols {
				sel[i] = fmt.Sprintf("CAST(defaultValueOfTypeName('%s') AS %s) AS `%s`", col.typ, col.typ, col.name)
			}
			if positionedSources[table] {
				sel = append(sel, "'' AS _part", "toUInt64(0) AS _part_offset", "toUInt32(0) AS file_num", "toUInt64(0) AS row_num")
			}
			with = "(SELECT " + strings.Join(sel, ", ") + " WHERE 0)"
		} else {
			var add []string
			for _, col := range cols {
				if !have[col.name] {
					add = append(add, fmt.Sprintf("CAST(defaultValueOfTypeName('%s') AS %s) AS `%s`", col.typ, col.typ, col.name))
				}
			}
			if positionedSources[table] {
				if !have["file_num"] {
					add = append(add, "toUInt32(0) AS file_num")
				}
				if !have["row_num"] {
					add = append(add, "toUInt64(0) AS row_num")
				}
			}
			if len(add) == 0 {
				continue
			}
			if positionedSources[table] {
				add = append([]string{"_part", "_part_offset"}, add...)
			}
			with = fmt.Sprintf("(SELECT *, %s FROM `%s`.`%s`)", strings.Join(add, ", "), rawDB, table)
		}
		out.subst = append(out.subst, sourceSubst{ref: tableRef(rawDB, table), with: with})
	}
	return out
}

// tableRef matches a reference to db.table in SQL text, with either name
// back-quoted or bare.
func tableRef(db, table string) *regexp.Regexp {
	d, t := regexp.QuoteMeta(db), regexp.QuoteMeta(table)
	return regexp.MustCompile("(?:`" + d + "`|\\b" + d + "\\b)\\.(?:`" + t + "`|" + t + "\\b)")
}

// Execute runs sql with staging-table stand-ins substituted and, while a
// transform stages its build, portal objects under their staged names.
func (c *client) Execute(sql string) (string, error) {
	if c.stage != nil {
		c.stage.record(sql)
		sql = c.stage.rewrite(sql)
	}
	return c.executeUnstaged(sql)
}

// executeUnstaged runs sql with staging-table stand-ins substituted.
func (c *client) executeUnstaged(sql string) (string, error) {
	for _, s := range c.subst {
		sql = s.ref.ReplaceAllLiteralString(sql, s.with)
	}
	return c.Client.Execute(sql)
}
