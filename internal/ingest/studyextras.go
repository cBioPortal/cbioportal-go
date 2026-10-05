package ingest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

// ingestStudyTags converts the study's tags file (meta_study `tags_file`,
// YAML) to JSON and stores it in cancer_study_tags, one row per study, like
// cbioportal-core's CancerStudyTagsReader.
func ingestStudyTags(c *ch.Client, s *parser.Study) error {
	table := "cancer_study_tags"
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.`%s` (study_id String, source_file String, tags String) ENGINE = MergeTree ORDER BY study_id",
		c.Database(), table)); err != nil {
		return err
	}
	c.DeleteWhere(table, fmt.Sprintf("study_id = '%s'", ch.SQLLit(s.StudyID)))
	name := s.Meta["tags_file"]
	if name == "" {
		return nil
	}
	tags, err := parser.ReadYAMLAsJSON(filepath.Join(s.Dir, name))
	if err != nil {
		return err
	}
	_, err = c.Execute(fmt.Sprintf("INSERT INTO `%s`.`%s` (study_id, source_file, tags) SELECT '%s', '%s', '%s'",
		c.Database(), table, ch.SQLLit(s.StudyID), ch.SQLLit(name), ch.SQLLit(tags)))
	return err
}

// mutSigColumns are the staged MutSig columns, extracted by mutSigRows.
var mutSigColumns = []string{"rank", "gene", "num_bases_covered", "num_mutations", "p_value", "q_value"}

// mutSigRows reads a MutSig file the way cbioportal-core's MutSigReader does:
// columns are located by header name (rank; gene; N or Nnon = bases covered;
// n or nnon = mutations; p; q — the last matching header wins), a missing rank
// column means rank is the 1-based line number, and '<' is dropped from p/q.
// It returns TSV rows in mutSigColumns order.
func mutSigRows(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	if !sc.Scan() {
		return nil, sc.Err()
	}
	rankF, geneF, basesF, mutsF, pF, qF := -1, -1, -1, -1, -1, -1
	for i, name := range strings.Split(strings.TrimRight(sc.Text(), "\r"), "\t") {
		switch {
		case name == "rank":
			rankF = i
		case strings.EqualFold(name, "gene"):
			geneF = i
		case name == "N" || name == "Nnon":
			basesF = i
		case name == "n" || name == "nnon":
			mutsF = i
		case strings.EqualFold(name, "p"):
			pF = i
		case strings.EqualFold(name, "q"):
			qF = i
		}
	}
	if geneF < 0 || basesF < 0 || mutsF < 0 || pF < 0 || qF < 0 {
		return nil, fmt.Errorf("%s: one or more of the fields [gene, N, n, p, q] are missing", path)
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", "", "\\", "")
	field := func(parts []string, i int) string {
		if i < 0 || i >= len(parts) {
			return ""
		}
		return clean.Replace(strings.TrimSpace(parts[i]))
	}
	var rows []string
	line := 0
	for sc.Scan() {
		text := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(text) == "" {
			continue
		}
		line++
		parts := strings.Split(text, "\t")
		rank := strconv.Itoa(line)
		if rankF >= 0 {
			rank = field(parts, rankF)
		}
		rows = append(rows, strings.Join([]string{
			rank, field(parts, geneF), field(parts, basesF), field(parts, mutsF),
			strings.ReplaceAll(field(parts, pF), "<", ""), strings.ReplaceAll(field(parts, qF), "<", ""),
		}, "\t"))
	}
	return rows, sc.Err()
}

// ingestMutSig stages every MUTSIG file of the study.
func ingestMutSig(c *ch.Client, s *parser.Study) ([]FileStat, error) {
	var stats []FileStat
	for i := range s.Files {
		f := &s.Files[i]
		if f.StageSlug != "mutsig" {
			continue
		}
		rows, err := mutSigRows(f.DataPath)
		if err != nil {
			return nil, err
		}
		deleteExisting(c, f, s.StudyID)
		emitted := uint64(len(rows))
		var body strings.Builder
		for j, r := range rows {
			fmt.Fprintf(&body, "%d\t%s\n", j+1, r)
		}
		st, err := runInsert(c, f, s.StudyID, i+1, mutSigColumns, strings.NewReader(body.String()), &emitted)
		if err != nil {
			return nil, err
		}
		stats = append(stats, st)
	}
	return stats, nil
}

// ingestGenericAssayEntities stores the study's generic assay entities as
// cbioportal-core's ImportGenericAssayEntity does: per profile, the entities
// of its file (generic_assay_profile_entity), and per entity the properties
// listed in the meta file's generic_entity_meta_properties, keyed as listed,
// for the columns the file has (generic_assay_entity_meta). Runs after the
// matrices are staged; the values come from generic_assay_matrix.
func ingestGenericAssayEntities(c *ch.Client, s *parser.Study) error {
	db := c.Database()
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.generic_assay_profile_entity (study_id String, profile_stable_id String, entity_stable_id String) "+
			"ENGINE = MergeTree ORDER BY (study_id, profile_stable_id, entity_stable_id)", db)); err != nil {
		return err
	}
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.generic_assay_entity_meta (study_id String, entity_stable_id String, entity_type String, "+
			"properties Map(String, String)) ENGINE = MergeTree ORDER BY (entity_stable_id, study_id)", db)); err != nil {
		return err
	}
	where := fmt.Sprintf("study_id = '%s'", ch.SQLLit(s.StudyID))
	c.DeleteWhere("generic_assay_profile_entity", where)
	c.DeleteWhere("generic_assay_entity_meta", where)
	for i := range s.Files {
		f := &s.Files[i]
		if f.StageSlug != "generic_assay_matrix" {
			continue
		}
		h, err := parser.ReadHeader(f.DataPath)
		if err != nil {
			return err
		}
		staged := parser.SanitizeColumns(h.Columns)
		var kv []string
		for _, p := range f.EntityMetaProperties {
			for j, col := range h.Columns {
				if strings.EqualFold(strings.TrimSpace(col), p) {
					kv = append(kv, fmt.Sprintf("'%s', any(`%s`)", ch.SQLLit(p), staged[j]))
					break
				}
			}
		}
		props := "map()"
		if len(kv) > 0 {
			props = "map(" + strings.Join(kv, ", ") + ")"
		}
		cond := fmt.Sprintf("study_id = '%s' AND profile_stable_id = '%s' AND source_file = '%s'",
			ch.SQLLit(s.StudyID), ch.SQLLit(f.StableID), ch.SQLLit(fileName(f.DataPath)))
		if _, err := c.Execute(fmt.Sprintf(
			"INSERT INTO `%[1]s`.generic_assay_profile_entity SELECT DISTINCT study_id, profile_stable_id, entity_stable_id "+
				"FROM `%[1]s`.generic_assay_matrix WHERE %[2]s", db, cond)); err != nil {
			return err
		}
		if _, err := c.Execute(fmt.Sprintf(
			"INSERT INTO `%[1]s`.generic_assay_entity_meta SELECT study_id, entity_stable_id, 'GENERIC_ASSAY', %[3]s "+
				"FROM `%[1]s`.generic_assay_matrix WHERE %[2]s GROUP BY study_id, entity_stable_id", db, cond, props)); err != nil {
			return err
		}
	}
	return nil
}
