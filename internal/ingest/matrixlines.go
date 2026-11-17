package ingest

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

// A matrix file (a gene or entity per line, a value per sample) is staged as
// its lines: one row per gene or entity in <table>_lines, with the line's
// values as an array, and the file's sample ids once in matrixSamplesTable.
// <table> itself is a view with one row per non-empty cell, as the API and
// the transform read it. Staging a line instead of each of its cells writes
// about a thousandth of the rows: a 100 MB expression file loads in a
// second instead of ten.
//
// A raw database whose <table> is still a table of cells (staged before
// lines existed) keeps staging cells into it.

// matrixSamplesTable holds each staged matrix file's sample ids, in column
// order.
const matrixSamplesTable = "matrix_samples"

func linesTable(table string) string { return table + "_lines" }

// lineColumns are a lines table's columns after the provenance columns and
// before the identifier columns: the file's position, the number of cells
// before the line in the file (its first cell's row_num is row_base + 1),
// the line's non-empty cells, and its values.
var lineColumnDefs = []string{"`file_num` UInt32", "`row_base` UInt64", "`cells` UInt32"}

// stagesLines reports whether a matrix table is staged as lines: true unless
// the raw database has it as a table of cells.
func stagesLines(c *ch.Client, table string) (bool, error) {
	engine, err := c.TableEngine(table)
	if err != nil {
		return false, err
	}
	return !strings.HasSuffix(engine, "MergeTree"), nil
}

// lineSchemaLocks holds a mutex per database and matrix table, so files
// loading at once extend a lines table and replace its view in turn.
var lineSchemaLocks sync.Map

// ensureLinesSchema creates or extends table's lines table with the
// identifier columns and makes the view list every identifier column.
func ensureLinesSchema(c *ch.Client, table string, idColumns []string) error {
	mu, _ := lineSchemaLocks.LoadOrStore(c.Database()+"."+table, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	if err := c.EnsureTable(matrixSamplesTable, fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.`%s` (study_id String, source_file String, stage_table String, samples Array(String)) "+
			"ENGINE = MergeTree ORDER BY (study_id, stage_table, source_file)", c.Database(), matrixSamplesTable)); err != nil {
		return err
	}
	lines := linesTable(table)
	existing, err := c.TableColumns(lines)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		defs := make([]string, 0, len(provColumns)+len(lineColumnDefs)+len(idColumns)+1)
		for _, col := range provColumns {
			defs = append(defs, fmt.Sprintf("`%s` String", col))
		}
		defs = append(defs, lineColumnDefs...)
		for _, col := range idColumns {
			defs = append(defs, fmt.Sprintf("`%s` String", col))
		}
		defs = append(defs, "`values` Array(String)")
		if err := c.EnsureTable(lines, fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s`.`%s` (%s) ENGINE = MergeTree ORDER BY %s",
			c.Database(), lines, strings.Join(defs, ", "), stagingSortKey(idColumns))); err != nil {
			return err
		}
		existing, err = c.TableColumns(lines)
		if err != nil {
			return err
		}
	} else {
		var add []string
		for _, col := range idColumns {
			if !existing[col] {
				add = append(add, fmt.Sprintf("`%s` String", col))
				existing[col] = true
			}
		}
		if err := addColumns(c, lines, add); err != nil {
			return err
		}
	}
	return ensureCellsView(c, table, existing)
}

// ensureCellsView (re)creates table as the view of its lines' non-empty
// cells, unless it already lists every identifier column of the lines
// table. Its columns are those of a table of cells: provenance, file_num,
// row_num, the identifier columns, sample_id and value, plus constant
// storage positions (_part, _part_offset), since row_num already orders
// the cells as their file does.
func ensureCellsView(c *ch.Client, table string, linesCols map[string]bool) error {
	fixed := map[string]bool{"file_num": true, "row_base": true, "cells": true, "values": true}
	for _, col := range provColumns {
		fixed[col] = true
	}
	var ids []string
	for col := range linesCols {
		if !fixed[col] {
			ids = append(ids, col)
		}
	}
	viewCols, err := c.TableColumns(table)
	if err != nil {
		return err
	}
	missing := len(viewCols) == 0
	for _, col := range ids {
		if !viewCols[col] {
			missing = true
		}
	}
	if !missing {
		return nil
	}
	slices.Sort(ids)
	inner := make([]string, 0, len(provColumns)+len(ids)+2)
	outer := make([]string, 0, len(provColumns)+len(ids)+6)
	for _, col := range provColumns {
		inner = append(inner, "l.`"+col+"` AS `"+col+"`")
		outer = append(outer, "`"+col+"`")
	}
	inner = append(inner, "l.file_num AS file_num", "l.row_base AS row_base")
	outer = append(outer, "file_num", "row_base + n AS row_num")
	for _, col := range ids {
		inner = append(inner, "l.`"+col+"` AS `"+col+"`")
		outer = append(outer, "`"+col+"`")
	}
	outer = append(outer, "cell.2 AS sample_id", "cell.1 AS value", "'' AS _part", "toUInt64(0) AS _part_offset")
	db := c.Database()
	_, err = c.Execute(fmt.Sprintf(
		"CREATE OR REPLACE VIEW `%[1]s`.`%[2]s` AS SELECT %[3]s FROM ("+
			"SELECT %[4]s, arrayFilter(x -> x.1 != '', arrayZip(arrayResize(l.values, length(s.samples), ''), s.samples)) AS cells "+
			"FROM `%[1]s`.`%[5]s` AS l INNER JOIN `%[1]s`.`%[6]s` AS s "+
			"ON s.study_id = l.study_id AND s.source_file = l.source_file AND s.stage_table = '%[2]s'"+
			") ARRAY JOIN cells AS cell, arrayEnumerate(cells) AS n",
		db, table, strings.Join(outer, ", "), strings.Join(inner, ", "), linesTable(table), matrixSamplesTable))
	return err
}

// ingestMatrixLines stages a matrix file as lines: comment, blank and short
// lines and lines repeating an earlier line's identifiers are left out, as
// when the file is staged as cells.
func ingestMatrixLines(c *ch.Client, studyID string, fileNum int, f *parser.DataFile, shape parser.MatrixShape) (FileStat, error) {
	table := f.StageSlug
	source := fileName(f.DataPath)
	if err := ensureLinesSchema(c, table, shape.IDColumns); err != nil {
		return FileStat{}, err
	}
	samples := make([]string, len(shape.SampleColumns))
	for i, s := range shape.SampleColumns {
		samples[i] = "'" + ch.SQLLit(s) + "'"
	}
	if _, err := c.Execute(fmt.Sprintf("INSERT INTO `%s`.`%s` (study_id, source_file, stage_table, samples) VALUES ('%s', '%s', '%s', [%s])",
		c.Database(), matrixSamplesTable, ch.SQLLit(studyID), ch.SQLLit(source), ch.SQLLit(table), strings.Join(samples, ","))); err != nil {
		return FileStat{}, err
	}

	var size int64
	if fi, err := os.Stat(f.DataPath); err == nil {
		size = fi.Size()
	}
	in, err := parser.OpenText(f.DataPath)
	if err != nil {
		return FileStat{}, err
	}
	// The lines go to lineStreams inserts at once, in turn: ClickHouse
	// builds one insert's rows on one thread.
	var cells uint64
	readers := make([]*io.PipeReader, lineStreams)
	writers := make([]*io.PipeWriter, lineStreams)
	outs := make([]io.Writer, lineStreams)
	for i := range readers {
		readers[i], writers[i] = io.Pipe()
		outs[i] = writers[i]
	}
	go func() {
		defer in.Close()
		err := writeMatrixLines(outs, in, len(shape.IDColumns), len(shape.SampleColumns), &cells)
		for _, w := range writers {
			w.CloseWithError(err)
		}
	}()

	k := len(shape.IDColumns)
	ids := make([]string, k)
	for i := range ids {
		ids[i] = fmt.Sprintf("f[%d]", i+3)
	}
	prov := provenance(studyID, f)
	consts := make([]string, len(prov))
	for i, v := range prov {
		consts[i] = "'" + ch.SQLLit(v) + "'"
	}
	cols := append(append(append([]string{}, provColumns[:]...), "file_num", "row_base", "cells"), shape.IDColumns...)
	cols = append(cols, "values")
	sql := fmt.Sprintf("INSERT INTO `%s`.`%s` (%s) SELECT %s, toUInt32(%d), toUInt64(f[1]), toUInt32(f[2]), %s, arraySlice(f, %d, %d) "+
		"FROM (SELECT splitByChar('\\t', line) AS f FROM input('line String')) FORMAT LineAsString",
		c.Database(), linesTable(table), backtick(cols), strings.Join(consts, ", "), fileNum, strings.Join(ids, ", "),
		k+3, len(shape.SampleColumns))
	start := time.Now()
	errs := make([]error, lineStreams)
	var wg sync.WaitGroup
	for i, r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errs[i] = c.InsertStream(sql, r); errs[i] != nil {
				r.CloseWithError(errs[i])
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return FileStat{}, fmt.Errorf("streaming %s: %w", f.DataPath, err)
		}
	}
	secs := time.Since(start).Seconds()
	loaded, err := countCells(c, table, studyID, source)
	if err != nil {
		return FileStat{}, err
	}
	return FileStat{Table: table, SourceFile: source, RowsLoaded: loaded, RowsExpected: cells, Bytes: size, Secs: secs}, nil
}

// lineStreams is how many inserts one matrix file's lines are split over.
const lineStreams = 4

// writeMatrixLines copies a matrix file's data lines to ws in turn, each
// prefixed with the number of cells before it and its own number of
// non-empty cells, and counts the non-empty cells.
func writeMatrixLines(ws []io.Writer, in io.Reader, idCount, sampleCount int, cells *uint64) error {
	r := bufio.NewReaderSize(in, 1<<20)
	outs := make([]*bufio.Writer, len(ws))
	for i, w := range ws {
		outs[i] = bufio.NewWriterSize(w, 256<<10)
	}
	next := 0
	header := true
	seen := map[string]struct{}{}
	var base uint64
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if line[0] == '#' {
			continue
		}
		line = bytes.TrimRight(line, "\r\n")
		if header {
			header = false
			continue
		}
		if len(line) == 0 {
			continue
		}
		fields := bytes.Split(line, []byte{'\t'})
		if len(fields) <= idCount {
			continue
		}
		prefix := string(bytes.Join(fields[:idCount], []byte{'\t'}))
		// A line repeating an earlier line's identifiers is skipped, as
		// ImportTabDelimData ignores a duplicated gene's later rows.
		if _, dup := seen[prefix]; dup {
			continue
		}
		seen[prefix] = struct{}{}
		var n uint64
		for j := 0; j < sampleCount && idCount+j < len(fields); j++ {
			if len(fields[idCount+j]) > 0 {
				n++
			}
		}
		out := outs[next]
		next = (next + 1) % len(outs)
		out.WriteString(strconv.FormatUint(base, 10))
		out.WriteByte('\t')
		out.WriteString(strconv.FormatUint(n, 10))
		out.WriteByte('\t')
		out.Write(line)
		if err := out.WriteByte('\n'); err != nil {
			return err
		}
		base += n
	}
	*cells = base
	for _, out := range outs {
		if err := out.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// countCells is the number of staged cells of one file of a staging table:
// its rows, or for a matrix staged as lines, its lines' non-empty cells.
func countCells(c *ch.Client, table, studyID, source string) (uint64, error) {
	lines, err := stagesLines(c, table)
	if err != nil {
		return 0, err
	}
	if !lines || !isMatrixTable(table) {
		return c.CountForFile(table, studyID, source)
	}
	out, err := c.Execute(fmt.Sprintf("SELECT sum(cells) FROM `%s`.`%s` WHERE study_id = '%s' AND source_file = '%s' FORMAT TabSeparated",
		c.Database(), linesTable(table), ch.SQLLit(studyID), ch.SQLLit(source)))
	if err != nil {
		if strings.Contains(err.Error(), "UNKNOWN_TABLE") {
			return 0, nil
		}
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(out), 10, 64)
}

// deleteFileRows deletes one file's staged rows from a staging table: for a
// matrix staged as lines, its lines and its sample list.
func deleteFileRows(c *ch.Client, table, studyID, source string) error {
	where := fmt.Sprintf("study_id = '%s' AND source_file = '%s'", ch.SQLLit(studyID), ch.SQLLit(source))
	lines, err := stagesLines(c, table)
	if err != nil {
		return err
	}
	if !lines || !isMatrixTable(table) {
		return c.DeleteWhere(table, where)
	}
	if err := c.DeleteWhere(linesTable(table), where); err != nil {
		return err
	}
	return c.DeleteWhere(matrixSamplesTable, where+fmt.Sprintf(" AND stage_table = '%s'", ch.SQLLit(table)))
}

// isMatrixTable reports whether a staging table holds matrix files.
func isMatrixTable(table string) bool {
	return strings.HasSuffix(table, "_matrix") && table != "gene_panel_matrix"
}
