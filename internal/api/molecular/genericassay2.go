package molecular

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// routeGenericAssay2 registers the generic-assay study-view aggregation
// endpoints (categorical value counts + numeric bin counts) and the
// single-entity metadata / data GETs.
func (s Server) routeGenericAssay2(mux *http.ServeMux) {
	// Single-entity data + metadata GETs.
	mux.HandleFunc("GET /api/generic-assay-data/{molecularProfileId}/generic-assay/{genericAssayStableId}", s.Bulk().genericAssayDataInProfileGet)
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

// EntityLess orders entity ids by their leading number, then lexically.
func EntityLess(a, b string) bool {
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
func (s Server) genericAssayDataInProfileGet(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("molecularProfileId")
	stableID := r.PathValue("genericAssayStableId")
	s.writeGenericAssayData(w, r, []string{mpid}, nil, []string{stableID})
}

// ---------- Single-entity metadata GETs ----------

// genericAssayMetasWhere returns distinct entity metadata rows matching the
// given WHERE clause (with placeholders bound in args), in
// generic_assay_meta_derived's order.
func (s Server) genericAssayMetasWhere(where string, args *core.SQLArgs) ([]GenericAssayMeta, error) {
	props, err := s.GenericAssayMetaProperties(where, args)
	if err != nil {
		return nil, err
	}
	return genericAssayMetaList(props), nil
}

// genericAssayMetaByStableIdGet handles
// GET /api/generic-assay-meta/generic-assay/{genericAssayStableId} — metadata for
// one entity across all profiles it appears in. Returns an array.
func (s Server) genericAssayMetaByStableIdGet(w http.ResponseWriter, r *http.Request) {
	stableID := r.PathValue("genericAssayStableId")
	args := core.NewArgs()
	out, err := s.genericAssayMetasWhere("entity_stable_id = "+args.Str(stableID), args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, out)
}

// genericAssayMetaByIDGet handles GET /api/generic-assay-meta/{molecularProfileId}:
// the entities of a generic-assay profile (none for any other id).
func (s Server) genericAssayMetaByIDGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("molecularProfileId")
	m, ok := s.ProfileMeta(id)
	if !ok || !strings.EqualFold(m.AlterationType, "GENERIC_ASSAY") {
		core.WriteJSON(w, []GenericAssayMeta{})
		return
	}
	args := core.NewArgs()
	out, err := s.genericAssayMetasWhere(fmt.Sprintf(
		"study_id = %s AND profile_stable_id = %s", args.Str(m.StudyID), args.Str(m.StableID)), args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, out)
}

// GACountUnit is the case column generic assay chart counts count for a
// profile type: patients when, as GenericAssayMapper's profile_level has it,
// any profile of this type in any study is declared patient_level, else
// samples.
func (s Server) GACountUnit(profileType string) string {
	type lvl struct {
		P uint64 `json:"p"`
	}
	lv, _ := core.CachedQueryP[lvl](s.State,
		"SELECT count() AS p FROM cbioportal_raw.genetic_profile WHERE profile_stable_id = {profile:String} AND lower(toString(patient_level)) = 'true'",
		ch.Params{"profile": profileType})
	if len(lv) > 0 && lv[0].P > 0 {
		return "patient_unique_id"
	}
	return "sample_unique_id"
}
