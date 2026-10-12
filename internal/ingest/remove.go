package ingest

import (
	"fmt"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// TableRows is a table of the raw database and its number of rows of one
// study.
type TableRows struct {
	Table string `json:"table"`
	Rows  uint64 `json:"rows"`
}

// StudyTables returns the tables of the raw database that hold rows of a
// study, with their counts: the rows RemoveStudy deletes.
func StudyTables(c *ch.Client, studyID string) ([]TableRows, error) {
	type tableRow struct {
		Table string `json:"table"`
	}
	tables, err := ch.QueryP[tableRow](c,
		"SELECT c.table AS table FROM system.columns c JOIN system.tables t ON t.database = c.database AND t.name = c.table "+
			"WHERE c.database = {db:String} AND c.name = 'study_id' AND t.engine LIKE '%MergeTree' ORDER BY c.table",
		ch.Params{"db": c.Database()})
	if err != nil {
		return nil, err
	}
	var out []TableRows
	for _, t := range tables {
		type countRow struct {
			N uint64 `json:"n"`
		}
		n, err := ch.QueryP[countRow](c, fmt.Sprintf(
			"SELECT count() AS n FROM `%s`.`%s` WHERE study_id = {study:String}", c.Database(), t.Table),
			ch.Params{"study": studyID})
		if err != nil {
			return nil, err
		}
		if len(n) == 1 && n[0].N > 0 {
			out = append(out, TableRows{t.Table, n[0].N})
		}
	}
	return out, nil
}

// RemoveStudy deletes every staged row of a study: the rows with its
// study_id in each table of the raw database. Shared rows (gene panels,
// an empty study_id) stay. It returns the number of tables that held the study.
// A transform afterwards removes the study from the portal database.
func RemoveStudy(c *ch.Client, studyID string) (int, error) {
	tables, err := StudyTables(c, studyID)
	if err != nil {
		return 0, err
	}
	for i, t := range tables {
		// mutations_sync = 2 waits for every replica, so the next transform
		// reads none of the rows.
		if _, err := c.Execute(fmt.Sprintf(
			"ALTER TABLE `%s`.`%s` DELETE WHERE study_id = '%s' SETTINGS mutations_sync = 2",
			c.Database(), t.Table, ch.SQLLit(studyID))); err != nil {
			return i, fmt.Errorf("removing study %s from %s: %w", studyID, t.Table, err)
		}
	}
	return len(tables), nil
}
