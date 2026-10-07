// Package ingest streams a study's data files into ClickHouse staging tables.
// Record files stream directly; wide gene×sample matrices are unpivoted to long
// form. Every row is tagged with provenance constants so otherwise-identical
// tables (e.g. DISCRETE vs LOG2 CNA in cna_matrix) stay distinguishable.
package ingest

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

var provColumns = [5]string{"study_id", "source_file", "alteration_type", "datatype", "profile_stable_id"}

// Every staged data row records where it came from: file_num is its file's
// position among the study's data files in ingest order (1-based) and
// row_num its position among the rows the file yields (1-based, in file
// order). Together they order a study's rows as its files list them,
// independently of how ClickHouse stores them.
const (
	fileNumColumn = "file_num"
	rowNumColumn  = "row_num"
)

// orderColumnDefs declare the order columns. A file's rows keep their order
// within its part (the sort key, study and file, ties within a file), so
// row_num ascends by 1 and delta-encodes to almost nothing.
var orderColumnDefs = [2]string{"`" + fileNumColumn + "` UInt32", "`" + rowNumColumn + "` UInt64 CODEC(Delta, LZ4)"}

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

// ensureSchema creates the staging table (provenance, file_num, row_num and
// the data columns as String) or adds any missing columns to an existing one.
func ensureSchema(c *ch.Client, table string, dataColumns []string) error {
	existing, err := c.TableColumns(table)
	if err != nil {
		return err
	}
	all := append(append([]string{}, provColumns[:]...), dataColumns...)
	if len(existing) == 0 {
		defs := make([]string, 0, len(all)+len(orderColumnDefs))
		for _, col := range provColumns {
			defs = append(defs, fmt.Sprintf("`%s` String", col))
		}
		defs = append(defs, orderColumnDefs[:]...)
		for _, col := range dataColumns {
			defs = append(defs, fmt.Sprintf("`%s` String", col))
		}
		ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s`.`%s` (%s) ENGINE = MergeTree ORDER BY (study_id, source_file)",
			c.Database(), table, strings.Join(defs, ", "))
		_, err := c.Execute(ddl)
		return err
	}
	if err := addOrderColumns(c, table, existing); err != nil {
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

// addOrderColumns adds file_num and row_num to a staging table created
// without them; its existing rows read 0 for both.
func addOrderColumns(c *ch.Client, table string, existing map[string]bool) error {
	for i, col := range []string{fileNumColumn, rowNumColumn} {
		if !existing[col] {
			if _, err := c.Execute(fmt.Sprintf("ALTER TABLE `%s`.`%s` ADD COLUMN IF NOT EXISTS %s",
				c.Database(), table, orderColumnDefs[i])); err != nil {
				return err
			}
		}
	}
	return nil
}

// sampleIDColumns names, per staging table of record files, the column whose
// ids cbioportal-core's importer reads through StableIdUtil.getSampleId.
var sampleIDColumns = map[string]string{
	"clinical_sample": "sample_id", "clinical_mixed": "sample_id",
	"mutation_extended": "tumor_sample_barcode", "mutation_uncalled": "tumor_sample_barcode",
	"cna_seg": "id", "cna_discrete_long": "sample_id", "gene_panel_matrix": "sample_id",
}

// insertSQL inserts a numbered body (row_num, then the data columns) with
// the provenance and file_num as constants, and the table's sample ids (see
// sampleIDColumns) as the importer stores them.
func insertSQL(c *ch.Client, table string, prov [5]string, fileNum int, dataColumns []string) string {
	colList := backtick(append(append(append([]string{}, provColumns[:]...), fileNumColumn, rowNumColumn), dataColumns...))
	consts := make([]string, len(prov), len(prov)+1)
	for i, v := range prov {
		consts[i] = "'" + ch.SQLLit(v) + "'"
	}
	consts = append(consts, fmt.Sprintf("toUInt32(%d)", fileNum))
	inputStruct := make([]string, 0, len(dataColumns)+1)
	inputStruct = append(inputStruct, "`"+rowNumColumn+"` UInt64")
	for _, col := range dataColumns {
		inputStruct = append(inputStruct, fmt.Sprintf("`%s` String", col))
	}
	star := "*"
	if col := sampleIDColumns[table]; col != "" && slices.Contains(dataColumns, col) {
		q := "`" + col + "`"
		star = fmt.Sprintf("* REPLACE (%s AS %s)", parser.SampleIDSQL(q), q)
	}
	return fmt.Sprintf(
		"INSERT INTO `%s`.`%s` (%s) SELECT %s, %s FROM input('%s') FORMAT TabSeparatedRaw",
		c.Database(), table, colList, strings.Join(consts, ", "), star, strings.Join(inputStruct, ", "))
}

// runInsert streams a numbered body (see insertSQL) into f's staging table.
func runInsert(c *ch.Client, f *parser.DataFile, studyID string, fileNum int, dataColumns []string, body io.Reader, emitted *uint64) (FileStat, error) {
	table := f.StageSlug
	source := fileName(f.DataPath)
	var size int64
	if fi, err := os.Stat(f.DataPath); err == nil {
		size = fi.Size()
	}
	if err := ensureSchema(c, table, dataColumns); err != nil {
		return FileStat{}, err
	}
	sql := insertSQL(c, table, provenance(studyID, f), fileNum, dataColumns)
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

func ingestRecord(c *ch.Client, studyID string, fileNum int, f *parser.DataFile) (FileStat, error) {
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
	return runInsert(c, f, studyID, fileNum, cols, body, &emitted)
}

func ingestHeaderless(c *ch.Client, studyID string, fileNum int, f *parser.DataFile) (FileStat, error) {
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
	return runInsert(c, f, studyID, fileNum, cols, body, &emitted)
}

func ingestMatrix(c *ch.Client, studyID string, fileNum int, f *parser.DataFile) (FileStat, error) {
	h, err := parser.ReadHeader(f.DataPath)
	if err != nil {
		return FileStat{}, err
	}
	shape := f.MatrixShape(h.Columns)
	if !f.IsPatientLevel() {
		for i, id := range shape.SampleColumns {
			shape.SampleColumns[i] = parser.SampleID(id)
		}
	}
	cols := append(append([]string{}, shape.IDColumns...), "sample_id", "value")
	var emitted uint64
	body, err := parser.OpenMatrix(f.DataPath, shape, &emitted)
	if err != nil {
		return FileStat{}, err
	}
	return runInsert(c, f, studyID, fileNum, cols, body, &emitted)
}

// pruneRemovedFiles deletes the study's staged rows whose source file is no
// longer in the study directory (removed or renamed since its last ingest),
// in every staging table that records source files, so a re-ingested study
// holds only its current files' rows. It runs before anything is loaded.
func pruneRemovedFiles(c *ch.Client, s *parser.Study) error {
	current := map[string]bool{}
	err := filepath.WalkDir(s.Dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			current[d.Name()] = true
		}
		return err
	})
	if err != nil {
		return err
	}
	type tableRow struct {
		Table string `json:"table"`
	}
	tables, err := ch.QueryP[tableRow](c,
		"SELECT table FROM system.columns WHERE database = {db:String} AND name IN ('study_id', 'source_file') "+
			"GROUP BY table HAVING count() = 2 ORDER BY table", ch.Params{"db": c.Database()})
	if err != nil {
		return err
	}
	type fileRow struct {
		File string `json:"file"`
	}
	for _, t := range tables {
		files, err := ch.QueryP[fileRow](c, fmt.Sprintf(
			"SELECT DISTINCT source_file AS file FROM `%s`.`%s` WHERE study_id = {study:String}", c.Database(), t.Table),
			ch.Params{"study": s.StudyID})
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.File != "" && !current[f.File] {
				c.DeleteWhere(t.Table, fmt.Sprintf("study_id = '%s' AND source_file = '%s'",
					ch.SQLLit(s.StudyID), ch.SQLLit(f.File)))
			}
		}
	}
	return nil
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
	if err := pruneRemovedFiles(c, s); err != nil {
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
		var fn func(*ch.Client, string, int, *parser.DataFile) (FileStat, error)
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
		st, err := fn(c, s.StudyID, i+1, f)
		if err != nil {
			return nil, err
		}
		stats = append(stats, st)
	}
	seq := map[string]int{}
	for _, f := range s.Files {
		seq[fileName(f.DataPath)] = f.ImportSeq
	}
	if err := recordImportOrder(c, s.StudyID, "data", seq); err != nil {
		return nil, err
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
