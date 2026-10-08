package studyview

import (
	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// scope holds a read of the filtered samples' rows to their studies
// (scopeStudies), the leading key of the study tables and *_derived views.
func (f studyViewFilter) scope() core.Scope { return core.ByStudies(f.scopeStudies()) }

// filterFrom is the sample_derived rows of the samples a study-view filter
// selects, as a FROM source: filterWhere leads with the filter's studies.
func (s Server) filterFrom(a *core.SQLArgs, f studyViewFilter) string {
	return core.TSampleDerived.Name + " WHERE " + s.filterWhere(a, f)
}

// filterSQL compiles the filter to a SELECT of the matching sample_unique_ids.
func (s Server) filterSQL(a *core.SQLArgs, f studyViewFilter) string {
	return "SELECT sample_unique_id FROM " + s.filterFrom(a, f)
}

// sampleScopeFrom is the rows of t (sample, sample_list, clinical_sample:
// a table with study_id and sample_id columns) of the samples a study-view
// filter selects, as a FROM source: sampleScope names their studies or
// samples.
func (s Server) sampleScopeFrom(a *core.SQLArgs, f studyViewFilter, t core.KeyedTable) string {
	return t.Name + " WHERE " + s.sampleScope(a, f)
}
