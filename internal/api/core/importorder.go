package core

import (
	"cmp"
	"slices"
)

// cbioportal-core numbers studies, patients, samples and segments as it
// imports them (internal ids), and many of the Java backend's queries return
// rows in that order: they have no ORDER BY, or order by the internal id, and
// ClickHouse reads the tables in their sort key order. The transform carries
// the internal ids (internal_id of cancer_study, patient, sample and the
// clinical tables, seg.seg_id, sample_list.list_internal_id); on a database
// without them the API falls back to stable id order.

// ImportOrdered reports whether the database carries the internal ids.
func (s *State) ImportOrdered() bool {
	return s.ModelHasColumn("sample", "internal_id")
}

// SampleOrder orders rows joined to the sample table (alias alias) as the
// Java backend's unsorted sample queries return them: by internal id, then by
// idCol (the sample id), which alone orders them where the database carries
// no internal ids.
func (s *State) SampleOrder(alias, idCol string) string {
	if s.ImportOrdered() {
		return alias + ".internal_id, " + idCol
	}
	return idCol
}

// StudyRanks maps each study to its internal id (cancer_study_id), or is nil
// where the database carries none.
func (s *State) StudyRanks() map[string]uint64 {
	if !s.ModelHasColumn("cancer_study", "internal_id") {
		return nil
	}
	type row struct {
		StudyID    string `json:"study_id"`
		InternalID uint64 `json:"internal_id"`
	}
	rows, err := CachedQuery[row](s, "SELECT study_id, internal_id FROM cancer_study")
	if err != nil {
		return nil
	}
	out := make(map[string]uint64, len(rows))
	for _, r := range rows {
		out[r.StudyID] = r.InternalID
	}
	return out
}

// StudyOrder is an ORDER BY expression over a study id column that sorts
// studies as cancer_study_id does (import order), or by study id where the
// database carries no internal ids.
func (s *State) StudyOrder(col string) string {
	ranks := s.StudyRanks()
	if len(ranks) == 0 {
		return col
	}
	ids := make([]string, 0, len(ranks))
	for id := range ranks {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return cmp.Compare(ranks[a], ranks[b]) })
	return "indexOf([" + InList(ids) + "], " + col + "), " + col
}
