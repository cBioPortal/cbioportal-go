package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

// stagedFilesTable lists, per study, every data file staged in the raw
// database: the staging table its rows went to, a fingerprint of what made
// those rows and how many there are. A re-ingest skips a file whose
// fingerprint and row count still match, and finds removed files here
// instead of scanning every staging table.
const stagedFilesTable = "_staged_files"

// stageFormat versions how ingest turns a file into rows. Raise it whenever
// that changes (parsing, sample ids, provenance or order columns), so every
// file is staged again on its next ingest.
const stageFormat = 1

type stagedFile struct {
	SourceFile  string `json:"source_file"`
	StageTable  string `json:"stage_table"`
	Fingerprint string `json:"fingerprint"`
	Rows        uint64 `json:"rows"`
}

func (f stagedFile) key() string { return f.StageTable + "\x00" + f.SourceFile }

func stagedKey(table, source string) string { return table + "\x00" + source }

func ensureStagedFiles(c *ch.Client) error {
	err := c.EnsureTable(stagedFilesTable, fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.`%s` (study_id String, source_file String, stage_table String, "+
			"fingerprint String, `rows` UInt64, staged_at DateTime64(3)) "+
			"ENGINE = ReplacingMergeTree(staged_at) ORDER BY (study_id, stage_table, source_file)",
		c.Database(), stagedFilesTable))
	return err
}

// readStagedFiles returns the study's listed files by stage table and file.
func readStagedFiles(c *ch.Client, studyID string) (map[string]stagedFile, error) {
	rows, err := ch.QueryP[stagedFile](c, fmt.Sprintf(
		"SELECT source_file, stage_table, fingerprint, `rows` FROM `%s`.`%s` FINAL WHERE study_id = {study:String}",
		c.Database(), stagedFilesTable), ch.Params{"study": studyID})
	if err != nil {
		return nil, err
	}
	out := make(map[string]stagedFile, len(rows))
	for _, r := range rows {
		out[r.key()] = r
	}
	return out, nil
}

func recordStagedFile(c *ch.Client, studyID string, f stagedFile) error {
	_, err := c.Execute(fmt.Sprintf(
		"INSERT INTO `%s`.`%s` (study_id, source_file, stage_table, fingerprint, `rows`, staged_at) "+
			"VALUES ('%s', '%s', '%s', '%s', %d, now64(3))",
		c.Database(), stagedFilesTable, ch.SQLLit(studyID), ch.SQLLit(f.SourceFile), ch.SQLLit(f.StageTable),
		ch.SQLLit(f.Fingerprint), f.Rows))
	return err
}

func forgetStagedFile(c *ch.Client, studyID string, f stagedFile) error {
	return c.DeleteWhere(stagedFilesTable, fmt.Sprintf("study_id = '%s' AND stage_table = '%s' AND source_file = '%s'",
		ch.SQLLit(studyID), ch.SQLLit(f.StageTable), ch.SQLLit(f.SourceFile)))
}

// fingerprint hashes everything a file's staged rows are made from: the
// staging format, the table, the file's position among the study's files
// (its rows' file_num), its provenance, and the meta and data files' bytes.
func fingerprint(studyID string, fileNum int, f *parser.DataFile) (string, error) {
	h := sha256.New()
	prov := provenance(studyID, f)
	fmt.Fprintf(h, "format %d\ntable %s\nfile_num %d\nprovenance %s\n",
		stageFormat, f.StageSlug, fileNum, strings.Join(prov[:], "\x1f"))
	for _, path := range []string{f.MetaPath, f.DataPath} {
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n", fileName(path))
		_, err = io.Copy(h, file)
		file.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unchanged reports whether a file's staged rows are still current: it is
// listed with the same fingerprint, and its table holds as many of its rows
// as were staged.
func unchanged(c *ch.Client, studyID string, listed map[string]stagedFile, table, source, fp string) (bool, error) {
	l, ok := listed[stagedKey(table, source)]
	if !ok || l.Fingerprint != fp {
		return false, nil
	}
	n, err := c.CountForFile(table, studyID, source)
	if err != nil {
		return false, err
	}
	return n == l.Rows, nil
}

// pruneListedFiles deletes the staged rows, recorded headers and list entries
// of the study's listed files that are no longer in the study directory.
// It reports whether a generic assay file was among them.
func pruneListedFiles(c *ch.Client, studyID string, listed map[string]stagedFile, current map[string]bool) (bool, error) {
	genericAssay := false
	for _, f := range listed {
		if current[f.SourceFile] {
			continue
		}
		where := fmt.Sprintf("study_id = '%s' AND source_file = '%s'", ch.SQLLit(studyID), ch.SQLLit(f.SourceFile))
		if err := c.DeleteWhere(f.StageTable, where); err != nil {
			return false, err
		}
		if headerTables[f.StageTable] {
			if err := c.DeleteWhere(sourceHeaderTable, where+fmt.Sprintf(" AND table_name = '%s'", ch.SQLLit(f.StageTable))); err != nil {
				return false, err
			}
		}
		if err := forgetStagedFile(c, studyID, f); err != nil {
			return false, err
		}
		if f.StageTable == "generic_assay_matrix" {
			genericAssay = true
		}
	}
	return genericAssay, nil
}
