package studyview

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/molecular"
)

// genericAssayDataCountsFetch handles POST /api/generic-assay-data-counts/fetch:
// per entity, the filtered samples (or patients, for patient-level profiles)
// having each categorical value, with the remainder reported as NA. Entities
// come from the data filters, or every entity of the top-level profileType.
func (s Server) genericAssayDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		GenericAssayDataFilters []genericAssayDataFilter `json:"genericAssayDataFilters"`
		ProfileType             string                   `json:"profileType"`
		StudyViewFilter         *studyViewFilter         `json:"studyViewFilter"`
	}
	core.ReadBody(r, &f)
	if len(f.GenericAssayDataFilters) == 0 && f.ProfileType == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if f.StudyViewFilter == nil {
		// The controller dereferences the filter: a NullPointerException.
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	svf := *f.StudyViewFilter
	if len(f.GenericAssayDataFilters) == 1 {
		svf = svf.withoutGenericAssay(f.GenericAssayDataFilters[0].StableID)
	}
	profileType := f.ProfileType
	var entities []string
	if len(f.GenericAssayDataFilters) > 0 {
		profileType = f.GenericAssayDataFilters[0].ProfileType
		for _, g := range f.GenericAssayDataFilters {
			entities = append(entities, g.StableID)
		}
	}
	unit, err := s.GACountUnit(profileType)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	args := core.NewArgs()
	scope := fmt.Sprintf("profile_type = %s AND sample_unique_id IN (%s)", args.Str(profileType), s.filterSQL(args, svf))
	entityClause := ""
	if len(entities) > 0 {
		entityClause = " AND entity_stable_id IN " + args.Strs(entities)
	}
	type row struct {
		StableID string `json:"stableId"`
		Value    string `json:"value"`
		Count    uint64 `json:"count"`
	}
	rows, err := core.QueryA[row](s.CH, fmt.Sprintf(
		"SELECT entity_stable_id AS stableId, value, uniqExact(%s) AS count FROM generic_assay_data_derived "+
			"WHERE NOT %s AND %s%s GROUP BY entity_stable_id, value",
		unit, isNASQL("value"), scope, entityClause), args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	var total uint64
	if len(entities) > 0 {
		if unit == "patient_unique_id" {
			type c struct {
				N uint64 `json:"n"`
			}
			t, err := core.QueryA[c](s.CH,
				"SELECT uniqExact(patient_unique_id) AS n FROM "+s.filterFrom(args, svf), args)
			if err != nil {
				core.Fail(w, r, err)
				return
			}
			if len(t) > 0 {
				total = t[0].N
			}
		} else {
			if total, err = s.scopeSampleCount(svf); err != nil {
				core.Fail(w, r, err)
				return
			}
		}
	} else {
		type c struct {
			N uint64 `json:"n"`
		}
		t, err := core.QueryA[c](s.CH, fmt.Sprintf(
			"SELECT uniqExact(%s) AS n FROM generic_assay_data_derived WHERE %s", unit, scope), args)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		if len(t) > 0 {
			total = t[0].N
		}
		type e struct {
			StableID string `json:"stableId"`
		}
		es, err := core.QueryA[e](s.CH,
			"SELECT DISTINCT entity_stable_id AS stableId FROM generic_assay_data_derived WHERE "+scope, args)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		for _, x := range es {
			entities = append(entities, x.StableID)
		}
	}
	values := map[string][]molecular.GenericAssayDataCount{}
	sums := map[string]uint64{}
	for _, rw := range rows {
		values[rw.StableID] = append(values[rw.StableID], molecular.GenericAssayDataCount{Value: rw.Value, Count: rw.Count})
		sums[rw.StableID] += rw.Count
	}
	sort.Slice(entities, func(i, j int) bool { return molecular.EntityLess(entities[i], entities[j]) })
	byFilters := len(f.GenericAssayDataFilters) > 0
	out := []molecular.GenericAssayDataCountItem{}
	for i, id := range entities {
		counts := values[id]
		sort.Slice(counts, func(i, j int) bool { return counts[i].Value < counts[j].Value })
		switch {
		case byFilters && i == 0:
			// getGenericAssayDataCounts appends one NA row, for the first
			// entity only, even when its count is 0.
			counts = append(counts, molecular.GenericAssayDataCount{Value: "NA", Count: total - min(total, sums[id])})
		case !byFilters && total > sums[id]:
			// getGenericAssayDataCountsByProfileType: an NA row per entity
			// with a positive count, sorted among the values.
			counts = append(counts, molecular.GenericAssayDataCount{Value: "NA", Count: total - sums[id]})
			sort.Slice(counts, func(i, j int) bool { return counts[i].Value < counts[j].Value })
		}
		if len(counts) == 0 {
			continue
		}
		out = append(out, molecular.GenericAssayDataCountItem{StableID: id, Counts: counts})
	}
	core.WriteJSON(w, out)
}
