// Package ingest streams a study's data files into ClickHouse staging tables.
// Record files stream directly; wide gene×sample matrices are unpivoted to long
// form. Every row is tagged with provenance constants so otherwise-identical
// tables (e.g. DISCRETE vs LOG2 CNA in cna_matrix) stay distinguishable.
package ingest

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/parser"
)

var provColumns = [5]string{"study_id", "source_file", "alteration_type", "datatype", "profile_stable_id"}

type FileStat struct {
	Table        string
	SourceFile   string
	RowsLoaded   uint64
	RowsExpected uint64
	Bytes        int64
	Secs         float64
	Skipped      string
}

func (s FileStat) MBPerS() float64 {
	if s.Secs > 0 {
		return (float64(s.Bytes) / 1048576.0) / s.Secs
	}
	return 0
}
func (s FileStat) RowsPerS() float64 {
	if s.Secs > 0 {
		return float64(s.RowsLoaded) / s.Secs
	}
	return 0
}

func fileName(path string) string {
	i := strings.LastIndexByte(path, '/')
	return path[i+1:]
}

func provenance(studyID string, f *parser.DataFile) [5]string {
	return [5]string{studyID, fileName(f.DataPath), f.AlterationType, f.Datatype, f.StableID}
}

func backtick(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = "`" + c + "`"
	}
	return strings.Join(q, ", ")
}

// ensureSchema creates the staging table (provenance + data columns as String)
// or adds any missing columns to an existing one.
func ensureSchema(c *ch.Client, table string, dataColumns []string) error {
	existing, err := c.TableColumns(table)
	if err != nil {
		return err
	}
	all := append(append([]string{}, provColumns[:]...), dataColumns...)
	if len(existing) == 0 {
		defs := make([]string, len(all))
		for i, col := range all {
			defs[i] = fmt.Sprintf("`%s` String", col)
		}
		ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s`.`%s` (%s) ENGINE = MergeTree ORDER BY (study_id, source_file)",
			c.Database(), table, strings.Join(defs, ", "))
		_, err := c.Execute(ddl)
		return err
	}
	for _, col := range all {
		if !existing[col] {
			if _, err := c.Execute(fmt.Sprintf(
				"ALTER TABLE `%s`.`%s` ADD COLUMN IF NOT EXISTS `%s` String",
				c.Database(), table, col)); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertSQL(c *ch.Client, table string, prov [5]string, dataColumns []string) string {
	colList := backtick(append(append([]string{}, provColumns[:]...), dataColumns...))
	consts := make([]string, len(prov))
	for i, v := range prov {
		consts[i] = "'" + ch.SQLLit(v) + "'"
	}
	inputStruct := make([]string, len(dataColumns))
	for i, col := range dataColumns {
		inputStruct[i] = fmt.Sprintf("`%s` String", col)
	}
	return fmt.Sprintf(
		"INSERT INTO `%s`.`%s` (%s) SELECT %s, * FROM input('%s') FORMAT TabSeparatedRaw",
		c.Database(), table, colList, strings.Join(consts, ", "), strings.Join(inputStruct, ", "))
}

func runInsert(c *ch.Client, f *parser.DataFile, studyID string, dataColumns []string, body io.Reader, emitted *uint64) (FileStat, error) {
	table := f.StageSlug
	source := fileName(f.DataPath)
	var size int64
	if fi, err := os.Stat(f.DataPath); err == nil {
		size = fi.Size()
	}
	if err := ensureSchema(c, table, dataColumns); err != nil {
		return FileStat{}, err
	}
	sql := insertSQL(c, table, provenance(studyID, f), dataColumns)
	start := time.Now()
	if err := c.InsertStream(sql, body); err != nil {
		return FileStat{}, fmt.Errorf("streaming %s: %w", f.DataPath, err)
	}
	secs := time.Since(start).Seconds()
	loaded, err := c.CountForFile(table, studyID, source)
	if err != nil {
		return FileStat{}, err
	}
	return FileStat{Table: table, SourceFile: source, RowsLoaded: loaded, RowsExpected: *emitted, Bytes: size, Secs: secs}, nil
}

func ingestRecord(c *ch.Client, studyID string, f *parser.DataFile) (FileStat, error) {
	h, err := parser.ReadHeader(f.DataPath)
	if err != nil {
		return FileStat{}, err
	}
	cols := parser.SanitizeColumns(h.Columns)
	var emitted uint64
	body, err := parser.OpenBody(f.DataPath, true, &emitted)
	if err != nil {
		return FileStat{}, err
	}
	return runInsert(c, f, studyID, cols, body, &emitted)
}

func ingestHeaderless(c *ch.Client, studyID string, f *parser.DataFile) (FileStat, error) {
	var cols []string
	switch f.StageSlug {
	case "type_of_cancer":
		cols = []string{"type_of_cancer", "name", "dedicated_color", "parent"}
	default:
		h, err := parser.ReadHeader(f.DataPath)
		if err != nil {
			return FileStat{}, err
		}
		cols = parser.SanitizeColumns(h.Columns)
	}
	var emitted uint64
	body, err := parser.OpenBody(f.DataPath, false, &emitted)
	if err != nil {
		return FileStat{}, err
	}
	return runInsert(c, f, studyID, cols, body, &emitted)
}

func ingestMatrix(c *ch.Client, studyID string, f *parser.DataFile) (FileStat, error) {
	h, err := parser.ReadHeader(f.DataPath)
	if err != nil {
		return FileStat{}, err
	}
	shape := f.MatrixShape(h.Columns)
	cols := append(append([]string{}, shape.IDColumns...), "sample_id", "value")
	var emitted uint64
	body, err := parser.OpenMatrix(f.DataPath, shape, &emitted)
	if err != nil {
		return FileStat{}, err
	}
	return runInsert(c, f, studyID, cols, body, &emitted)
}

func deleteExisting(c *ch.Client, f *parser.DataFile, studyID string) {
	c.DeleteWhere(f.StageSlug, fmt.Sprintf(
		"study_id = '%s' AND source_file = '%s'", ch.SQLLit(studyID), ch.SQLLit(fileName(f.DataPath))))
}

// IngestStudy ingests every data file in a study plus the study/profile/panel/
// clinical-attribute dimensions. Idempotent: a study's prior rows are deleted
// before loading.
func IngestStudy(c *ch.Client, s *parser.Study) ([]FileStat, error) {
	if err := c.CreateDatabase(); err != nil {
		return nil, err
	}
	if err := ingestStudyMeta(c, s); err != nil {
		return nil, err
	}
	if err := ingestProfiles(c, s); err != nil {
		return nil, err
	}
	if err := ingestProfileSamples(c, s); err != nil {
		return nil, err
	}
	if err := ingestGenePanels(c, s); err != nil {
		return nil, err
	}
	if err := ingestClinicalMeta(c, s); err != nil {
		return nil, err
	}
	if err := ingestCaseLists(c, s); err != nil {
		return nil, err
	}
	if err := ingestStudyTags(c, s); err != nil {
		return nil, err
	}

	var stats []FileStat
	for i := range s.Files {
		f := &s.Files[i]
		var fn func(*ch.Client, string, *parser.DataFile) (FileStat, error)
		switch f.Layout {
		case parser.Record:
			fn = ingestRecord
		case parser.Headerless:
			fn = ingestHeaderless
		case parser.Matrix:
			fn = ingestMatrix
		default:
			continue
		}
		deleteExisting(c, f, s.StudyID)
		st, err := fn(c, s.StudyID, f)
		if err != nil {
			return nil, err
		}
		stats = append(stats, st)
	}
	if err := ingestGenericAssayEntities(c, s); err != nil {
		return nil, err
	}
	mutsig, err := ingestMutSig(c, s)
	if err != nil {
		return nil, err
	}
	return append(stats, mutsig...), nil
}
