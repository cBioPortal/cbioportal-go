package api

import (
	"fmt"
	"net/http"
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
	mux.HandleFunc("GET /api/generic-assay-meta/{genericAssayStableId}", s.genericAssayMetaByIDGet)
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

// genericAssayDataCountsFetch handles POST /api/generic-assay-data-counts/fetch.
// For the requested profileType it counts, per entity, the distinct scoped
// samples having each categorical value, plus an "NA" bucket for scoped samples
// that have data in the profile but no (non-NA) value for that entity.
func (s *State) genericAssayDataCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		GenericAssayDataFilters []struct {
			ProfileType string `json:"profileType"`
			StableID    string `json:"stableId"`
		} `json:"genericAssayDataFilters"`
		ProfileType     string          `json:"profileType"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	readBody(r, &f)

	scope := f.StudyViewFilter.sampleScope()

	// Resolve the profile type(s) to count: the explicit top-level one, else the
	// distinct types named by the individual data filters.
	var profileTypes []string
	if f.ProfileType != "" {
		profileTypes = []string{f.ProfileType}
	} else {
		seen := map[string]bool{}
		for _, df := range f.GenericAssayDataFilters {
			if df.ProfileType != "" && !seen[df.ProfileType] {
				seen[df.ProfileType] = true
				profileTypes = append(profileTypes, df.ProfileType)
			}
		}
	}

	out := []GenericAssayDataCountItem{}
	type row struct {
		StableID string `json:"stableId"`
		Value    string `json:"value"`
		Count    uint64 `json:"count"`
	}
	for _, pt := range profileTypes {
		total := fmt.Sprintf(
			"(SELECT uniqExact(sample_id) FROM cbioportal_raw.generic_assay_matrix WHERE profile_stable_id = '%s' AND %s)",
			sqlLit(pt), scope)
		sql := fmt.Sprintf(
			"SELECT stableId, value, count FROM ("+
				"SELECT g.entity_stable_id AS stableId, g.value AS value, toUInt64(uniqExact(g.sample_id)) AS count "+
				"FROM cbioportal_raw.generic_assay_matrix g "+
				"WHERE g.profile_stable_id = '%[1]s' AND %[2]s AND upper(trimBoth(g.value)) NOT IN ('NA', '') "+
				"GROUP BY g.entity_stable_id, g.value "+
				"UNION ALL "+
				"SELECT g.entity_stable_id AS stableId, 'NA' AS value, "+
				"toUInt64(%[3]s - uniqExactIf(g.sample_id, upper(trimBoth(g.value)) NOT IN ('NA', ''))) AS count "+
				"FROM cbioportal_raw.generic_assay_matrix g "+
				"WHERE g.profile_stable_id = '%[1]s' AND %[2]s "+
				"GROUP BY g.entity_stable_id"+
				") WHERE count > 0 ORDER BY stableId, value",
			sqlLit(pt), scope, total)
		rows, err := ch.Query[row](s.CH, sql)
		if err != nil {
			fail(w, err)
			return
		}
		// Group value rows into one item per entity, preserving stableId order.
		byEntity := map[string]int{}
		for _, rw := range rows {
			idx, ok := byEntity[rw.StableID]
			if !ok {
				idx = len(out)
				byEntity[rw.StableID] = idx
				out = append(out, GenericAssayDataCountItem{StableID: rw.StableID, Counts: []GenericAssayDataCount{}})
			}
			out[idx].Counts = append(out[idx].Counts, GenericAssayDataCount{Value: rw.Value, Count: rw.Count})
		}
	}
	writeJSON(w, out)
}

// ---------- Generic assay data bin counts (study view, numeric) ----------

// GenericAssayDataBin is one histogram bucket for a numeric generic-assay entity.
type GenericAssayDataBin struct {
	StableID     string   `json:"stableId"`
	ProfileType  string   `json:"profileType"`
	SpecialValue string   `json:"specialValue,omitempty"`
	Start        *float64 `json:"start,omitempty"`
	End          *float64 `json:"end,omitempty"`
	Count        uint64   `json:"count"`
}

// genericAssayDataBinCountsFetch handles POST /api/generic-assay-data-bin-counts/fetch.
// For each (profileType, stableId) it groups the entity's values in ClickHouse
// and buckets them into equal-width numeric bins (plus an NA bucket).
func (s *State) genericAssayDataBinCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		GenericAssayDataBinFilters []struct {
			ProfileType string `json:"profileType"`
			StableID    string `json:"stableId"`
		} `json:"genericAssayDataBinFilters"`
		StudyViewFilter studyViewFilter `json:"studyViewFilter"`
	}
	readBody(r, &f)

	scope := f.StudyViewFilter.sampleScope()

	out := []GenericAssayDataBin{}
	for _, bf := range f.GenericAssayDataBinFilters {
		if bf.StableID == "" || bf.ProfileType == "" {
			continue
		}
		rows, err := ch.Query[binValRow](s.CH, fmt.Sprintf(
			"SELECT value AS value, toUInt64(count()) AS c FROM cbioportal_raw.generic_assay_matrix "+
				"WHERE profile_stable_id = '%s' AND entity_stable_id = '%s' AND %s GROUP BY value",
			sqlLit(bf.ProfileType), sqlLit(bf.StableID), scope))
		if err != nil {
			fail(w, err)
			return
		}
		for _, b := range binNumericValues(bf.StableID, rows) {
			out = append(out, GenericAssayDataBin{
				StableID:     bf.StableID,
				ProfileType:  bf.ProfileType,
				SpecialValue: b.SpecialValue,
				Start:        b.Start,
				End:          b.End,
				Count:        b.Count,
			})
		}
	}
	writeJSON(w, out)
}

// ---------- Single-entity data GET ----------

// genericAssayDataInProfileGet handles
// GET /api/generic-assay-data/{molecularProfileId}/generic-assay/{genericAssayStableId}
// — all samples' values for one entity in one molecular profile.
func (s *State) genericAssayDataInProfileGet(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("molecularProfileId")
	stableID := r.PathValue("genericAssayStableId")
	out, err := s.genericAssayRows(mpid, []string{stableID}, nil, "")
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// ---------- Single-entity metadata GETs ----------

// genericAssayMetasWhere returns distinct entity metadata rows matching the
// given WHERE clause over the generic-assay matrix.
func (s *State) genericAssayMetasWhere(where string) ([]GenericAssayMeta, error) {
	rows, err := ch.Query[genericAssayMetaRow](s.CH, fmt.Sprintf(
		"SELECT entity_stable_id AS stableId, any(name) AS name, any(description) AS description, any(url) AS url "+
			"FROM cbioportal_raw.generic_assay_matrix WHERE %s GROUP BY entity_stable_id ORDER BY entity_stable_id", where))
	if err != nil {
		return nil, err
	}
	out := make([]GenericAssayMeta, len(rows))
	for i, rw := range rows {
		out[i] = GenericAssayMeta{
			EntityType: "GENERIC_ASSAY", StableID: rw.StableID,
			GenericEntityMetaProperties: map[string]string{
				"NAME": rw.Name, "DESCRIPTION": rw.Description, "URL": rw.URL,
			},
		}
	}
	return out, nil
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

// genericAssayMetaByIDGet handles GET /api/generic-assay-meta/{genericAssayStableId}.
// The path segment is a molecular profile id when the caller wants every entity
// in a profile, or an entity stable id otherwise; both are resolved. Returns an
// array of GenericAssayMeta.
func (s *State) genericAssayMetaByIDGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("genericAssayStableId")
	if m, ok := s.profileMeta(id); ok && strings.EqualFold(m.AlterationType, "GENERIC_ASSAY") {
		out, err := s.genericAssayMetasWhere(fmt.Sprintf(
			"study_id = '%s' AND profile_stable_id = '%s'", sqlLit(m.StudyID), sqlLit(m.StableID)))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, out)
		return
	}
	out, err := s.genericAssayMetasWhere(fmt.Sprintf("entity_stable_id = '%s'", sqlLit(id)))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}
