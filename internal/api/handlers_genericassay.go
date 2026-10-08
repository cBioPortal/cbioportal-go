package api

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

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
func (s *State) genericAssayDataEach(r *http.Request, profileIDs []string, samplesByStudy map[string][]string, entityIDs []string,
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
	if samplesByStudy == nil && s.importOrdered() {
		sampleRank = "toUInt64(s.internal_id)"
	}
	args := newArgs()
	patientLevel := map[string]bool{}
	var parts []string
	for _, mpid := range ids {
		meta, ok := s.profileMeta(mpid)
		if !ok || !strings.EqualFold(meta.AlterationType, "GENERIC_ASSAY") {
			continue
		}
		sampleCond := ""
		if samplesByStudy != nil {
			sampleIDs := samplesByStudy[meta.StudyID]
			if len(sampleIDs) == 0 {
				continue
			}
			sampleCond = " AND s.sample_id IN " + args.strs(sampleIDs)
		}
		sc := byStudy(meta.StudyID)
		rest := " AND profile_stable_id = " + args.str(meta.StableID)
		if entityIDs != nil {
			rest += " AND entity_stable_id IN " + args.strs(entityIDs)
		}
		matrix := rawMatrix("generic_assay_matrix")
		join := "s.sample_id = m.sample_id"
		if s.gaProfilePatientLevel(meta) {
			join = "s.patient_id = m.sample_id"
			patientLevel[mpid] = true
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT s.sample_id AS sampleId, m.entity_stable_id AS stableId, s.patient_id AS patientId, %s AS value, "+
				"%s AS molecularProfileId, %s AS studyId, %s AS sampleRank, en.o AS entityRank "+
				"FROM (SELECT * FROM %s WHERE %s) m JOIN %s s ON s.study_id = m.study_id AND %s%s "+
				"JOIN (SELECT entity_stable_id AS e, min(%s) AS o FROM %s "+
				"GROUP BY entity_stable_id) en ON en.e = m.entity_stable_id",
			javaTruncatedValueSQL("m.value"), args.str(mpid), args.str(meta.StudyID), sampleRank,
			genericAssaySource(s), sc.cond(args, matrix, "")+rest, sc.samples(args), join, sampleCond,
			s.rawFileOrder("generic_assay_matrix", ""), sc.from(args, matrix)+rest))
	}
	if len(parts) == 0 {
		return nil
	}
	// A matrix lists each entity of a sample once; LIMIT BY keeps one should
	// it repeat.
	order := "sampleRank, sampleId, studyId, molecularProfileId, entityRank, stableId"
	query := "SELECT sampleId, stableId, patientId, value, molecularProfileId, studyId FROM (" +
		strings.Join(parts, " UNION ALL ") + ") ORDER BY " + order + " LIMIT 1 BY molecularProfileId, sampleId, stableId"
	if sp, expr := claimListPage(r, genericAssayDataSortable); sp != nil {
		tie := order
		if sp.sortBy == "value" {
			// Values that are no number (categories) follow, as text.
			dir := " ASC, "
			if sp.desc {
				dir = " DESC, "
			}
			tie = "value" + dir + order
		}
		// The page holds the cells with a value, which are the ones written.
		query = "SELECT * FROM (SELECT * FROM (" + strings.Join(parts, " UNION ALL ") + ") ORDER BY " + order +
			" LIMIT 1 BY molecularProfileId, sampleId, stableId) WHERE value NOT IN ('', 'NA')" + sp.orderLimit(expr, tie)
	}
	return ch.QueryEachP(s.CH, query, args.params(query), func(r *genericAssayDataRow) error {
		if r.Value == "" || r.Value == "NA" {
			return nil
		}
		return fn(GenericAssayData{
			GenericAssayStableID: r.StableID, MolecularProfileID: r.MolecularProfileID, PatientID: r.PatientID,
			PatientLevel: patientLevel[r.MolecularProfileID], SampleID: r.SampleID, StableID: r.StableID,
			StudyID: r.StudyID, Value: r.Value,
			UniqueSampleKey:  uniqueKey(r.StudyID, r.SampleID),
			UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
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
func (s *State) writeGenericAssayData(w http.ResponseWriter, r *http.Request, profileIDs []string, samplesByStudy map[string][]string, entityIDs []string) {
	out := newJSONArray(w, r)
	out.finish(s.genericAssayDataEach(r, profileIDs, samplesByStudy, entityIDs, func(d GenericAssayData) error {
		return out.add(d)
	}))
}

// genericAssayDataInProfileFetch handles POST /api/generic_assay_data/{molecularProfileId}/fetch:
// the samples of the sample list, or the given sample ids.
func (s *State) genericAssayDataInProfileFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("molecularProfileId")
	var f genericAssayFilter
	readBody(r, &f)
	if noSamples(f.SampleIDs, f.SampleListID) {
		writeJSON(w, []struct{}{})
		return
	}
	sampleIDs := f.SampleIDs
	if f.SampleListID != "" {
		type idRow struct {
			SampleID string `json:"sampleId"`
		}
		p := ch.Params{}
		rows, err := ch.QueryP[idRow](s.CH, "SELECT sample_id AS sampleId FROM "+s.bySampleList(f.SampleListID).from(paramsBinder(p), tSampleList), p)
		if err != nil {
			fail(w, r, err)
			return
		}
		sampleIDs = make([]string, len(rows))
		for i, row := range rows {
			sampleIDs[i] = row.SampleID
		}
		if len(sampleIDs) == 0 {
			writeJSON(w, []GenericAssayData{})
			return
		}
	}
	var byStudy map[string][]string
	if sampleIDs != nil {
		meta, _ := s.profileMeta(mpid)
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
func (s *State) genericAssayDataMultipleFetch(w http.ResponseWriter, r *http.Request) {
	var f genericAssayMultipleStudyFilter
	readBody(r, &f)
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
		if meta, ok := s.profileMeta(smi.MolecularProfileID); ok {
			byStudy[meta.StudyID] = append(byStudy[meta.StudyID], smi.SampleID)
		}
	}
	s.writeGenericAssayData(w, r, profiles, byStudy, f.GenericAssayStableIDs)
}

// ---------- Generic assay meta ----------

type GenericAssayMeta struct {
	StableID                    string        `json:"stableId"`
	EntityType                  string        `json:"entityType"`
	GenericEntityMetaProperties javaStringMap `json:"genericEntityMetaProperties"`
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
// and/or entity ids.
func (s *State) genericAssayMetaFetch(w http.ResponseWriter, r *http.Request) {
	var f genericAssayMetaFilter
	readBody(r, &f)

	args := newArgs()
	var conds []string
	if len(f.MolecularProfileIDs) > 0 {
		var pairs [][2]string
		for _, mpid := range f.MolecularProfileIDs {
			if m, ok := s.profileMeta(mpid); ok && strings.EqualFold(m.AlterationType, "GENERIC_ASSAY") {
				pairs = append(pairs, [2]string{m.StudyID, m.StableID})
			}
		}
		if len(pairs) == 0 {
			writeJSON(w, []GenericAssayMeta{})
			return
		}
		conds = append(conds, "(study_id, profile_stable_id) IN "+args.pairs(pairs))
	}
	if len(f.GenericAssayStableIDs) > 0 {
		conds = append(conds, "entity_stable_id IN "+args.strs(f.GenericAssayStableIDs))
	}
	if len(conds) == 0 {
		writeJSON(w, []GenericAssayMeta{})
		return
	}
	where := "1 = 1"
	if len(conds) > 0 {
		where = strings.Join(conds, " AND ")
	}
	out := newJSONArray(w, r)
	out.finish(s.genericAssayMetaEach(where, args, func(m GenericAssayMeta) error {
		return out.add(m)
	}))
}

// genericAssayMetaEach calls fn with each record of
// genericAssayMetaList(genericAssayMetaProperties(where, args)), in that
// order, building one record at a time: the requested profiles can have
// hundreds of thousands of entities.
func (s *State) genericAssayMetaEach(where string, args *sqlArgs, fn func(GenericAssayMeta) error) error {
	type entity struct {
		id         string
		keys, vals []string
	}
	var all []entity
	if len(s.rawColumns("generic_assay_entity_meta")) > 0 {
		type propRow struct {
			StableID string   `json:"stableId"`
			Keys     []string `json:"keys"`
			Values   []string `json:"vals"`
		}
		sql := fmt.Sprintf(
			"SELECT entity_stable_id AS stableId, mapKeys(properties) AS keys, mapValues(properties) AS vals "+
				"FROM cbioportal_raw.generic_assay_entity_meta "+
				"WHERE entity_stable_id IN (SELECT entity_stable_id FROM cbioportal_raw.generic_assay_profile_entity WHERE %s)", where)
		err := ch.QueryEachP(s.CH, sql, args.params(sql), func(r *propRow) error {
			all = append(all, entity{r.StableID, r.Keys, r.Values})
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		sql := fmt.Sprintf(
			"SELECT entity_stable_id AS stableId, any(name) AS name, any(description) AS description, any(url) AS url "+
				"FROM %s WHERE %s GROUP BY entity_stable_id", genericAssaySource(s), where)
		err := ch.QueryEachP(s.CH, sql, args.params(sql), func(r *genericAssayMetaRow) error {
			all = append(all, entity{r.StableID, []string{"NAME", "DESCRIPTION", "URL"}, []string{r.Name, r.Description, r.URL}})
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Of an entity's rows the last one counts, as in
	// genericAssayMetaProperties' map.
	sort.SliceStable(all, func(i, j int) bool { return entityLess(all[i].id, all[j].id) })
	for i, e := range all {
		if i+1 < len(all) && all[i+1].id == e.id {
			continue
		}
		var m javaStringMap
		for k, key := range e.keys {
			m.set(key, e.vals[k])
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
func genericAssayMetaList(props map[string]javaStringMap) []GenericAssayMeta {
	ids := sortedKeys(props)
	sort.SliceStable(ids, func(i, j int) bool { return entityLess(ids[i], ids[j]) })
	out := make([]GenericAssayMeta, 0, len(ids))
	for _, id := range ids {
		out = append(out, GenericAssayMeta{EntityType: "GENERIC_ASSAY", StableID: id, GenericEntityMetaProperties: props[id]})
	}
	return out
}

// genericAssayMetaProperties returns the meta properties of the generic
// assay entities matching where (over study_id, profile_stable_id and
// entity_stable_id, with placeholders bound in args), keyed by entity stable
// id: every stored property where the raw database has the entity tables of a
// production clone, else the NAME / DESCRIPTION / URL columns of the staged
// matrix. Properties keep their stored order (the importer's, by property
// id), in which Java puts them into its HashMap.
func (s *State) genericAssayMetaProperties(where string, args *sqlArgs) (map[string]javaStringMap, error) {
	if len(s.rawColumns("generic_assay_entity_meta")) > 0 {
		type propRow struct {
			StableID string   `json:"stableId"`
			Keys     []string `json:"keys"`
			Values   []string `json:"vals"`
		}
		rows, err := queryA[propRow](s.CH, fmt.Sprintf(
			"SELECT entity_stable_id AS stableId, mapKeys(properties) AS keys, mapValues(properties) AS vals "+
				"FROM cbioportal_raw.generic_assay_entity_meta "+
				"WHERE entity_stable_id IN (SELECT entity_stable_id FROM cbioportal_raw.generic_assay_profile_entity WHERE %s)", where), args)
		if err != nil {
			return nil, err
		}
		out := make(map[string]javaStringMap, len(rows))
		for _, r := range rows {
			var m javaStringMap
			for i, k := range r.Keys {
				m.set(k, r.Values[i])
			}
			out[r.StableID] = m
		}
		return out, nil
	}
	rows, err := queryA[genericAssayMetaRow](s.CH, fmt.Sprintf(
		"SELECT entity_stable_id AS stableId, any(name) AS name, any(description) AS description, any(url) AS url "+
			"FROM %s WHERE %s GROUP BY entity_stable_id", genericAssaySource(s), where), args)
	if err != nil {
		return nil, err
	}
	out := make(map[string]javaStringMap, len(rows))
	for _, r := range rows {
		var m javaStringMap
		m.set("NAME", r.Name)
		m.set("DESCRIPTION", r.Description)
		m.set("URL", r.URL)
		out[r.StableID] = m
	}
	return out, nil
}

// summaryMetaProperties keeps the properties GenericAssayServiceImpl returns
// for the SUMMARY projection (SUMMARY_META_PROPERTY_NAMES), as the enrichments
// attach them.
func summaryMetaProperties(props map[string]javaStringMap) map[string]javaStringMap {
	out := make(map[string]javaStringMap, len(props))
	for id, m := range props {
		var kept javaStringMap
		for _, k := range m.keys {
			switch k {
			case "NAME", "DESCRIPTION", "URL":
				kept.set(k, m.vals[k])
			}
		}
		out[id] = kept
	}
	return out
}

// routeGenericAssay registers the generic-assay and geneset (GSVA) endpoints.
func (s *State) routeGenericAssay(mux *http.ServeMux) {
	// Generic assay data.
	mux.HandleFunc("POST /api/generic_assay_data/{molecularProfileId}/fetch", s.genericAssayDataInProfileFetch)
	mux.HandleFunc("POST /api/generic_assay_data/fetch", s.genericAssayDataMultipleFetch)

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
func genericAssaySource(s *State) string {
	return s.rawSource("generic_assay_matrix",
		"study_id", "profile_stable_id", "entity_stable_id", "sample_id", "value", "name", "description", "url")
}

// genesetScoreSource is the staged GSVA score matrix.
func genesetScoreSource(s *State) string {
	return s.rawSource("geneset_score_matrix", "study_id", "profile_stable_id", "geneset_id", "sample_id", "value")
}

// geneMatrixCols are the columns read from a staged per-gene matrix.
var geneMatrixCols = []string{"study_id", "profile_stable_id", "sample_id", "hugo_symbol", "entrez_gene_id", "value"}
