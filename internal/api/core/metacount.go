package core

import (
	"context"
	"net/http"
)

// MetaStudyFilter is the set of studies a projection=META count covers: the
// studies whose records the list endpoint itself returns. Study
// authorization puts one on the list requests that leave the records of
// unreadable studies out, so that their count says the same thing as their
// list (CancerStudyPermissionEvaluator, and SampleController, which passes
// the readable study ids into its count query).
type MetaStudyFilter struct {
	ids  []string
	keep map[string]bool
}

// NewMetaStudyFilter returns the filter that counts the studies in ids.
func NewMetaStudyFilter(ids []string) *MetaStudyFilter {
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	return &MetaStudyFilter{ids: ids, keep: keep}
}

// IDs are the studies that count, for a count made in ClickHouse.
func (f *MetaStudyFilter) IDs() []string {
	if f == nil {
		return nil
	}
	return f.ids
}

// Keep reports whether a record of the study counts. Records without a study
// count, as do all records without a filter.
func (f *MetaStudyFilter) Keep(studyID string) bool {
	return f == nil || studyID == "" || f.keep[studyID]
}

type metaStudyKey struct{}

// WithMetaStudyFilter records which studies the request's projection=META
// count covers.
func WithMetaStudyFilter(r *http.Request, f *MetaStudyFilter) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), metaStudyKey{}, f))
}

// MetaStudyFilterOf returns the request's filter, or nil when every study
// counts.
func MetaStudyFilterOf(r *http.Request) *MetaStudyFilter {
	f, _ := r.Context().Value(metaStudyKey{}).(*MetaStudyFilter)
	return f
}

// MetaCountSQL counts the records sel selects, for a projection=META answer.
// With a filter on the request only the records of its studies count, and
// sel must then select each record's study as studyId.
func MetaCountSQL(r *http.Request, b Binder, sel string) string {
	sql := "SELECT count() AS n FROM (" + sel + ")"
	if f := MetaStudyFilterOf(r); f != nil {
		sql += " WHERE studyId IN " + b.Strs(f.IDs())
	}
	return sql
}
