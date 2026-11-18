package ingest

import (
	"fmt"
	"strings"
	"sync"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

// rowTables are the staging tables of record files whose headers differ
// most between studies (clinical data, mutations, structural variants,
// timelines). Their rows are stored in <table>_rows: the provenance and
// order columns, plus one map from column name to value holding the row's
// non-empty values (valuesColumn). <table> is a view with a column per name any of its
// files had, so readers see the same columns a table with a column per
// header would have, and a file with new column names replaces the view
// instead of altering a table.
var rowTables = map[string]bool{
	"clinical_sample": true, "clinical_patient": true, "clinical_mixed": true,
	"mutation_extended": true, "mutation_uncalled": true,
	"structural_variant": true, "timeline": true,
}

func rowsTable(table string) string { return table + "_rows" }

// valuesColumn is a rows table's map of column names to values.
const valuesColumn = "__values"

// stagesRows reports whether a staging table is stored as rows with a map
// of values: one of rowTables, unless the raw database already has it as a
// table with a column per header.
func stagesRows(c *ch.Client, table string) (bool, error) {
	if !rowTables[table] {
		return false, nil
	}
	engine, err := c.TableEngine(table)
	if err != nil {
		return false, err
	}
	return !strings.HasSuffix(engine, "MergeTree"), nil
}

// rowSchemaLocks holds a mutex per database and record table, so files
// loading at once replace its view in turn.
var rowSchemaLocks sync.Map

// ensureRowsSchema creates table's rows table and makes its view list every
// data column, in the order the files first had them.
func ensureRowsSchema(c *ch.Client, table string, dataColumns []string) error {
	mu, _ := rowSchemaLocks.LoadOrStore(c.Database()+"."+table, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	defs := make([]string, 0, len(provColumns)+len(orderColumnDefs)+1)
	for _, col := range provColumns {
		defs = append(defs, fmt.Sprintf("`%s` String", col))
	}
	defs = append(defs, orderColumnDefs[:]...)
	defs = append(defs, "`__values` Map(LowCardinality(String), String)")
	rows := rowsTable(table)
	if err := c.EnsureTable(rows, fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.`%s` (%s) ENGINE = MergeTree ORDER BY (study_id, source_file, %s)",
		c.Database(), rows, strings.Join(defs, ", "), rowNumColumn)); err != nil {
		return err
	}
	viewCols, err := orderedColumns(c, table)
	if err != nil {
		return err
	}
	keys := viewKeys(viewCols)
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
	}
	added := len(viewCols) == 0
	for _, col := range dataColumns {
		if !have[col] && !rowFixedColumns[col] {
			keys = append(keys, col)
			have[col] = true
			added = true
		}
	}
	if !added {
		return nil
	}
	sel := make([]string, 0, len(provColumns)+2+len(keys))
	for _, col := range provColumns {
		sel = append(sel, "`"+col+"`")
	}
	sel = append(sel, "`"+fileNumColumn+"`", "`"+rowNumColumn+"`")
	for _, k := range keys {
		sel = append(sel, fmt.Sprintf("`__values`['%s'] AS `%s`", ch.SQLLit(k), k))
	}
	_, err = c.Execute(fmt.Sprintf("CREATE OR REPLACE VIEW `%[1]s`.`%[2]s` AS SELECT %[3]s FROM `%[1]s`.`%[4]s`",
		c.Database(), table, strings.Join(sel, ", "), rows))
	return err
}

// rowFixedColumns are the view's columns that are not data columns.
var rowFixedColumns = func() map[string]bool {
	m := map[string]bool{fileNumColumn: true, rowNumColumn: true, valuesColumn: true}
	for _, col := range provColumns {
		m[col] = true
	}
	return m
}()

// viewKeys returns a record view's data columns, in order.
func viewKeys(cols []string) []string {
	var out []string
	for _, col := range cols {
		if !rowFixedColumns[col] {
			out = append(out, col)
		}
	}
	return out
}

// orderedColumns lists a table's columns in order, or none when it does
// not exist.
func orderedColumns(c *ch.Client, table string) ([]string, error) {
	type col struct {
		Name string `json:"name"`
	}
	cols, err := ch.QueryP[col](c,
		"SELECT name FROM system.columns WHERE database = {db:String} AND table = {table:String} ORDER BY position",
		ch.Params{"db": c.Database(), "table": table})
	if err != nil {
		return nil, err
	}
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out, nil
}

// rowsInsertSQL inserts a numbered body (row_num, then the data columns)
// into table's rows table, like insertSQL: the provenance and file_num as
// constants, the sample ids as the importer stores them, and the row's
// non-empty values as its map.
func rowsInsertSQL(c *ch.Client, table string, prov [5]string, fileNum int, dataColumns []string) string {
	consts := make([]string, len(prov), len(prov)+1)
	for i, v := range prov {
		consts[i] = "'" + ch.SQLLit(v) + "'"
	}
	consts = append(consts, fmt.Sprintf("toUInt32(%d)", fileNum))
	inputStruct := make([]string, 0, len(dataColumns)+1)
	inputStruct = append(inputStruct, "`"+rowNumColumn+"` UInt64")
	pairs := make([]string, 0, 2*len(dataColumns))
	sampleCol := sampleIDColumns[table]
	for _, col := range dataColumns {
		inputStruct = append(inputStruct, fmt.Sprintf("`%s` String", col))
		if rowFixedColumns[col] {
			continue
		}
		value := "`" + col + "`"
		if col == sampleCol {
			value = parser.SampleIDSQL(value)
		}
		pairs = append(pairs, "'"+ch.SQLLit(col)+"'", value)
	}
	values := "map()"
	if len(pairs) > 0 {
		values = fmt.Sprintf("mapFilter((k, v) -> v != '', map(%s))", strings.Join(pairs, ", "))
	}
	colList := backtick(append(append([]string{}, provColumns[:]...), fileNumColumn, rowNumColumn, valuesColumn))
	return fmt.Sprintf(
		"INSERT INTO `%s`.`%s` (%s) SELECT %s, `%s`, %s FROM input('%s') FORMAT TabSeparatedRaw",
		c.Database(), rowsTable(table), colList, strings.Join(consts, ", "), rowNumColumn, values,
		strings.Join(inputStruct, ", "))
}

// storageTable names the table holding a staging table's rows: its rows
// table when it is stored as rows, otherwise itself.
func storageTable(c *ch.Client, table string) (string, error) {
	rows, err := stagesRows(c, table)
	if err != nil || !rows {
		return table, err
	}
	return rowsTable(table), nil
}
