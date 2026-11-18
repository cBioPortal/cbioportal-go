package transform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// A build of the portal database either rebuilds every study (a full
// build) or only the studies staged or removed since the previous build (a
// study build). Every table holding rows of many studies keeps each study's
// rows in a partition of their own (studyColumns), so a study build copies
// the previous build's tables (CLONE AS, which copies no data), drops the
// partitions of the changed studies and fills them again from those
// studies' staged rows alone: every statement of the build runs as in a
// full build, with ClickHouse reading only the changed studies' rows of the
// raw staging tables (additional_table_filters).
//
// A study build is used when the previous build was made by the same
// importer from the same reference data and shared gene panels (its
// fingerprint), and at most half of the studies changed.
//
// Internal ids follow cbioportal-core, whose ids only ever grow: a full
// build numbers studies, samples, patients, sample lists and segments in
// import order, from 1. A study build keeps the ids of the studies it does
// not change and numbers the changed studies' rows after the highest id
// still in use, in import order.

// studyColumns names, for each table built with a partition per study, the
// column holding the row's study. Snapshotted views (snapshotted) are built
// as __snapshot_<name>.
var studyColumns = map[string]string{
	"gene_key_map":                  "study_id",
	"variant_classification_filter": "study_id",
	"mutation_merge":                "study_id",
	"sample":                        "study_id",
	"patient":                       "study_id",
	"sample_profile":                "study_id",
	"clinical_sample_computed":      "study_id",
	"sample_to_gene_panel_derived":  "cancer_study_identifier",
	"sample_derived":                "cancer_study_identifier",
	"clinical_data_derived":         "cancer_study_identifier",
	"genomic_event_derived":         "cancer_study_identifier",
	"genetic_alteration_derived":    "cancer_study_identifier",
	"generic_assay_data_derived":    "cancer_study_identifier",
	"clinical_event_derived":        "cancer_study_identifier",
	"clinical_event_data_derived":   "cancer_study_identifier",
	"clinical_attribute_meta":       "study_id",
	"clinical_sample":               "study_id",
	"clinical_patient":              "study_id",
	"sample_list":                   "study_id",
	"mutation":                      "study_id",
	"cna":                           "study_id",
	"structural_variant":            "study_id",
	"seg":                           "study_id",
}

// snapshotPrefix names the table a snapshotted view is copied into.
const snapshotPrefix = "__snapshot_"

// builtName is the name a published table is built under: its own, or for
// a snapshotted view, that of its snapshot.
func builtName(name string) string {
	for _, m := range snapshotted {
		if m.name == name {
			return snapshotPrefix + name
		}
	}
	return name
}

const (
	// studyRanksTable holds every study's rank (its internal id) and the
	// import time it was built from.
	studyRanksTable = "study_rank"
	// buildTable holds the fingerprint of the build.
	buildTable = "_transform_build"
)

// Logf, when set, reports what a build does (which studies it rebuilds).
var Logf func(format string, args ...any)

func logf(format string, args ...any) {
	if Logf != nil {
		Logf(format, args...)
	}
}

// studyBuild is what a build does with the staged studies.
type studyBuild struct {
	fingerprint string
	// order lists the staged studies in import order, with their import
	// times.
	order []stagedStudy
	// previous holds the previous build's study ranks, by study.
	previous map[string]rankedStudy
	// partial is set for a study build: it rebuilds only changed (new or
	// staged again since the previous build) and removed studies.
	partial          bool
	changed, removed []string
}

type stagedStudy struct {
	StudyID    string `json:"study_id"`
	ImportTime string `json:"t"`
}

type rankedStudy struct {
	StudyID    string `json:"study_id"`
	ImportTime string `json:"import_time"`
	Rank       uint64 `json:"rank,string"`
}

// planBuild decides between a full and a study build; full asks for a full
// build.
func planBuild(c *client, rawDB, targetDB, refDB string, full bool) (*studyBuild, error) {
	b := &studyBuild{}
	var err error
	if b.fingerprint, err = buildFingerprint(c, rawDB, refDB); err != nil {
		return nil, err
	}
	text, err := c.Execute(fmt.Sprintf(
		"SELECT study_id, toString(max(import_time)) AS t FROM `%s`.cancer_study GROUP BY study_id "+
			"ORDER BY max(import_time), min(%s), study_id FORMAT JSONEachRow", rawDB, fileOrderSQL))
	if err != nil {
		return nil, err
	}
	if err := decodeRows(text, &b.order); err != nil {
		return nil, err
	}

	published, err := publishedTables(c, targetDB)
	if err != nil {
		return nil, err
	}
	if _, ok := published[studyRanksTable]; !ok {
		logf("transform: full build: no previous build")
		return b, nil
	}
	var ranks []rankedStudy
	text, err = c.Client.Execute(fmt.Sprintf(
		"SELECT study_id, toString(import_time) AS import_time, rank FROM `%s`.`%s` FORMAT JSONEachRow", targetDB, studyRanksTable))
	if err != nil {
		return nil, err
	}
	if err := decodeRows(text, &ranks); err != nil {
		return nil, err
	}
	b.previous = map[string]rankedStudy{}
	for _, r := range ranks {
		b.previous[r.StudyID] = r
	}
	staged := map[string]bool{}
	for _, s := range b.order {
		staged[s.StudyID] = true
		if p, ok := b.previous[s.StudyID]; !ok || p.ImportTime != s.ImportTime {
			b.changed = append(b.changed, s.StudyID)
		}
	}
	for id := range b.previous {
		if !staged[id] {
			b.removed = append(b.removed, id)
		}
	}
	slices.Sort(b.removed)

	reason := ""
	if full {
		reason = "a study build could not build every table"
	} else if _, ok := published[buildTable]; !ok {
		reason = "the previous build has no fingerprint"
	} else if 2*(len(b.changed)+len(b.removed)) > len(b.order) {
		reason = "more than half of the studies changed"
	} else if text, err := c.Client.Execute(fmt.Sprintf(
		"SELECT fingerprint FROM `%s`.`%s` FORMAT TabSeparated", targetDB, buildTable)); err != nil {
		return nil, err
	} else if strings.TrimSpace(text) != b.fingerprint {
		reason = "the importer, reference data or shared gene panels changed"
	}
	for name, col := range studyColumns {
		if key, ok := published[name]; reason == "" && ok && key != col {
			reason = name + " is not partitioned by study"
		}
	}
	if reason != "" {
		logf("transform: full build: %s", reason)
		return b, nil
	}
	b.partial = true
	logf("transform: rebuilding %d changed and %d removed of %d studies: %s", len(b.changed), len(b.removed), len(b.order),
		strings.Join(append(append([]string{}, b.changed...), b.removed...), ", "))
	return b, nil
}

// publishedTables maps each table of targetDB (not staged) to its
// partition key.
func publishedTables(c *client, targetDB string) (map[string]string, error) {
	text, err := c.Client.Execute(fmt.Sprintf(
		"SELECT name, partition_key FROM system.tables WHERE database = '%s' AND engine LIKE '%%MergeTree' "+
			"AND NOT startsWith(name, '%s') FORMAT TabSeparated", ch.SQLLit(targetDB), stagePrefix))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		name, key, _ := strings.Cut(line, "\t")
		out[name] = key
	}
	return out, nil
}

// buildFingerprint identifies what a build reads besides the studies' own
// rows: the importer itself, the reference database and the raw rows
// shared by all studies (an empty study_id, such as the shared gene panels). The
// reference genes the transform adds for the phosphoproteins of RPPA files
// (negative Entrez ids) come from the studies' own rows and are left out.
func buildFingerprint(c *client, rawDB, refDB string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n", executableHash(), rawDB, refDB)
	text, err := c.Client.Execute(fmt.Sprintf(
		"SELECT t.database, t.name, countIf(c.name = 'study_id'), countIf(c.name = 'entrez_gene_id') FROM system.tables t "+
			"LEFT JOIN system.columns c ON c.database = t.database AND c.table = t.name "+
			"WHERE t.database IN ('%s', '%s') AND t.engine LIKE '%%MergeTree' AND NOT startsWith(t.name, '_') "+
			"GROUP BY t.database, t.name ORDER BY t.database, t.name FORMAT TabSeparated", ch.SQLLit(rawDB), ch.SQLLit(refDB)))
	if err != nil {
		return "", err
	}
	var parts []string
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		where := ""
		switch {
		case f[0] == rawDB && f[2] == "0":
			continue // a study's own rows, keyed some other way
		case f[0] == rawDB:
			where = " WHERE study_id = ''"
		case f[3] != "0":
			where = " WHERE entrez_gene_id >= 0"
		}
		parts = append(parts, fmt.Sprintf("SELECT '%[1]s.%[2]s' AS t, count(), sum(cityHash64(*)) FROM `%[1]s`.`%[2]s`%[3]s",
			ch.SQLLit(f[0]), ch.SQLLit(f[1]), where))
	}
	if len(parts) > 0 {
		text, err := c.Client.Execute("SELECT * FROM (" + strings.Join(parts, " UNION ALL ") + ") ORDER BY t FORMAT TabSeparated")
		if err != nil {
			return "", err
		}
		io.WriteString(h, text)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var executableHash = sync.OnceValue(func() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
})

// writeBuildState writes the study ranks and the build's fingerprint. A
// full build numbers the studies in import order from 1; a study build
// keeps the ranks of the unchanged studies and numbers the changed ones
// after the highest previous rank.
func writeBuildState(c *client, targetDB string, b *studyBuild) error {
	var top uint64
	if b.partial {
		for _, r := range b.previous {
			top = max(top, r.Rank)
		}
	}
	changed := map[string]bool{}
	for _, id := range b.changed {
		changed[id] = true
	}
	values := make([]string, 0, len(b.order))
	for _, s := range b.order {
		rank := b.previous[s.StudyID].Rank
		if !b.partial || changed[s.StudyID] {
			top++
			rank = top
		}
		values = append(values, fmt.Sprintf("('%s', '%s', %d)", ch.SQLLit(s.StudyID), ch.SQLLit(s.ImportTime), rank))
	}
	stmts := []string{
		fmt.Sprintf("CREATE OR REPLACE TABLE `%s`.`%s` (study_id String, import_time DateTime64(6), rank UInt64) "+
			"ENGINE = MergeTree ORDER BY study_id", targetDB, studyRanksTable),
		fmt.Sprintf("CREATE OR REPLACE TABLE `%s`.`%s` (fingerprint String) ENGINE = MergeTree ORDER BY tuple()", targetDB, buildTable),
		fmt.Sprintf("INSERT INTO `%s`.`%s` VALUES ('%s')", targetDB, buildTable, b.fingerprint),
	}
	if len(values) > 0 {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO `%s`.`%s` VALUES %s", targetDB, studyRanksTable, strings.Join(values, ", ")))
	}
	for _, q := range stmts {
		if _, err := c.Execute(q); err != nil {
			return fmt.Errorf("writing the build state: %w", err)
		}
	}
	return nil
}

// partialBuild is the state of a study build.
type partialBuild struct {
	targetDB string
	// studies are the changed and removed studies, whose partitions
	// publishStudies replaces.
	studies []string
	// filtered reads only the rebuilt studies' rows of the raw staging
	// tables and of the portal tables built so far.
	filtered *ch.Client
}

// errFullBuild stops a study build that cannot be finished as one: a
// table's columns changed, or a table is new. Apply then builds every
// study.
var errFullBuild = errors.New("a study build cannot build this table")

// prepareStudyBuild creates, under its staged name, an empty table like
// each published table partitioned by study, for the changed studies' rows,
// and sets c up to read only those studies' staged rows.
func prepareStudyBuild(c *client, rawDB, targetDB string, b *studyBuild) error {
	studies := append(append([]string{}, b.changed...), b.removed...)
	published, err := publishedTables(c, targetDB)
	if err != nil {
		return err
	}
	var g errgroup.Group
	g.SetLimit(8)
	for name := range studyColumns {
		if _, ok := published[name]; !ok {
			continue
		}
		g.Go(func() error {
			if _, err := c.Client.Execute(fmt.Sprintf("CREATE TABLE `%[1]s`.`%[2]s` AS `%[1]s`.`%[3]s`",
				targetDB, stagePrefix+builtName(name), name)); err != nil {
				return fmt.Errorf("creating the staged %s: %w", name, err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	filtered, err := c.studyFilter(rawDB, targetDB, studies)
	if err != nil {
		return err
	}
	c.part = &partialBuild{
		targetDB: targetDB,
		studies:  studies,
		filtered: filtered,
	}
	return nil
}

// studyFilter returns a client that reads only the given studies' rows and
// the rows all studies share (study_id ”) of every raw table with a
// study_id column, and only the given studies' rows of the portal tables
// built so far (by their staged names).
func (c *client) studyFilter(rawDB, targetDB string, studies []string) (*ch.Client, error) {
	text, err := c.Client.Execute(fmt.Sprintf(
		"SELECT t.name FROM system.tables t INNER JOIN system.columns c ON c.database = t.database AND c.table = t.name "+
			"WHERE t.database = '%s' AND t.engine LIKE '%%MergeTree' AND c.name = 'study_id' ORDER BY t.name FORMAT TabSeparated",
		ch.SQLLit(rawDB)))
	if err != nil {
		return nil, err
	}
	ids := []string{"''"}
	for _, id := range studies {
		ids = append(ids, "'"+ch.SQLLit(id)+"'")
	}
	cond := "study_id IN (" + strings.Join(ids, ", ") + ")"
	var filters []string
	for name := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		if name != "" {
			filters = append(filters, fmt.Sprintf("'%s': '%s'", ch.SQLLit(rawDB+"."+name), ch.SQLLit(cond)))
		}
	}
	for name, col := range studyColumns {
		filters = append(filters, fmt.Sprintf("'%s': '%s'", ch.SQLLit(targetDB+"."+stagePrefix+builtName(name)),
			ch.SQLLit(strings.Replace(cond, "study_id", "`"+col+"`", 1))))
	}
	return c.Client.WithSettings(map[string]string{"additional_table_filters": "{" + strings.Join(filters, ", ") + "}"}), nil
}

// batchStudies is how many studies a full build fills a table with at a
// time: a portal with more studies is filled batch by batch, so a query
// needs the memory of one batch, not of every study.
var batchStudies = 50

// numberedAcrossStudies are the tables whose ids number the rows of every
// study in one sequence; a full build fills them in one go.
var numberedAcrossStudies = map[string]bool{"sample": true, "patient": true, "sample_list": true, "seg": true}

// prepareBatches sets the clients a full build fills the tables holding
// many studies with, one per batch of studies, when there is more than one
// batch.
func prepareBatches(c *client, rawDB, targetDB string, b *studyBuild) error {
	c.batches = nil
	if len(b.order) <= batchStudies {
		return nil
	}
	for start := 0; start < len(b.order); start += batchStudies {
		var ids []string
		for _, s := range b.order[start:min(start+batchStudies, len(b.order))] {
			ids = append(ids, s.StudyID)
		}
		filtered, err := c.studyFilter(rawDB, targetDB, ids)
		if err != nil {
			return err
		}
		c.batches = append(c.batches, filtered)
	}
	logf("transform: filling the tables in %d batches of up to %d studies", len(c.batches), batchStudies)
	return nil
}

// idOffset is the highest id (column col) of the published table name,
// after which a study build numbers the changed studies' rows; 0 in a full
// build.
func (c *client) idOffset(name, col string) uint64 {
	if c.part == nil {
		return 0
	}
	text, err := c.Client.Execute(fmt.Sprintf("SELECT max(`%s`) FROM `%s`.`%s` FORMAT TabSeparated",
		col, c.part.targetDB, name))
	if err != nil {
		return 0
	}
	var n uint64
	fmt.Sscan(strings.TrimSpace(text), &n)
	return n
}

// nextIDs numbers rows in the given order after offset.
func nextIDs(offset uint64, order string) string {
	return fmt.Sprintf("toUInt64(%d) + toUInt64(row_number() OVER (ORDER BY %s))", offset, order)
}

// fill builds table name of targetDB (its built name: see builtName) from
// sel, sorted by orderBy, with the projections. A table holding rows of
// many studies gets a partition per study. In a study build, such a table
// gets only the changed studies' rows, read from their staged rows alone,
// into a staged table like the published one; when sel's columns differ
// from the published table's (a changed study added columns) or none is
// published, fill returns errFullBuild. A full build of many studies fills
// such a table batch by batch (prepareBatches), unless its ids number
// every study's rows in one sequence.
func (c *client) fill(targetDB, name, orderBy, sel string, projections ...string) error {
	study := studyColumns[strings.TrimPrefix(name, snapshotPrefix)]
	if c.part != nil && study != "" {
		// The rows are inserted while their columns are checked: rows
		// with other columns are dropped with the staged table when the
		// build turns into a full build.
		var same bool
		var checkErr, insertErr error
		var wg sync.WaitGroup
		wg.Go(func() { same, checkErr = c.sameColumns(targetDB, name, sel) })
		wg.Go(func() {
			fc := *c
			fc.Client = c.part.filtered
			_, insertErr = fc.Execute(fmt.Sprintf("INSERT INTO `%s`.`%s` %s", targetDB, name, sel))
		})
		wg.Wait()
		switch {
		case checkErr != nil:
			return checkErr
		case !same:
			logf("transform: %s has other columns than the published table", name)
			return errFullBuild
		case insertErr != nil:
			return fmt.Errorf("filling table %s: %w", name, insertErr)
		}
		return nil
	}
	partition := ""
	if study != "" {
		partition = " PARTITION BY `" + study + "`"
	}
	batched := study != "" && len(c.batches) > 0 && !numberedAcrossStudies[strings.TrimPrefix(name, snapshotPrefix)]
	stmts := []string{fmt.Sprintf(
		"CREATE OR REPLACE TABLE `%s`.`%s` ENGINE = MergeTree%s ORDER BY %s AS %s", targetDB, name, partition, orderBy, sel)}
	if batched {
		// Each batch of studies is inserted on its own.
		stmts = []string{fmt.Sprintf(
			"CREATE OR REPLACE TABLE `%s`.`%s` ENGINE = MergeTree%s ORDER BY %s EMPTY AS %s", targetDB, name, partition, orderBy, sel)}
		for _, p := range projections {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE `%s`.`%s` ADD PROJECTION %s", targetDB, name, p))
		}
	} else if len(projections) > 0 {
		// A projection is added to the empty table so the insert builds
		// it with the rows.
		stmts = []string{fmt.Sprintf(
			"CREATE OR REPLACE TABLE `%s`.`%s` ENGINE = MergeTree%s ORDER BY %s EMPTY AS %s", targetDB, name, partition, orderBy, sel)}
		for _, p := range projections {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE `%s`.`%s` ADD PROJECTION %s", targetDB, name, p))
		}
		stmts = append(stmts, fmt.Sprintf("INSERT INTO `%s`.`%s` %s", targetDB, name, sel))
	}
	for _, q := range stmts {
		if _, err := c.Execute(q); err != nil {
			return fmt.Errorf("creating table %s: %w", name, err)
		}
	}
	if batched {
		for _, filtered := range c.batches {
			fc := *c
			fc.Client = filtered
			if _, err := fc.Execute(fmt.Sprintf("INSERT INTO `%s`.`%s` %s", targetDB, name, sel)); err != nil {
				return fmt.Errorf("filling table %s: %w", name, err)
			}
		}
	}
	return nil
}

// sameColumns reports whether the staged table name exists and has the
// columns sel returns, in order.
func (c *client) sameColumns(targetDB, name, sel string) (bool, error) {
	have, err := c.Client.Execute(fmt.Sprintf(
		"SELECT name, type FROM system.columns WHERE database = '%s' AND table = '%s' ORDER BY position FORMAT TabSeparated",
		ch.SQLLit(targetDB), ch.SQLLit(stagePrefix+name)))
	if err != nil || strings.TrimSpace(have) == "" {
		return false, err
	}
	described, err := c.Execute("DESCRIBE (" + sel + ") FORMAT TabSeparated")
	if err != nil {
		return false, err
	}
	var want []string
	for line := range strings.SplitSeq(strings.TrimSpace(described), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 2 {
			return false, nil
		}
		want = append(want, f[0]+"\t"+f[1])
	}
	return strings.Join(want, "\n") == strings.TrimSpace(have), nil
}

// decodeRows decodes JSONEachRow text into rows.
func decodeRows[T any](text string, rows *[]T) error {
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		var r T
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return err
		}
		*rows = append(*rows, r)
	}
	return nil
}

// studyRanksSQL reads the ranks (study_id, study_rank).
func studyRanksSQL(targetDB string) string {
	return fmt.Sprintf("SELECT study_id, rank AS study_rank FROM `%s`.`%s`", targetDB, studyRanksTable)
}

// publishStudies moves the changed studies' rows of each staged table
// partitioned by study into the published table: REPLACE PARTITION swaps
// in a study's partition of the staged table, or removes the study's rows
// where the staged table has none. The staged tables are left to
// dropStaged.
func publishStudies(c *client, p *partialBuild) error {
	objs, err := stagedObjects(c, p.targetDB)
	if err != nil {
		return err
	}
	replaces := make([]string, len(p.studies))
	for i, id := range p.studies {
		replaces[i] = fmt.Sprintf("REPLACE PARTITION '%s' FROM `%s`.`%%s`", ch.SQLLit(id), p.targetDB)
	}
	var g errgroup.Group
	g.SetLimit(8)
	for _, o := range objs {
		name := strings.TrimPrefix(o.name, stagePrefix)
		if o.engine == "View" || studyColumns[name] == "" || len(replaces) == 0 {
			continue
		}
		g.Go(func() error {
			cmds := make([]string, len(replaces))
			for i, r := range replaces {
				cmds[i] = fmt.Sprintf(r, o.name)
			}
			if _, err := c.Client.Execute(fmt.Sprintf("ALTER TABLE `%s`.`%s` %s", p.targetDB, name, strings.Join(cmds, ", "))); err != nil {
				return fmt.Errorf("publishing %s: %w", name, err)
			}
			return nil
		})
	}
	return g.Wait()
}
