package transform

import (
	_ "embed"
	"fmt"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// oncotreeTSV is a copy of OncoTree's oncotree_latest_stable release, the
// version cBioPortal's production import uses for the public portal: one
// line per tumor type with its code, name and main type. To refresh it, run
// `make oncotree`.
//
//go:embed oncotree.tsv
var oncotreeTSV string

// oncotreeSQL selects the OncoTree tumor types as (code, name, main_type).
func oncotreeSQL() string {
	var rows []string
	for _, line := range strings.Split(oncotreeTSV, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		rows = append(rows, fmt.Sprintf("('%s', '%s', '%s')", ch.SQLLit(f[0]), ch.SQLLit(f[1]), ch.SQLLit(f[2])))
	}
	return "SELECT * FROM values('code String, name String, main_type String', " + strings.Join(rows, ", ") + ")"
}

// cancerTypesFromOncotreeSQL selects the CANCER_TYPE and
// CANCER_TYPE_DETAILED values cBioPortal's production import adds to a
// study whose sample file has ONCOTREE_CODE but not these columns: the
// code's OncoTree main type and name. Each column is added only when the
// study's clinical files lack it, and values already in a file are kept. A
// sample whose code is empty or not in OncoTree gets NA, which is not stored.
// base selects the staged sample values as (study_id, sample_id, attr_id,
// attr_value).
func cancerTypesFromOncotreeSQL(rawDB, base string) string {
	return fmt.Sprintf(
		"SELECT b.study_id AS study_id, b.sample_id AS sample_id, a AS attr_id, if(a = 'CANCER_TYPE', o.main_type, o.name) AS attr_value "+
			"FROM (%[2]s) b INNER JOIN (%[3]s) o ON o.code = b.attr_value ARRAY JOIN ['CANCER_TYPE', 'CANCER_TYPE_DETAILED'] AS a "+
			"WHERE b.attr_id = 'ONCOTREE_CODE' AND (b.study_id, a) NOT IN (SELECT study_id, attr_id FROM `%[1]s`.clinical_attribute_meta)",
		rawDB, base, oncotreeSQL())
}

// cancerTypesFromOncotreeMetaSQL selects the clinical attribute definitions
// of the columns cancerTypesFromOncotreeSQL adds, as production's import
// writes them into the file's header.
func cancerTypesFromOncotreeMetaSQL(rawDB string) string {
	return fmt.Sprintf(
		"SELECT m.study_id AS study_id, a.1 AS clinical_attribute_id, a.2 AS display_name, a.2 AS description, 'STRING' AS datatype, '1' AS priority, 0 AS patient_attribute "+
			"FROM (SELECT DISTINCT study_id FROM `%[1]s`.clinical_attribute_meta WHERE attr_id = 'ONCOTREE_CODE') m "+
			"ARRAY JOIN [('CANCER_TYPE', 'Cancer Type'), ('CANCER_TYPE_DETAILED', 'Cancer Type Detailed')] AS a", rawDB)
}

// usesProductionRules reports whether rawDB was staged with cBioPortal's
// production import rules (parser.Study.UseProductionRules).
func usesProductionRules(c *client, rawDB string) (bool, error) {
	if ok, err := c.TableExists("import_rules"); err != nil || !ok {
		return false, err
	}
	n, err := c.Execute(fmt.Sprintf("SELECT count() FROM `%s`.import_rules WHERE rule = 'production'", rawDB))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(n) != "0", nil
}
