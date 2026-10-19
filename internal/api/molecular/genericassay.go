package molecular

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// ---------- Generic assay data ----------

type GenericAssayData struct {
	UniqueSampleKey      string `json:"uniqueSampleKey"`
	UniquePatientKey     string `json:"uniquePatientKey"`
	MolecularProfileID   string `json:"molecularProfileId"`
	SampleID             string `json:"sampleId"`
	PatientID            string `json:"patientId"`
	StudyID              string `json:"studyId"`
	Value                string `json:"value"`
	GenericAssayStableID string `json:"genericAssayStableId"`
	PatientLevel         bool   `json:"patientLevel"`
	StableID             string `json:"stableId"`
}

type genericAssayFilter struct {
	GenericAssayStableIDs []string `json:"genericAssayStableIds"`
	SampleIDs             []string `json:"sampleIds"`
	SampleListID          string   `json:"sampleListId"`
}

type genericAssayDataRow struct {
	SampleID           string `json:"sampleId"`
	StableID           string `json:"stableId"`
	PatientID          string `json:"patientId"`
	Value              string `json:"value"`
	MolecularProfileID string `json:"molecularProfileId"`
	StudyID            string `json:"studyId"`
}

// genericAssayDataEach ports GenericAssayServiceImpl.fetchGenericAssayData,
// calling fn with each record in order: the samples (all of the profiles'
// samples when samplesByStudy is nil, in internal id order as
// getSamplesByInternalIds returns them, else the listed samples of each
// profile's study in sample-id order, as fetchSamples with the ID projection
// returns them), and for each, every requested profile of its study that
// holds the sample, by stable id, one entry per entity in matrix order.
// Entities are restricted to entityIDs unless nil. A patient-level
// profile's matrix columns are patients; every sample of the patient
// carries the patient's value, as the importer links all of a patient's
// samples to the profile. Values read as the Java mapper truncates them;
// cells without a value (empty or NA) are left out, as the controllers
// filter them. One query orders the profiles' cells so. A page the request
// r asks for is read in SQL.
func (s Server) genericAssayDataEach(r *http.Request, profileIDs []string, samplesByStudy map[string][]string, entityIDs []string,
	fn func(GenericAssayData) error) error {
	if entityIDs != nil && len(entityIDs) == 0 {
		return nil
	}
	ids := slices.Clone(profileIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	// Samples sort by internal id (where the database carries them) unless
	// requested by id, then by stable id and study.
	sampleRank := "toUInt64(0)"
	if samplesByStudy == nil && s.ImportOrdered() {
		sampleRank = "toUInt64(s.internal_id)"
	}
	args := core.NewArgs()
	patientLevel := map[string]bool{}
	var parts []string
	for _, mpid := range ids {
		meta, ok := s.ProfileMeta(mpid)
		if !ok || !strings.EqualFold(meta.AlterationType, "GENERIC_ASSAY") {
			continue
		}
		sampleCond := ""
		if samplesByStudy != nil {
			sampleIDs := samplesByStudy[meta.StudyID]
			if len(sampleIDs) == 0 {
				continue
			}
			sampleCond = " AND s.sample_id IN " + args.Strs(sampleIDs)
		}
		sc := core.ByStudy(meta.StudyID)
		rest := " AND profile_stable_id = " + args.Str(meta.StableID)
		if entityIDs != nil {
			rest += " AND entity_stable_id IN " + args.Strs(entityIDs)
		}
		matrix := core.RawMatrix("generic_assay_matrix")
		join := "s.sample_id = m.sample_id"
		if s.GAProfilePatientLevel(meta) {
			join = "s.patient_id = m.sample_id"
			patientLevel[mpid] = true
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT s.sample_id AS sampleId, m.entity_stable_id AS stableId, s.patient_id AS patientId, %s AS value, "+
				"%s AS molecularProfileId, %s AS studyId, %s AS sampleRank, en.o AS entityRank "+
				"FROM (SELECT * FROM %s WHERE %s) m JOIN %s s ON s.study_id = m.study_id AND %s%s "+
				"JOIN (SELECT entity_stable_id AS e, min(%s) AS o FROM %s "+
				"GROUP BY entity_stable_id) en ON en.e = m.entity_stable_id",
			core.JavaTruncatedValueSQL("m.value"), args.Str(mpid), args.Str(meta.StudyID), sampleRank,
			genericAssaySource(s.State), sc.Cond(args, matrix, "")+rest, sc.Samples(args), join, sampleCond,
			s.RawFileOrder("generic_assay_matrix", ""), sc.From(args, matrix)+rest))
	}
	if len(parts) == 0 {
		return nil
	}
	// A matrix lists each entity of a sample once; LIMIT BY keeps one should
	// it repeat.
	order := "sampleRank, sampleId, studyId, molecularProfileId, entityRank, stableId"
	query := "SELECT sampleId, stableId, patientId, value, molecularProfileId, studyId FROM (" +
		strings.Join(parts, " UNION ALL ") + ") ORDER BY " + order + " LIMIT 1 BY molecularProfileId, sampleId, stableId"
	if sp, expr := core.ClaimListPage(r, genericAssayDataSortable); sp != nil {
		tie := order
		if sp.SortBy == "value" {
			// Values that are no number (categories) follow, as text.
			dir := " ASC, "
			if sp.Desc {
				dir = " DESC, "
			}
			tie = "value" + dir + order
		}
		// The page holds the cells with a value, which are the ones written.
		query = "SELECT * FROM (SELECT * FROM (" + strings.Join(parts, " UNION ALL ") + ") ORDER BY " + order +
			" LIMIT 1 BY molecularProfileId, sampleId, stableId) WHERE value NOT IN ('', 'NA')" + sp.OrderLimit(expr, tie)
	}
	return ch.QueryEachP(s.CH, query, args.Params(query), func(r *genericAssayDataRow) error {
		if r.Value == "" || r.Value == "NA" {
			return nil
		}
		return fn(GenericAssayData{
			GenericAssayStableID: r.StableID, MolecularProfileID: r.MolecularProfileID, PatientID: r.PatientID,
			PatientLevel: patientLevel[r.MolecularProfileID], SampleID: r.SampleID, StableID: r.StableID,
			StudyID: r.StudyID, Value: r.Value,
			UniqueSampleKey:  core.UniqueKey(r.StudyID, r.SampleID),
			UniquePatientKey: core.UniqueKey(r.StudyID, r.PatientID),
		})
	})
}

// genericAssayDataSortable are the sortBy fields of generic assay data
// records over genericAssayDataEach's columns. value sorts as a number;
// values that are not numbers come after them, as text.
var genericAssayDataSortable = map[string]string{
	"stableId": "stableId", "genericAssayStableId": "stableId", "sampleId": "sampleId", "patientId": "patientId",
	"studyId": "studyId", "molecularProfileId": "molecularProfileId", "value": "toFloat64OrNull(value)",
}

// writeGenericAssayData streams genericAssayDataEach's records.
func (s Server) writeGenericAssayData(w http.ResponseWriter, r *http.Request, profileIDs []string, samplesByStudy map[string][]string, entityIDs []string) {
	out := core.NewJSONArray(w, r)
	out.Finish(s.genericAssayDataEach(r, profileIDs, samplesByStudy, entityIDs, func(d GenericAssayData) error {
		return out.Add(d)
	}))
}

// genericAssayDataInProfileFetch handles POST /api/generic_assay_data/{molecularProfileId}/fetch:
// the samples of the sample list, or the given sample ids.
func (s Server) genericAssayDataInProfileFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("molecularProfileId")
	var f genericAssayFilter
	core.ReadBody(r, &f)
	if core.NoSamples(f.SampleIDs, f.SampleListID) {
		core.WriteJSON(w, []struct{}{})
		return
	}
	sampleIDs := f.SampleIDs
	if f.SampleListID != "" {
		type idRow struct {
			SampleID string `json:"sampleId"`
		}
		p := ch.Params{}
		rows, err := ch.QueryP[idRow](s.CH, "SELECT sample_id AS sampleId FROM "+s.BySampleList(f.SampleListID).From(core.ParamsBinder(p), core.TSampleList), p)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		sampleIDs = make([]string, len(rows))
		for i, row := range rows {
			sampleIDs[i] = row.SampleID
		}
		if len(sampleIDs) == 0 {
			core.WriteJSON(w, []GenericAssayData{})
			return
		}
	}
	var byStudy map[string][]string
	if sampleIDs != nil {
		meta, _ := s.ProfileMeta(mpid)
		byStudy = map[string][]string{meta.StudyID: sampleIDs}
	}
	s.writeGenericAssayData(w, r, []string{mpid}, byStudy, f.GenericAssayStableIDs)
}

type genericAssayMultipleStudyFilter struct {
	GenericAssayStableIDs      []string `json:"genericAssayStableIds"`
	MolecularProfileIDs        []string `json:"molecularProfileIds"`
	SampleMolecularIdentifiers []struct {
		MolecularProfileID string `json:"molecularProfileId"`
		SampleID           string `json:"sampleId"`
	} `json:"sampleMolecularIdentifiers"`
}

// genericAssayDataMultipleFetch handles POST /api/generic_assay_data/fetch:
// every sample of the profiles, or the samples named by the
// (profile, sample) identifiers — each then read from every requested
// profile of its study, as Java pairs samples with profiles by study.
// Exactly one of the two forms must be given (else 400 with no body).
func (s Server) genericAssayDataMultipleFetch(w http.ResponseWriter, r *http.Request) {
	var f genericAssayMultipleStudyFilter
	core.ReadBody(r, &f)
	if (len(f.MolecularProfileIDs) > 0) == (len(f.SampleMolecularIdentifiers) > 0) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(f.MolecularProfileIDs) > 0 {
		s.writeGenericAssayData(w, r, f.MolecularProfileIDs, nil, f.GenericAssayStableIDs)
		return
	}
	var profiles []string
	byStudy := map[string][]string{}
	for _, smi := range f.SampleMolecularIdentifiers {
		profiles = append(profiles, smi.MolecularProfileID)
		if meta, ok := s.ProfileMeta(smi.MolecularProfileID); ok {
			byStudy[meta.StudyID] = append(byStudy[meta.StudyID], smi.SampleID)
		}
	}
	s.writeGenericAssayData(w, r, profiles, byStudy, f.GenericAssayStableIDs)
}

// ---------- Generic assay meta ----------

type GenericAssayMeta struct {
	StableID                    string             `json:"stableId"`
	EntityType                  string             `json:"entityType"`
	GenericEntityMetaProperties core.JavaStringMap `json:"genericEntityMetaProperties"`
}

type genericAssayMetaFilter struct {
	GenericAssayStableIDs []string `json:"genericAssayStableIds"`
	MolecularProfileIDs   []string `json:"molecularProfileIds"`
}

type genericAssayMetaRow struct {
	StableID    string `json:"stableId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// genericAssayMetaFetch handles POST /api/generic-assay-meta/fetch — distinct
// entities (with NAME/DESCRIPTION/URL properties) across the requested profiles
// and/or entity ids. searchTerm, trimmed, keeps the entities whose stable id,
// NAME or DESCRIPTION contains it (GetGenericAssayMetaUseCase): ignoring case,
// with LIKE wildcards, and on the stable id alone for the ID projection. The
// legacy alias takes no searchTerm.
func (s Server) genericAssayMetaFetch(w http.ResponseWriter, r *http.Request) {
	var f genericAssayMetaFilter
	core.ReadBody(r, &f)
	search := ""
	if !core.IsLegacyAlias(r) {
		search = strings.TrimSpace(r.URL.Query().Get("searchTerm"))
	}
	idOnly := strings.EqualFold(r.URL.Query().Get("projection"), "ID")

	args := core.NewArgs()
	var conds []string
	if len(f.MolecularProfileIDs) > 0 {
		var pairs [][2]string
		for _, mpid := range f.MolecularProfileIDs {
			if m, ok := s.ProfileMeta(mpid); ok && strings.EqualFold(m.AlterationType, "GENERIC_ASSAY") {
				pairs = append(pairs, [2]string{m.StudyID, m.StableID})
			}
		}
		if len(pairs) == 0 {
			core.WriteJSON(w, []GenericAssayMeta{})
			return
		}
		conds = append(conds, "(study_id, profile_stable_id) IN "+args.Pairs(pairs))
	}
	if len(f.GenericAssayStableIDs) > 0 {
		conds = append(conds, "entity_stable_id IN "+args.Strs(f.GenericAssayStableIDs))
	}
	if len(conds) == 0 {
		core.WriteJSON(w, []GenericAssayMeta{})
		return
	}
	where := "1 = 1"
	if len(conds) > 0 {
		where = strings.Join(conds, " AND ")
	}
	var matches func(GenericAssayMeta) bool
	if search != "" {
		matches = genericAssayMetaSearch(search, idOnly)
	}
	out := core.NewJSONArray(w, r)
	out.Finish(s.genericAssayMetaEach(where, args, func(m GenericAssayMeta) error {
		if matches != nil && !matches(m) {
			return nil
		}
		return out.Add(m)
	}))
}

// genericAssayMetaSearch returns the test of whether an entity matches a
// non-empty searchTerm as GetGenericAssayMetaUseCase matches it: for the ID
// projection its stable id contains the term, ignoring case; otherwise its
// stable id, NAME or DESCRIPTION is ILIKE '%term%'
// (GenericAssayMetaSearchPredicate).
func genericAssayMetaSearch(term string, idOnly bool) func(GenericAssayMeta) bool {
	if idOnly {
		lower := strings.ToLower(term)
		return func(m GenericAssayMeta) bool { return strings.Contains(strings.ToLower(m.StableID), lower) }
	}
	like := core.ILikeMatcher("%" + term + "%")
	return func(m GenericAssayMeta) bool {
		props := m.GenericEntityMetaProperties.Vals
		return like(m.StableID) || like(props["NAME"]) || like(props["DESCRIPTION"])
	}
}

// genericAssayMetaEach calls fn with the metadata record of each entity
// where selects, in genericAssayMetaList's order, building one record at a
// time: the requested profiles can have hundreds of thousands of entities.
func (s Server) genericAssayMetaEach(where string, args *core.SQLArgs, fn func(GenericAssayMeta) error) error {
	type entity struct {
		id         string
		keys, vals []string
	}
	var all []entity
	if len(s.RawColumns("generic_assay_entity_meta")) > 0 {
		type propRow struct {
			StableID string   `json:"stableId"`
			Keys     []string `json:"keys"`
			Values   []string `json:"vals"`
		}
		sql := fmt.Sprintf(
			"SELECT entity_stable_id AS stableId, mapKeys(properties) AS keys, mapValues(properties) AS vals "+
				"FROM cbioportal_raw.generic_assay_entity_meta "+
				"WHERE entity_stable_id IN (SELECT entity_stable_id FROM cbioportal_raw.generic_assay_profile_entity WHERE %s)", where)
		err := ch.QueryEachP(s.CH, sql, args.Params(sql), func(r *propRow) error {
			all = append(all, entity{r.StableID, r.Keys, r.Values})
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		sql := fmt.Sprintf(
			"SELECT entity_stable_id AS stableId, any(name) AS name, any(description) AS description, any(url) AS url "+
				"FROM %s WHERE %s GROUP BY entity_stable_id", genericAssaySource(s.State), where)
		err := ch.QueryEachP(s.CH, sql, args.Params(sql), func(r *genericAssayMetaRow) error {
			all = append(all, entity{r.StableID, []string{"NAME", "DESCRIPTION", "URL"}, []string{r.Name, r.Description, r.URL}})
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Of an entity's rows the last one counts, as when Java collects them
	// into a map.
	sort.SliceStable(all, func(i, j int) bool { return EntityLess(all[i].id, all[j].id) })
	for i, e := range all {
		if i+1 < len(all) && all[i+1].id == e.id {
			continue
		}
		var m core.JavaStringMap
		for k, key := range e.keys {
			m.Set(key, e.vals[k])
		}
		if err := fn(GenericAssayMeta{EntityType: "GENERIC_ASSAY", StableID: e.id, GenericEntityMetaProperties: m}); err != nil {
			return err
		}
		all[i] = entity{}
	}
	return nil
}

// genericAssayMetaList orders entity metadata as GenericAssayMapper does:
// by the number an id starts with (0 without one), then by id.
func genericAssayMetaList(props map[string]core.JavaStringMap) []GenericAssayMeta {
	ids := core.SortedKeys(props)
	sort.SliceStable(ids, func(i, j int) bool { return EntityLess(ids[i], ids[j]) })
	out := make([]GenericAssayMeta, 0, len(ids))
	for _, id := range ids {
		out = append(out, GenericAssayMeta{EntityType: "GENERIC_ASSAY", StableID: id, GenericEntityMetaProperties: props[id]})
	}
	return out
}

// GenericAssayMetaProperties returns the meta properties of the generic
// assay entities matching where (over study_id, profile_stable_id and
// entity_stable_id, with placeholders bound in args), keyed by entity stable
// id: every stored property where the raw database has the entity tables of a
// production clone, else the NAME / DESCRIPTION / URL columns of the staged
// matrix. Properties keep their stored order (the importer's, by property
// id), in which Java puts them into its HashMap.
func (s Server) GenericAssayMetaProperties(where string, args *core.SQLArgs) (map[string]core.JavaStringMap, error) {
	if len(s.RawColumns("generic_assay_entity_meta")) > 0 {
		type propRow struct {
			StableID string   `json:"stableId"`
			Keys     []string `json:"keys"`
			Values   []string `json:"vals"`
		}
		rows, err := core.QueryA[propRow](s.CH, fmt.Sprintf(
			"SELECT entity_stable_id AS stableId, mapKeys(properties) AS keys, mapValues(properties) AS vals "+
				"FROM cbioportal_raw.generic_assay_entity_meta "+
				"WHERE entity_stable_id IN (SELECT entity_stable_id FROM cbioportal_raw.generic_assay_profile_entity WHERE %s)", where), args)
		if err != nil {
			return nil, err
		}
		out := make(map[string]core.JavaStringMap, len(rows))
		for _, r := range rows {
			var m core.JavaStringMap
			for i, k := range r.Keys {
				m.Set(k, r.Values[i])
			}
			out[r.StableID] = m
		}
		return out, nil
	}
	rows, err := core.QueryA[genericAssayMetaRow](s.CH, fmt.Sprintf(
		"SELECT entity_stable_id AS stableId, any(name) AS name, any(description) AS description, any(url) AS url "+
			"FROM %s WHERE %s GROUP BY entity_stable_id", genericAssaySource(s.State), where), args)
	if err != nil {
		return nil, err
	}
	out := make(map[string]core.JavaStringMap, len(rows))
	for _, r := range rows {
		var m core.JavaStringMap
		m.Set("NAME", r.Name)
		m.Set("DESCRIPTION", r.Description)
		m.Set("URL", r.URL)
		out[r.StableID] = m
	}
	return out, nil
}

// SummaryMetaProperties keeps the properties GenericAssayServiceImpl returns
// for the SUMMARY projection (SUMMARY_META_PROPERTY_NAMES), as the enrichments
// attach them.
func SummaryMetaProperties(props map[string]core.JavaStringMap) map[string]core.JavaStringMap {
	out := make(map[string]core.JavaStringMap, len(props))
	for id, m := range props {
		var kept core.JavaStringMap
		for _, k := range m.Keys {
			switch k {
			case "NAME", "DESCRIPTION", "URL":
				kept.Set(k, m.Vals[k])
			}
		}
		out[id] = kept
	}
	return out
}

// routeGenericAssay registers the generic-assay and geneset (GSVA) endpoints.
func (s Server) routeGenericAssay(mux *http.ServeMux) {
	// Generic assay data.
	mux.HandleFunc("POST /api/generic_assay_data/{molecularProfileId}/fetch", s.Bulk().genericAssayDataInProfileFetch)
	mux.HandleFunc("POST /api/generic_assay_data/fetch", s.Bulk().genericAssayDataMultipleFetch)

	// Generic assay meta.
	mux.HandleFunc("POST /api/generic-assay-meta/fetch", s.genericAssayMetaFetch)

	// Genesets.
	mux.HandleFunc("POST /api/genesets/fetch", s.genesetsFetch)
	mux.HandleFunc("GET /api/genesets/{genesetId}", s.genesetGet)

	// Geneset molecular data (GSVA).
	mux.HandleFunc("POST /api/genetic-profiles/{geneticProfileId}/geneset-genetic-data/fetch", s.genesetGeneticDataFetch)
}

// genericAssaySource is the staged generic-assay matrix with every column the
// handlers read (entity properties a file lacks read as empty).
func genericAssaySource(s *core.State) string {
	return s.RawSource("generic_assay_matrix",
		"study_id", "profile_stable_id", "entity_stable_id", "sample_id", "value", "name", "description", "url")
}

// genesetScoreSource is the staged GSVA score matrix.
func genesetScoreSource(s *core.State) string {
	return s.RawSource("geneset_score_matrix", "study_id", "profile_stable_id", "geneset_id", "sample_id", "value")
}

// geneMatrixCols are the columns read from a staged per-gene matrix.
var geneMatrixCols = []string{"study_id", "profile_stable_id", "sample_id", "hugo_symbol", "entrez_gene_id", "value"}

// GAProfilePatientLevel reports whether the profile's meta file declares
// patient_level: true (its matrix columns are then patient ids).
func (s Server) GAProfilePatientLevel(meta core.ProfileMeta) bool {
	type row struct {
		P string `json:"p"`
	}
	rows, err := core.CachedQueryP[row](s.State,
		"SELECT toString(patient_level) AS p FROM cbioportal_raw.genetic_profile "+
			"WHERE study_id = {study:String} AND profile_stable_id = {profile:String} LIMIT 1",
		ch.Params{"study": meta.StudyID, "profile": meta.StableID})
	return err == nil && len(rows) > 0 && strings.EqualFold(strings.TrimSpace(rows[0].P), "true")
}
