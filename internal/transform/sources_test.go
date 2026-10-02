package transform

import "testing"

func TestTableRef(t *testing.T) {
	re := tableRef("raw", "timeline")
	for _, sql := range []string{
		"FROM `raw`.timeline t",
		"FROM `raw`.`timeline` t",
		"FROM raw.timeline t",
		"(SELECT * FROM raw.timeline)",
	} {
		if got := re.ReplaceAllLiteralString(sql, "X"); got == sql {
			t.Errorf("%q: reference not replaced", sql)
		}
	}
	for _, sql := range []string{
		"FROM `raw`.timeline_extra t",
		"FROM `rawx`.timeline t",
		"FROM xraw.timeline t",
		"FROM `target`.timeline t",
		"WHERE database = 'raw' AND table = 'timeline'",
	} {
		if got := re.ReplaceAllLiteralString(sql, "X"); got != sql {
			t.Errorf("%q: unexpectedly replaced: %q", sql, got)
		}
	}
}
