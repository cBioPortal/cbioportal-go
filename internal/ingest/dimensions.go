package ingest

import (
	"fmt"
	"os"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

var studyMetaFields = []string{
	"type_of_cancer", "name", "short_name", "description", "groups",
	"pmid", "citation", "reference_genome", "add_global_case_list",
}

func ingestStudyMeta(c *ch.Client, s *parser.Study) error {
	if len(s.Meta) == 0 {
		return nil
	}
	table := "cancer_study"
	cols := append([]string{"study_id"}, studyMetaFields...)
	existing, err := c.TableColumns(table)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		defs := make([]string, len(cols))
		for i, col := range cols {
			defs[i] = fmt.Sprintf("`%s` String", col)
		}
		if _, err := c.Execute(fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS `%s`.`%s` (%s) ENGINE = MergeTree ORDER BY study_id",
			c.Database(), table, strings.Join(defs, ", "))); err != nil {
			return err
		}
	}
	// import_time orders the studies as cbioportal-core numbers them: by
	// import, a re-imported study last.
	if !existing["import_time"] {
		if _, err := c.Execute(fmt.Sprintf(
			"ALTER TABLE `%s`.`%s` ADD COLUMN IF NOT EXISTS import_time DateTime64(6)", c.Database(), table)); err != nil {
			return err
		}
	}
	c.DeleteWhere(table, fmt.Sprintf("study_id = '%s'", ch.SQLLit(s.StudyID)))

	values := []string{s.StudyID}
	for _, f := range studyMetaFields {
		values = append(values, s.Meta[f])
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + ch.SQLLit(v) + "'"
	}
	_, err = c.Execute(fmt.Sprintf("INSERT INTO `%s`.`%s` (%s, import_time) SELECT %s, now64(6)",
		c.Database(), table, backtick(cols), strings.Join(quoted, ", ")))
	return err
}

func ingestProfiles(c *ch.Client, s *parser.Study) error {
	table := "genetic_profile"
	cols := []string{"molecular_profile_id", "study_id", "profile_stable_id",
		"alteration_type", "datatype", "name", "description", "show_in_analysis", "generic_assay_type", "namespaces",
		"gene_panel", "patient_level", "pivot_threshold", "sort_order"}
	existing, err := c.TableColumns(table)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		defs := make([]string, len(cols))
		for i, col := range cols {
			defs[i] = fmt.Sprintf("`%s` String", col)
		}
		if _, err := c.Execute(fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS `%s`.`%s` (%s) ENGINE = MergeTree ORDER BY molecular_profile_id",
			c.Database(), table, strings.Join(defs, ", "))); err != nil {
			return err
		}
	} else {
		// Schema evolution: add columns absent from an older table.
		for _, col := range cols {
			if !existing[col] {
				if _, err := c.Execute(fmt.Sprintf(
					"ALTER TABLE `%s`.`%s` ADD COLUMN IF NOT EXISTS `%s` String", c.Database(), table, col)); err != nil {
					return err
				}
			}
		}
	}
	c.DeleteWhere(table, fmt.Sprintf("study_id = '%s'", ch.SQLLit(s.StudyID)))

	for i := range s.Files {
		f := &s.Files[i]
		if !f.IsMolecularProfile() {
			continue
		}
		mpid := s.StudyID + "_" + f.StableID
		values := []string{mpid, s.StudyID, f.StableID, f.AlterationType, f.Datatype, f.ProfileName, f.ProfileDescription, f.ShowInAnalysis, f.GenericAssayType, f.Namespaces,
			f.GenePanel, f.PatientLevel, f.PivotThreshold, f.SortOrder}
		quoted := make([]string, len(values))
		for j, v := range values {
			quoted[j] = "'" + ch.SQLLit(v) + "'"
		}
		if _, err := c.Execute(fmt.Sprintf("INSERT INTO `%s`.`%s` (%s) SELECT %s",
			c.Database(), table, backtick(cols), strings.Join(quoted, ", "))); err != nil {
			return err
		}
	}
	return nil
}

// ingestProfileSamples records, per molecular profile data file, the case ids
// the file declares independently of its data cells: every sample (or, for
// patient-level generic assay, patient) column of a matrix header, and the ids
// on a MAF's `#sequenced_samples:` header lines. The transform's sample_profile
// view unions these with the ids present in the record tables to reproduce
// cbioportal-core's sample_profile membership. kind is "header" or
// "sequenced_samples".
func ingestProfileSamples(c *ch.Client, s *parser.Study) error {
	table := "profile_sample"
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.`%s` (study_id String, source_file String, profile_stable_id String, kind String, sample_id String) ENGINE = MergeTree ORDER BY (study_id, profile_stable_id)",
		c.Database(), table)); err != nil {
		return err
	}
	c.DeleteWhere(table, fmt.Sprintf("study_id = '%s'", ch.SQLLit(s.StudyID)))

	var b strings.Builder
	for i := range s.Files {
		f := &s.Files[i]
		if !f.IsMolecularProfile() {
			continue
		}
		var kind string
		var ids []string
		switch {
		case f.Layout == parser.Matrix:
			h, err := parser.ReadHeader(f.DataPath)
			if err != nil {
				return err
			}
			kind, ids = "header", f.MatrixShape(h.Columns).SampleColumns
		case f.StageSlug == "mutation_extended" || f.StageSlug == "mutation_uncalled":
			seq, err := parser.ReadSequencedSamples(f.DataPath)
			if err != nil {
				return err
			}
			kind, ids = "sequenced_samples", seq
		default:
			continue
		}
		for _, id := range ids {
			if id == "" || strings.ContainsAny(id, "\t\n\r") {
				continue
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", s.StudyID, fileName(f.DataPath), f.StableID, kind, id)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return c.InsertStream(
		fmt.Sprintf("INSERT INTO `%s`.`%s` FORMAT TabSeparatedRaw", c.Database(), table),
		strings.NewReader(b.String()))
}

func ensureGenePanelTable(c *ch.Client) error {
	_, err := c.Execute(fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.gene_panel (study_id String, gene_panel_id String, description String, hugo_symbol String) ENGINE = MergeTree ORDER BY (study_id, gene_panel_id)",
		c.Database()))
	return err
}

// ingestPanelFile parses one gene-panel definition (stable_id / description /
// whitespace-delimited gene_list) and inserts one row per gene under studyID.
func ingestPanelFile(c *ch.Client, studyID, path string) (int, error) {
	meta, err := parser.ReadMeta(path)
	if err != nil {
		return 0, err
	}
	panelID := meta.Get("stable_id")
	if panelID == "" {
		return 0, nil
	}
	desc := meta.Get("description")
	genes := strings.Fields(meta.Get("gene_list"))
	if len(genes) == 0 {
		return 0, nil
	}
	var b strings.Builder
	for _, g := range genes {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", studyID, panelID, desc, g)
	}
	if err := c.InsertStream(
		fmt.Sprintf("INSERT INTO `%s`.gene_panel FORMAT TabSeparatedRaw", c.Database()),
		strings.NewReader(b.String())); err != nil {
		return 0, err
	}
	return len(genes), nil
}

func ingestGenePanels(c *ch.Client, s *parser.Study) error {
	if err := ensureGenePanelTable(c); err != nil {
		return err
	}
	c.DeleteWhere("gene_panel", fmt.Sprintf("study_id = '%s'", ch.SQLLit(s.StudyID)))
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "data_gene_panel_") && strings.HasSuffix(n, ".txt") && !strings.Contains(n, "_matrix") {
			if _, err := ingestPanelFile(c, s.StudyID, s.Dir+"/"+n); err != nil {
				return err
			}
		}
	}
	return nil
}

// LoadGenePanelsDir loads a directory of global gene-panel definitions under a
// blank study id. Returns (panels, genes).
func LoadGenePanelsDir(c *ch.Client, dir string) (int, int, error) {
	if err := c.CreateDatabase(); err != nil {
		return 0, 0, err
	}
	if err := ensureGenePanelTable(c); err != nil {
		return 0, 0, err
	}
	c.DeleteWhere("gene_panel", "study_id = ''")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	panels, genes := 0, 0
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "data_gene_panel_") && strings.HasSuffix(n, ".txt") && !strings.Contains(n, "_matrix") {
			ng, err := ingestPanelFile(c, "", dir+"/"+n)
			if err != nil {
				return 0, 0, err
			}
			if ng > 0 {
				panels++
				genes += ng
			}
		}
	}
	return panels, genes, nil
}

// IngestCaseLists replaces a study's staged case lists only, leaving its
// data files' staging rows untouched.
func IngestCaseLists(c *ch.Client, s *parser.Study) error {
	if err := c.CreateDatabase(); err != nil {
		return err
	}
	return ingestCaseLists(c, s)
}

// ingestCaseLists loads the study's case_lists/*.txt (sample lists) into a
// sample_list staging table: one row per (list, sample).
func ingestCaseLists(c *ch.Client, s *parser.Study) error {
	table := "sample_list"
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.`%s` (study_id String, list_id String, category String, name String, description String, sample_id String) ENGINE = MergeTree ORDER BY (study_id, list_id)",
		c.Database(), table)); err != nil {
		return err
	}
	c.DeleteWhere(table, fmt.Sprintf("study_id = '%s'", ch.SQLLit(s.StudyID)))

	dir := s.Dir + "/case_lists"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // no case_lists dir is fine
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		meta, err := parser.ReadMeta(dir + "/" + e.Name())
		if err != nil {
			return err
		}
		listID := meta.Get("stable_id")
		if listID == "" {
			continue
		}
		samples := strings.FieldsFunc(meta.Get("case_list_ids"), func(r rune) bool { return r == '\t' })
		var b strings.Builder
		for _, s2 := range samples {
			s2 = strings.TrimSpace(s2)
			if s2 == "" {
				continue
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\n",
				s.StudyID, listID, meta.Get("case_list_category"),
				clean.Replace(meta.Get("case_list_name")), clean.Replace(meta.Get("case_list_description")), s2)
		}
		if b.Len() == 0 {
			continue
		}
		if err := c.InsertStream(
			fmt.Sprintf("INSERT INTO `%s`.`%s` FORMAT TabSeparatedRaw", c.Database(), table),
			strings.NewReader(b.String())); err != nil {
			return err
		}
	}
	return nil
}

func ingestClinicalMeta(c *ch.Client, s *parser.Study) error {
	table := "clinical_attribute_meta"
	if _, err := c.Execute(fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS `%s`.`%s` (study_id String, attr_id String, display_name String, description String, datatype String, priority String, patient_attribute UInt8) ENGINE = MergeTree ORDER BY (study_id, attr_id)",
		c.Database(), table)); err != nil {
		return err
	}
	c.DeleteWhere(table, fmt.Sprintf("study_id = '%s'", ch.SQLLit(s.StudyID)))

	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")
	for i := range s.Files {
		f := &s.Files[i]
		var patientDefault bool
		switch f.StageSlug {
		case "clinical_patient":
			patientDefault = true
		case "clinical_sample", "clinical_mixed":
			patientDefault = false
		default:
			continue
		}
		attrs, err := parser.ParseAttributeMeta(f.DataPath, patientDefault)
		if err != nil {
			return err
		}
		if len(attrs) == 0 {
			continue
		}
		var b strings.Builder
		for _, a := range attrs {
			pa := 0
			if a.PatientAttribute {
				pa = 1
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
				s.StudyID, a.AttrID, clean.Replace(a.DisplayName), clean.Replace(a.Description),
				a.Datatype, a.Priority, pa)
		}
		if err := c.InsertStream(
			fmt.Sprintf("INSERT INTO `%s`.`%s` FORMAT TabSeparatedRaw", c.Database(), table),
			strings.NewReader(b.String())); err != nil {
			return err
		}
	}
	return nil
}
