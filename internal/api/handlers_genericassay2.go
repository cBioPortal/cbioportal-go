package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeGenericAssay2 registers the generic-assay study-view aggregation
// endpoints (categorical value counts + numeric bin counts) and the
// single-entity metadata / data GETs.
func (s *State) routeGenericAssay2(mux *http.ServeMux) {
	// Study-view aggregations.
	mux.HandleFunc("POST /api/generic-assay-data-counts/fetch", s.genericAssayDataCountsFetch)
	mux.HandleFunc("POST /api/generic-assay-data-bin-counts/fetch", s.genericAssayDataBinCountsFetch)

	// Single-entity data + metadata GETs.
	mux.HandleFunc("GET /api/generic-assay-data/{molecularProfileId}/generic-assay/{genericAssayStableId}", s.genericAssayDataInProfileGet)
	mux.HandleFunc("GET /api/generic-assay-meta/generic-assay/{genericAssayStableId}", s.genericAssayMetaByStableIdGet)
	mux.HandleFunc("GET /api/generic-assay-meta/{molecularProfileId}", s.genericAssayMetaByIDGet)
}

// ---------- Generic assay data counts (study view, categorical) ----------

// GenericAssayDataCount is one categorical value bucket for an entity.
type GenericAssayDataCount struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}

// GenericAssayDataCountItem groups the per-value counts for one generic-assay
// entity (stableId).
type GenericAssayDataCountItem struct {
	StableID string                  `json:"stableId"`
	Counts   []GenericAssayDataCount `json:"counts"`
}

// genericAssayDataCountsFetch handles POST /api/generic-assay-data-counts/fetch:
// per entity, the filtered samples (or patients, for patient-level profiles)
// having each categorical value, with the remainder reported as NA. Entities
// come from the data filters, or every entity of the top-level profileType.
func (s *State) genericAssayDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		GenericAssayDataFilters []genericAssayDataFilter `json:"genericAssayDataFilters"`
		ProfileType             string                   `json:"profileType"`
		StudyViewFilter         studyViewFilter          `json:"studyViewFilter"`
	}
	readBody(r, &f)
	if len(f.GenericAssayDataFilters) == 0 && f.ProfileType == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	svf := f.StudyViewFilter
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
	type lvl struct {
		P uint64 `json:"p"`
	}
	// GenericAssayMapper's profile_level: any profile of this type, in any
	// study, declared patient_level.
	lv, _ := cachedQuery[lvl](s, fmt.Sprintf(
		"SELECT count() AS p FROM cbioportal_raw.genetic_profile WHERE profile_stable_id = '%s' AND lower(toString(patient_level)) = 'true'",
		sqlLit(profileType)))
	unit := "sample_unique_id"
	if len(lv) > 0 && lv[0].P > 0 {
		unit = "patient_unique_id"
	}
	scope := fmt.Sprintf("profile_type = '%s' AND sample_unique_id IN (%s)", sqlLit(profileType), s.filterSQL(svf))
	entityClause := ""
	if len(entities) > 0 {
		entityClause = fmt.Sprintf(" AND entity_stable_id IN (%s)", inList(entities))
	}
	type row struct {
		StableID string `json:"stableId"`
		Value    string `json:"value"`
		Count    uint64 `json:"count"`
	}
	rows, err := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT entity_stable_id AS stableId, value, uniqExact(%s) AS count FROM generic_assay_data_derived "+
			"WHERE NOT %s AND %s%s GROUP BY entity_stable_id, value",
		unit, isNASQL("value"), scope, entityClause))
	if err != nil {
		fail(w, err)
		return
	}
	var total uint64
	if len(entities) > 0 {
		if unit == "patient_unique_id" {
			type c struct {
				N uint64 `json:"n"`
			}
			t, _ := ch.Query[c](s.CH, fmt.Sprintf(
				"SELECT uniqExact(patient_unique_id) AS n FROM sample_derived WHERE %s", s.filterWhere(svf)))
			if len(t) > 0 {
				total = t[0].N
			}
		} else {
			total = s.scopeSampleCount(svf)
		}
	} else {
		type c struct {
			N uint64 `json:"n"`
		}
		t, _ := ch.Query[c](s.CH, fmt.Sprintf(
			"SELECT uniqExact(%s) AS n FROM generic_assay_data_derived WHERE %s", unit, scope))
		if len(t) > 0 {
			total = t[0].N
		}
		type e struct {
			StableID string `json:"stableId"`
		}
		es, _ := ch.Query[e](s.CH, fmt.Sprintf(
			"SELECT DISTINCT entity_stable_id AS stableId FROM generic_assay_data_derived WHERE %s", scope))
		for _, x := range es {
			entities = append(entities, x.StableID)
		}
	}
	values := map[string][]GenericAssayDataCount{}
	sums := map[string]uint64{}
	for _, rw := range rows {
		values[rw.StableID] = append(values[rw.StableID], GenericAssayDataCount{Value: rw.Value, Count: rw.Count})
		sums[rw.StableID] += rw.Count
	}
	sort.Slice(entities, func(i, j int) bool { return entityLess(entities[i], entities[j]) })
	byFilters := len(f.GenericAssayDataFilters) > 0
	out := []GenericAssayDataCountItem{}
	for i, id := range entities {
		counts := values[id]
		sort.Slice(counts, func(i, j int) bool { return counts[i].Value < counts[j].Value })
		switch {
		case byFilters && i == 0:
			// getGenericAssayDataCounts appends one NA row, for the first
			// entity only, even when its count is 0.
			counts = append(counts, GenericAssayDataCount{Value: "NA", Count: total - min(total, sums[id])})
		case !byFilters && total > sums[id]:
			// getGenericAssayDataCountsByProfileType: an NA row per entity
			// with a positive count, sorted among the values.
			counts = append(counts, GenericAssayDataCount{Value: "NA", Count: total - sums[id]})
			sort.Slice(counts, func(i, j int) bool { return counts[i].Value < counts[j].Value })
		}
		if len(counts) == 0 {
			continue
		}
		out = append(out, GenericAssayDataCountItem{StableID: id, Counts: counts})
	}
	writeJSON(w, out)
}

// entityLess orders entity ids by their leading number, then lexically.
func entityLess(a, b string) bool {
	na, nb := leadingInt(a), leadingInt(b)
	if na != nb {
		return na < nb
	}
	return a < b
}

func leadingInt(s string) uint64 {
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}

// ---------- Generic assay data bin counts (study view, numeric) ----------

// ---------- Single-entity data GET ----------

// genericAssayDataInProfileGet handles
// GET /api/generic-assay-data/{molecularProfileId}/generic-assay/{genericAssayStableId}
// — all samples' values for one entity in one molecular profile.
func (s *State) genericAssayDataInProfileGet(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("molecularProfileId")
	stableID := r.PathValue("genericAssayStableId")
	out, err := s.genericAssayData([]string{mpid}, nil, []string{stableID})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// ---------- Single-entity metadata GETs ----------

// genericAssayMetasWhere returns distinct entity metadata rows matching the
// given WHERE clause, in generic_assay_meta_derived's order.
func (s *State) genericAssayMetasWhere(where string) ([]GenericAssayMeta, error) {
	props, err := s.genericAssayMetaProperties(where)
	if err != nil {
		return nil, err
	}
	return genericAssayMetaList(props), nil
}

// genericAssayMetaByStableIdGet handles
// GET /api/generic-assay-meta/generic-assay/{genericAssayStableId} — metadata for
// one entity across all profiles it appears in. Returns an array.
func (s *State) genericAssayMetaByStableIdGet(w http.ResponseWriter, r *http.Request) {
	stableID := r.PathValue("genericAssayStableId")
	out, err := s.genericAssayMetasWhere(fmt.Sprintf("entity_stable_id = '%s'", sqlLit(stableID)))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// genericAssayMetaByIDGet handles GET /api/generic-assay-meta/{molecularProfileId}:
// the entities of a generic-assay profile (none for any other id).
func (s *State) genericAssayMetaByIDGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("molecularProfileId")
	m, ok := s.profileMeta(id)
	if !ok || !strings.EqualFold(m.AlterationType, "GENERIC_ASSAY") {
		writeJSON(w, []GenericAssayMeta{})
		return
	}
	out, err := s.genericAssayMetasWhere(fmt.Sprintf(
		"study_id = '%s' AND profile_stable_id = '%s'", sqlLit(m.StudyID), sqlLit(m.StableID)))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}
