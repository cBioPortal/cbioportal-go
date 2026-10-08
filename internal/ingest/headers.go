package ingest

import (
	"fmt"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// sourceHeaderTable records, per staged file of the tables in
// headerTables, each data column's header exactly as the file writes it,
// which the sanitized column name loses. The transform keys timeline event
// data by it, as cbioportal-core's ImportTimelineData does.
const sourceHeaderTable = "source_header"

// headerTables are the staging tables whose files' headers are recorded.
var headerTables = map[string]bool{"timeline": true}

// recordHeader replaces the recorded header of one staged file: position
// is the column's 0-based index in the header, column its staged name.
func recordHeader(c *ch.Client, studyID, table, source string, columns, raw []string) error {
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.`%s` (study_id String, source_file String, table_name String, "+
			"position UInt32, column_name String, header String) ENGINE = MergeTree ORDER BY (study_id, source_file)",
		c.Database(), sourceHeaderTable)); err != nil {
		return err
	}
	c.DeleteWhere(sourceHeaderTable, fmt.Sprintf("study_id = '%s' AND source_file = '%s' AND table_name = '%s'",
		ch.SQLLit(studyID), ch.SQLLit(source), ch.SQLLit(table)))
	rows := make([]string, len(columns))
	for i, col := range columns {
		rows[i] = fmt.Sprintf("('%s', '%s', '%s', %d, '%s', '%s')", ch.SQLLit(studyID), ch.SQLLit(source),
			ch.SQLLit(table), i, ch.SQLLit(col), ch.SQLLit(raw[i]))
	}
	if len(rows) == 0 {
		return nil
	}
	_, err := c.Execute(fmt.Sprintf("INSERT INTO `%s`.`%s` VALUES %s", c.Database(), sourceHeaderTable, strings.Join(rows, ", ")))
	return err
}
