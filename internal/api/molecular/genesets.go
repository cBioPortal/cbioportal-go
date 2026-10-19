package molecular

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/studies"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// Gene sets come from the reference database's geneset tables (loaded by
// `cbioportal-import load-genesets`, as cbioportal-core's gene set importers
// fill Java's). Reference databases without them fall back to the gene set
// ids of the GSVA score matrices, each named after its id, under a single
// hierarchy node.

// Geneset is GenesetDTO; the representative score and p-value are set only in
// the gene set hierarchy.
type Geneset struct {
	GenesetID            string   `json:"genesetId"`
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	RefLink              string   `json:"refLink,omitempty"`
	RepresentativeScore  *float64 `json:"representativeScore,omitempty"`
	RepresentativePvalue *float64 `json:"representativePvalue,omitempty"`
}

func msgGenesetNotFound(id string) string { return "Gene set not found: " + id }

// hasGenesetRef reports whether the reference database has gene set tables:
// those load-genesets writes, or a production clone's (whose version is in
// the info table instead of geneset_info).
func (s Server) hasGenesetRef() bool {
	n, _ := s.genesetRefTables()
	return n == 4
}

// genesetRefTables counts the reference database's gene set tables other
// than the version's, and reports whether it has geneset_info.
func (s Server) genesetRefTables() (n int, versionTable bool) {
	if s.CH == nil {
		return 0, false
	}
	type row struct {
		Name string `json:"name"`
	}
	rows, err := core.CachedQuery[row](s.State, "SELECT name FROM system.tables "+
		"WHERE database = 'cbioportal_ref' AND name IN ('geneset', 'geneset_gene', 'geneset_hierarchy_node', 'geneset_hierarchy_leaf', 'geneset_info')")
	if err != nil {
		return 0, false
	}
	for _, r := range rows {
		if r.Name == "geneset_info" {
			versionTable = true
		} else {
			n++
		}
	}
	return n, versionTable
}

type genesetRefRow struct {
	ID          int64  `json:"id"`
	GenesetID   string `json:"genesetId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	RefLink     string `json:"refLink"`
}

func (r genesetRefRow) dto() Geneset {
	return Geneset{GenesetID: r.GenesetID, Name: r.Name, Description: r.Description, RefLink: r.RefLink}
}

// refGenesets reads gene sets of the reference (GenesetMapper's select)
// matching where, whose placeholders take their values from p.
func (s Server) refGenesets(where string, p ch.Params, order string) ([]genesetRefRow, error) {
	return core.CachedQueryP[genesetRefRow](s.State, fmt.Sprintf(
		"SELECT id, external_id AS genesetId, name, description, ifNull(ref_link, '') AS refLink FROM cbioportal_ref.geneset WHERE %s ORDER BY %s",
		where, order), p)
}

// genesetsForIDs lists gene sets ordered by id, all or those named.
func (s Server) genesetsForIDs(ids []string) ([]Geneset, error) {
	if s.hasGenesetRef() {
		where := "1"
		var p ch.Params
		if ids != nil {
			where = "external_id IN {ids:Array(String)}"
			p = ch.Params{"ids": ids}
		}
		rows, err := s.refGenesets(where, p, "external_id")
		if err != nil {
			return nil, err
		}
		out := make([]Geneset, len(rows))
		for i, r := range rows {
			out[i] = r.dto()
		}
		return out, nil
	}
	where := "1 = 1"
	p := ch.Params{}
	if ids != nil {
		where = "geneset_id IN {ids:Array(String)}"
		p["ids"] = ids
	}
	type row struct {
		GenesetID string `json:"genesetId"`
	}
	rows, err := ch.QueryP[row](s.CH, fmt.Sprintf(
		"SELECT DISTINCT geneset_id AS genesetId FROM %s WHERE %s ORDER BY geneset_id", genesetScoreSource(s.State), where), p)
	if err != nil {
		return nil, err
	}
	out := make([]Geneset, len(rows))
	for i, r := range rows {
		out[i] = Geneset{GenesetID: r.GenesetID, Name: r.GenesetID, Description: r.GenesetID}
	}
	return out, nil
}

// genesetsList serves GET /api/genesets: every gene set by id.
func (s Server) genesetsList(w http.ResponseWriter, r *http.Request) {
	out, err := s.genesetsForIDs(nil)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, out)
}

// genesetsFetch serves POST /api/genesets/fetch: the named gene sets by id.
func (s Server) genesetsFetch(w http.ResponseWriter, r *http.Request) {
	raw := core.BufferedBody(r)
	if core.EmptyJSONArray(raw) {
		core.WriteError(w, http.StatusBadRequest, core.MsgListSize("genesetIds"))
		return
	}
	var ids []string
	core.ReadBody(r, &ids)
	if ids == nil {
		ids = []string{}
	}
	out, err := s.genesetsForIDs(ids)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, out)
}

// genesetGet serves GET /api/genesets/{genesetId}.
func (s Server) genesetGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("genesetId")
	rows, err := s.genesetsForIDs([]string{id})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		core.WriteError(w, http.StatusNotFound, msgGenesetNotFound(id))
		return
	}
	core.WriteJSON(w, rows[0])
}

// SeedGenesetVersion is the gene set version of the seed reference data,
// reported when the reference database has none.
const SeedGenesetVersion = "msigdb_v2025.1.Hs"

// genesetVersion serves GET /api/genesets/version (GenesetMapper
// .getGenesetVersion, the info table's geneset_version): the version
// load-genesets recorded, else that of a reference database with cBioPortal's
// info table (a production clone), else the seed's.
func (s Server) genesetVersion(w http.ResponseWriter, r *http.Request) {
	type row struct {
		V string `json:"v"`
	}
	if _, versionTable := s.genesetRefTables(); versionTable {
		rows, err := core.CachedQuery[row](s.State, "SELECT geneset_version AS v FROM cbioportal_ref.geneset_info LIMIT 1")
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		v := ""
		if len(rows) > 0 {
			v = rows[0].V
		}
		core.WriteJSON(w, v)
		return
	}
	if rows, err := core.CachedQuery[row](s.State, "SELECT geneset_version AS v FROM cbioportal_ref.info LIMIT 1"); err == nil &&
		len(rows) > 0 && rows[0].V != "" {
		core.WriteJSON(w, rows[0].V)
		return
	}
	core.WriteJSON(w, SeedGenesetVersion)
}

// ---------- Gene set scores ----------

// genesetScore is one GSVA-like value of a sample for a gene set.
type genesetScore struct {
	GenesetID string `json:"genesetId"`
	SampleID  string `json:"sampleId"`
	PatientID string `json:"patientId"`
	Value     string `json:"value"`
}

// genesetScores is GenesetDataServiceImpl.fetchGenesetData: the profile's
// samples (all, or those of sampleIds that exist in the study) by sample id,
// and for each the values of every gene set (or those of genesetIds) in gene
// set order.
func (s Server) genesetScores(meta core.ProfileMeta, sampleIDs, genesetIDs []string) ([]genesetScore, error) {
	where := "m.study_id = {study:String} AND m.profile_stable_id = {profile:String}"
	p := ch.Params{"study": meta.StudyID, "profile": meta.StableID}
	if sampleIDs != nil {
		where += " AND m.sample_id IN {samples:Array(String)}"
		p["samples"] = sampleIDs
	}
	if len(genesetIDs) > 0 {
		where += " AND m.geneset_id IN {genesets:Array(String)}"
		p["genesets"] = genesetIDs
	}
	order := "m.sample_id, m.geneset_id"
	join := ""
	if s.hasGenesetRef() {
		// Java joins the values to the gene set table, in the order the gene
		// sets were loaded.
		join = "JOIN cbioportal_ref.geneset g ON g.external_id = m.geneset_id "
		order = "m.sample_id, g.id"
	}
	return ch.QueryP[genesetScore](s.CH, fmt.Sprintf(
		"SELECT m.geneset_id AS genesetId, m.sample_id AS sampleId, sm.patient_id AS patientId, "+core.JavaTruncatedValueSQL("m.value")+" AS value "+
			"FROM %s m JOIN %s sm ON sm.study_id = m.study_id AND sm.sample_id = m.sample_id %s"+
			"WHERE %s ORDER BY %s", genesetScoreSource(s.State), core.ByStudy(meta.StudyID).Samples(core.ParamsBinder(p)), join, where, order), p)
}

// GenesetMolecularData is GenesetMolecularDataDTO as the controller fills it.
type GenesetMolecularData struct {
	GeneticProfileID string `json:"geneticProfileId"`
	SampleID         string `json:"sampleId"`
	PatientID        string `json:"patientId"`
	StudyID          string `json:"studyId"`
	Value            string `json:"value"`
	GenesetID        string `json:"genesetId"`
}

// genesetGeneticDataFetch serves
// POST /api/genetic-profiles/{geneticProfileId}/geneset-genetic-data/fetch
// (GenesetDataController): the scores of a sample list's samples (when
// sampleListId is set) or of sampleIds (all the profile's samples without).
func (s Server) genesetGeneticDataFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("geneticProfileId")
	var f struct {
		GenesetIDs   []string `json:"genesetIds"`
		SampleIDs    []string `json:"sampleIds"`
		SampleListID string   `json:"sampleListId"`
	}
	core.ReadBody(r, &f)
	meta, ok := s.ProfileMeta(mpid)
	if !ok {
		core.WriteError(w, http.StatusNotFound, core.MsgMolecularProfileNotFound(mpid))
		return
	}
	sampleIDs := f.SampleIDs
	if strings.TrimSpace(f.SampleListID) != "" {
		ids, found := s.sampleListIDs(f.SampleListID)
		if !found {
			core.WriteError(w, http.StatusNotFound, core.MsgSampleListNotFound(f.SampleListID))
			return
		}
		sampleIDs = ids
	}
	if !strings.EqualFold(meta.AlterationType, "GENESET_SCORE") {
		core.WriteJSON(w, []GenesetMolecularData{})
		return
	}
	rows, err := s.genesetScores(meta, sampleIDs, f.GenesetIDs)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]GenesetMolecularData, len(rows))
	for i, r := range rows {
		out[i] = GenesetMolecularData{GeneticProfileID: mpid, SampleID: r.SampleID, PatientID: r.PatientID,
			StudyID: meta.StudyID, Value: r.Value, GenesetID: r.GenesetID}
	}
	core.WriteJSON(w, out)
}

// sampleListIDs returns the sample ids of a sample list, found false when the
// list does not exist.
func (s Server) sampleListIDs(listID string) ([]string, bool) {
	type row struct {
		SampleID string `json:"sampleId"`
	}
	p := ch.Params{}
	from := s.BySampleList(listID).From(core.ParamsBinder(p), core.TSampleList)
	rows, err := ch.QueryP[row](s.CH, "SELECT sample_id AS sampleId FROM "+from+" ORDER BY sample_id", p)
	if err != nil || len(rows) == 0 {
		if err == nil && !s.RowExists("SELECT 1 FROM "+from+" LIMIT 1", p) {
			return nil, false
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SampleID
	}
	return out, true
}

// ---------- Gene set hierarchy ----------

// GenesetHierarchyInfo is a hierarchy node. A top-level node has no parent
// id and an empty parent name, as the Java backend's ClickHouse LEFT JOIN on
// the parent node returns them.
type GenesetHierarchyInfo struct {
	NodeID         int64     `json:"nodeId"`
	NodeName       string    `json:"nodeName"`
	ParentID       *int64    `json:"parentId,omitempty"`
	ParentNodeName *string   `json:"parentNodeName,omitempty"`
	Genesets       []Geneset `json:"genesets,omitempty"`
	// hasGenesets marks a node whose gene set list was set (possibly empty),
	// which Java serializes as [] rather than leaving out.
	hasGenesets bool
}

func (h GenesetHierarchyInfo) MarshalJSON() ([]byte, error) {
	type plain GenesetHierarchyInfo
	if !h.hasGenesets {
		return json.Marshal(plain(h))
	}
	gs := h.Genesets
	if gs == nil {
		gs = []Geneset{}
	}
	return json.Marshal(struct {
		NodeID         int64     `json:"nodeId"`
		NodeName       string    `json:"nodeName"`
		ParentID       *int64    `json:"parentId,omitempty"`
		ParentNodeName *string   `json:"parentNodeName,omitempty"`
		Genesets       []Geneset `json:"genesets"`
	}{h.NodeID, h.NodeName, h.ParentID, h.ParentNodeName, gs})
}

// hierarchyParam parses a numeric @RequestParam with @Min / @Max bounds,
// answering Spring's conversion or constraint error.
func hierarchyParam(w http.ResponseWriter, q map[string][]string, name, def string, min, max float64, integer bool) (float64, bool) {
	v := def
	if vs, ok := q[name]; ok && len(vs) > 0 {
		v = vs[0]
	}
	var x float64
	var err error
	if integer {
		var n int64
		n, err = strconv.ParseInt(strings.TrimSpace(v), 10, 32)
		x = float64(n)
	} else {
		x, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	if err != nil {
		core.WriteError(w, http.StatusBadRequest, core.MsgIntMismatch(v))
		return 0, false
	}
	bound := func(b float64) string { return strconv.FormatFloat(b, 'f', -1, 64) }
	if x > max {
		core.WriteError(w, http.StatusBadRequest, name+" must be less than or equal to "+bound(max))
		return 0, false
	}
	if x < min {
		core.WriteError(w, http.StatusBadRequest, name+" must be greater than or equal to "+bound(min))
		return 0, false
	}
	return x, true
}

// genesetHierarchyFetch serves POST /api/geneset-hierarchy/fetch
// (GenesetHierarchyController, GenesetHierarchyServiceImpl): the hierarchy
// nodes leading to gene sets that the GSVA-SCORE profile scores and whose
// representative score and p-value pass the thresholds, over the samples of
// sampleListId, the body's sample ids, or the whole study.
func (s Server) genesetHierarchyFetch(w http.ResponseWriter, r *http.Request) {
	var bodyIDs []string
	core.ReadBody(r, &bodyIDs)
	q := r.URL.Query()
	mpid, ok := q["geneticProfileId"]
	if !ok {
		core.WriteError(w, http.StatusBadRequest, "Request parameter is missing: geneticProfileId")
		return
	}
	percentile, ok := hierarchyParam(w, q, "percentile", "75", 1, 100, true)
	if !ok {
		return
	}
	scoreThreshold, ok := hierarchyParam(w, q, "scoreThreshold", "0.4", 0, 1, false)
	if !ok {
		return
	}
	pvalueThreshold, ok := hierarchyParam(w, q, "pvalueThreshold", "0.05", 0, 1, false)
	if !ok {
		return
	}
	meta, ok := s.ProfileMeta(mpid[0])
	if !ok {
		core.WriteError(w, http.StatusNotFound, core.MsgMolecularProfileNotFound(mpid[0]))
		return
	}
	var sampleIDs []string
	switch listID := q.Get("sampleListId"); {
	case strings.TrimSpace(listID) != "":
		ids, found := s.sampleListIDs(listID)
		if !found {
			core.WriteError(w, http.StatusNotFound, core.MsgSampleListNotFound(listID))
			return
		}
		sampleIDs = ids
	case len(bodyIDs) > 0:
		sampleIDs = bodyIDs
	default:
		sampleIDs = s.studySampleIDs(meta.StudyID)
	}
	if !s.hasGenesetRef() {
		s.genesetHierarchyFromScores(w, r, meta, sampleIDs)
		return
	}
	if meta.Datatype != "GSVA-SCORE" {
		// IllegalArgumentException, which no handler maps.
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	pvalueMeta, ok := s.genesetPvalueProfile(meta)
	if !ok {
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	scores, err := s.genesetScores(meta, sampleIDs, nil)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	pvalues, err := s.genesetScores(pvalueMeta, sampleIDs, nil)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out, err := s.genesetHierarchy(scores, pvalues, int(percentile), scoreThreshold, pvalueThreshold)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, out)
}

// genesetPvalueProfile is the P-VALUE gene set profile of the scores
// profile's study (Java's profile linked to the scores profile).
func (s Server) genesetPvalueProfile(scores core.ProfileMeta) (core.ProfileMeta, bool) {
	type row struct {
		ID string `json:"id"`
	}
	rows, _ := core.CachedQueryP[row](s.State,
		"SELECT molecular_profile_id AS id FROM molecular_profile WHERE study_id = {study:String} "+
			"AND molecular_alteration_type = 'GENESET_SCORE' AND datatype = 'P-VALUE'", ch.Params{"study": scores.StudyID})
	if len(rows) != 1 {
		return core.ProfileMeta{}, false
	}
	return s.ProfileMeta(rows[0].ID)
}

// genesetHierarchy is GenesetHierarchyServiceImpl.getGenesetHierarchyItems.
func (s Server) genesetHierarchy(scores, pvalues []genesetScore, percentile int, scoreThreshold, pvalueThreshold float64) ([]*GenesetHierarchyInfo, error) {
	type nodeRow struct {
		NodeID   int64  `json:"nodeId"`
		NodeName string `json:"nodeName"`
		ParentID int64  `json:"parentId"`
	}
	nodes, err := core.CachedQuery[nodeRow](s.State, "SELECT node_id AS nodeId, node_name AS nodeName, ifNull(parent_id, 0) AS parentId "+
		"FROM cbioportal_ref.geneset_hierarchy_node")
	if err != nil {
		return nil, err
	}
	type leafRow struct {
		NodeID    int64 `json:"nodeId"`
		GenesetID int64 `json:"genesetId"`
	}
	leaves, err := core.CachedQuery[leafRow](s.State, "SELECT node_id AS nodeId, geneset_id AS genesetId FROM cbioportal_ref.geneset_hierarchy_leaf")
	if err != nil {
		return nil, err
	}
	genesets, err := s.refGenesets("1", nil, "id")
	if err != nil {
		return nil, err
	}
	byInternal := map[int64]genesetRefRow{}
	for _, g := range genesets {
		byInternal[g.ID] = g
	}
	nameOf := map[int64]string{}
	for _, n := range nodes {
		nameOf[n.NodeID] = n.NodeName
	}

	scoresByGeneset := map[string][]genesetScore{}
	for _, sc := range scores {
		scoresByGeneset[sc.GenesetID] = append(scoresByGeneset[sc.GenesetID], sc)
	}
	pvalueOf := map[[2]string]string{}
	for _, p := range pvalues {
		pvalueOf[[2]string{p.GenesetID, p.SampleID}] = p.Value
	}

	// Leaf nodes of the scored gene sets (of all gene sets when none is scored).
	scoredLeafNodes := map[int64]bool{}
	for _, l := range leaves {
		if len(scoresByGeneset) == 0 || len(scoresByGeneset[byInternal[l.GenesetID].GenesetID]) > 0 {
			scoredLeafNodes[l.NodeID] = true
		}
	}
	info := func(n nodeRow) *GenesetHierarchyInfo {
		h := &GenesetHierarchyInfo{NodeID: n.NodeID, NodeName: n.NodeName}
		name, ok := nameOf[n.ParentID]
		if ok {
			parent := n.ParentID
			h.ParentID = &parent
		}
		h.ParentNodeName = &name
		return h
	}
	// ORDER BY parent node name (none first), node name.
	sorted := append([]nodeRow(nil), nodes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := nameOf[sorted[i].ParentID], nameOf[sorted[j].ParentID]
		if pi != pj {
			return pi < pj
		}
		if sorted[i].NodeName != sorted[j].NodeName {
			return sorted[i].NodeName < sorted[j].NodeName
		}
		return sorted[i].NodeID < sorted[j].NodeID
	})
	var superNodes, parents []*GenesetHierarchyInfo
	for _, n := range sorted {
		if !scoredLeafNodes[n.NodeID] {
			superNodes = append(superNodes, info(n))
		}
		if scoredLeafNodes[n.NodeID] || len(scoresByGeneset) == 0 {
			parents = append(parents, info(n))
		}
	}

	for _, p := range parents {
		var nodeSets []genesetRefRow
		for _, l := range leaves {
			if l.NodeID == p.NodeID {
				nodeSets = append(nodeSets, byInternal[l.GenesetID])
			}
		}
		sort.SliceStable(nodeSets, func(i, j int) bool { return nodeSets[i].Name < nodeSets[j].Name })
		kept := []Geneset{}
		for _, g := range nodeSets {
			data := scoresByGeneset[g.GenesetID]
			if len(data) == 0 {
				continue
			}
			score, pvalue := representativeScore(data, pvalueOf, percentile)
			if math.Abs(score) >= scoreThreshold && pvalue <= pvalueThreshold {
				d := g.dto()
				d.RepresentativeScore, d.RepresentativePvalue = &score, &pvalue
				kept = append(kept, d)
			}
		}
		p.Genesets, p.hasGenesets = kept, true
		if len(kept) == 0 {
			superNodes = append(superNodes, p)
		}
	}
	var withGenesets []*GenesetHierarchyInfo
	for _, p := range parents {
		if len(p.Genesets) > 0 {
			withGenesets = append(withGenesets, p)
		}
	}
	out := append([]*GenesetHierarchyInfo{}, referredSuperNodes(superNodes, withGenesets)...)
	return append(out, withGenesets...), nil
}

// referredSuperNodes is getFilteredHierarchySuperNodes: the super nodes that
// are parents of the referring nodes, then, recursively, their parents.
func referredSuperNodes(superNodes, referring []*GenesetHierarchyInfo) []*GenesetHierarchyInfo {
	referred := map[int64]bool{}
	for _, n := range referring {
		if n.ParentID != nil {
			referred[*n.ParentID] = true
		}
	}
	contains := func(list []*GenesetHierarchyInfo, n *GenesetHierarchyInfo) bool {
		for _, x := range list {
			if x == n {
				return true
			}
		}
		return false
	}
	var out []*GenesetHierarchyInfo
	for _, n := range superNodes {
		if referred[n.NodeID] && !contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) > 0 {
		for _, p := range referredSuperNodes(superNodes, out) {
			if !contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// representativeScore is calculateAndSetRepresentativeScoreAndPvalue: the
// percentile-th smallest positive and percentile-th largest negative score
// (with the p-value of the same sample), whichever is larger in magnitude.
func representativeScore(data []genesetScore, pvalueOf map[[2]string]string, percentile int) (float64, float64) {
	type pair struct{ score, pvalue float64 }
	var pos, neg []pair
	for _, d := range data {
		score, ok := javaNumber(d.Value)
		if !ok {
			continue
		}
		pvalue := 1.0
		if p, ok := javaNumber(pvalueOf[[2]string{d.GenesetID, d.SampleID}]); ok {
			pvalue = p
		}
		if score >= 0 {
			pos = append(pos, pair{score, pvalue})
		} else {
			neg = append(neg, pair{score, pvalue})
		}
	}
	sort.SliceStable(pos, func(i, j int) bool { return pos[i].score < pos[j].score })
	sort.SliceStable(neg, func(i, j int) bool { return neg[i].score > neg[j].score })
	pick := func(list []pair) pair {
		if len(list) == 0 {
			return pair{0, 1}
		}
		idx := int(math.Floor(float64(percentile)*float64(len(list))/100.0 + 0.5))
		if idx == 0 {
			idx = 1
		}
		return list[idx-1]
	}
	p, n := pick(pos), pick(neg)
	if math.Abs(p.score) > math.Abs(n.score) {
		return p.score, p.pvalue
	}
	return n.score, n.pvalue
}

// javaNumber parses what NumberUtils.isNumber accepts and Double.parseDouble
// reads: decimal numbers with an optional exponent.
func javaNumber(s string) (float64, bool) {
	t := strings.TrimSpace(s)
	if t == "" || strings.ContainsAny(t, "nNiI") {
		return 0, false
	}
	v, err := strconv.ParseFloat(t, 64)
	return v, err == nil
}

// genesetHierarchyFromScores lists, without a gene set reference, the
// profile's scored gene sets under a single node.
func (s Server) genesetHierarchyFromScores(w http.ResponseWriter, r *http.Request, meta core.ProfileMeta, sampleIDs []string) {
	if !strings.EqualFold(meta.AlterationType, "GENESET_SCORE") {
		core.WriteJSON(w, []GenesetHierarchyInfo{})
		return
	}
	type row struct {
		GenesetID string `json:"genesetId"`
	}
	rows, err := ch.QueryP[row](s.CH, fmt.Sprintf(
		"SELECT DISTINCT geneset_id AS genesetId FROM %s "+
			"WHERE study_id = {study:String} AND profile_stable_id = {profile:String} AND sample_id IN {samples:Array(String)} "+
			"ORDER BY geneset_id", genesetScoreSource(s.State)),
		ch.Params{"study": meta.StudyID, "profile": meta.StableID, "samples": sampleIDs})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		core.WriteJSON(w, []GenesetHierarchyInfo{})
		return
	}
	genesets := make([]Geneset, len(rows))
	for i, r := range rows {
		genesets[i] = Geneset{GenesetID: r.GenesetID, Name: r.GenesetID, Description: r.GenesetID}
	}
	core.WriteJSON(w, []GenesetHierarchyInfo{{NodeID: 1, NodeName: "Gene sets", Genesets: genesets, hasGenesets: true}})
}

// genesetGenes lists the member genes of a gene set (entrez id, symbol) by
// entrez id; found is false for an unknown gene set.
func (s Server) genesetGenes(genesetID string) (genes []studies.GeneRow, found bool, err error) {
	sets, err := s.refGenesets("external_id = {id:String}", ch.Params{"id": genesetID}, "id")
	if err != nil || len(sets) == 0 {
		return nil, false, err
	}
	genes, err = ch.Query[studies.GeneRow](s.CH, fmt.Sprintf(
		"SELECT g.entrez_gene_id AS entrezGeneId, g.hugo_gene_symbol AS hugoGeneSymbol, g.type AS type "+
			"FROM cbioportal_ref.geneset_gene gg JOIN cbioportal_ref.gene g ON g.entrez_gene_id = gg.entrez_gene_id "+
			"WHERE gg.geneset_id = %d ORDER BY g.entrez_gene_id", sets[0].ID))
	return genes, true, err
}

// genesetMemberCorrelations is GenesetCorrelationServiceImpl.fetchCorrelatedGenes:
// for each member gene of the gene set, the Spearman correlation of its
// expression with the gene set scores over the samples (the sample list's,
// the body's, or the study's) where both are numbers; 0 with fewer than two
// such samples. Genes below the threshold are dropped, the rest sorted by
// correlation, descending.
func (s Server) genesetMemberCorrelations(w http.ResponseWriter, r *http.Request, meta core.ProfileMeta, genesetID, sampleListID string,
	bodyIDs []string, threshold float64, exprID, exprStableID, zscoreID string) {
	var sampleIDs []string
	switch {
	case strings.TrimSpace(sampleListID) != "":
		ids, found := s.sampleListIDs(sampleListID)
		if !found {
			core.WriteError(w, http.StatusNotFound, core.MsgSampleListNotFound(sampleListID))
			return
		}
		sampleIDs = ids
	case len(bodyIDs) > 0:
		sampleIDs = bodyIDs
	default:
		sampleIDs = s.studySampleIDs(meta.StudyID)
	}
	genes, found, err := s.genesetGenes(genesetID)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if !found {
		core.WriteError(w, http.StatusNotFound, msgGenesetNotFound(genesetID))
		return
	}
	scores, err := s.genesetScores(meta, sampleIDs, []string{genesetID})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	scoreOf := map[string]float64{}
	for _, sc := range scores {
		if v, ok := javaNumber(sc.Value); ok {
			scoreOf[sc.SampleID] = v
		}
	}
	out := []GenesetCorrelation{}
	if len(genes) > 0 {
		entrez := make([]string, len(genes))
		for i, g := range genes {
			entrez[i] = strconv.FormatInt(g.EntrezGeneID, 10)
		}
		type exprRow struct {
			Entrez   int64  `json:"entrez"`
			SampleID string `json:"sampleId"`
			Value    string `json:"value"`
		}
		rows, err := ch.QueryP[exprRow](s.CH, fmt.Sprintf(
			"SELECT toInt64OrZero(entrez_gene_id) AS entrez, sample_id AS sampleId, value FROM %s "+
				"WHERE study_id = {study:String} AND profile_stable_id = {profile:String} AND toInt64OrZero(entrez_gene_id) IN (%s) "+
				"AND sample_id IN {samples:Array(String)}",
			s.RawSource("mrna_expression_matrix", geneMatrixCols...), strings.Join(entrez, ", ")),
			ch.Params{"study": meta.StudyID, "profile": exprStableID, "samples": sampleIDs})
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		exprOf := map[int64]map[string]float64{}
		for _, r := range rows {
			if v, ok := javaNumber(r.Value); ok {
				if exprOf[r.Entrez] == nil {
					exprOf[r.Entrez] = map[string]float64{}
				}
				exprOf[r.Entrez][r.SampleID] = v
			}
		}
		for _, g := range genes {
			var xs, ys []float64
			for _, sid := range sampleIDs {
				x, okX := exprOf[g.EntrezGeneID][sid]
				y, okY := scoreOf[sid]
				if okX && okY {
					xs, ys = append(xs, x), append(ys, y)
				}
			}
			corr := 0.0
			if len(xs) >= 2 {
				corr = Spearman(xs, ys)
			}
			if math.IsNaN(corr) || corr < threshold {
				continue
			}
			out = append(out, GenesetCorrelation{CorrelationValue: corr, EntrezGeneID: g.EntrezGeneID,
				HugoGeneSymbol: g.HugoGeneSymbol, ExpressionGeneticProfileID: exprID, ZScoreGeneticProfileID: zscoreID})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CorrelationValue > out[j].CorrelationValue })
	core.WriteJSON(w, out)
}

// ---------- 4. POST /api/genesets/{genesetId}/expression-correlation/fetch ----------

type GenesetCorrelation struct {
	CorrelationValue           float64 `json:"correlationValue"`
	EntrezGeneID               int64   `json:"entrezGeneId"`
	ExpressionGeneticProfileID string  `json:"expressionGeneticProfileId"`
	HugoGeneSymbol             string  `json:"hugoGeneSymbol"`
	ZScoreGeneticProfileID     string  `json:"zScoreGeneticProfileId"`
}

// genesetExpressionCorrelationFetch computes, for the given geneset, the Spearman
// correlation of its per-sample GSVA score against each gene's mRNA expression
// (like co-expression). The correlation is pushed into ClickHouse (spearmanSQL) so
// only genes above the threshold cross the wire. Shape: []GenesetCorrelation.
func (s Server) genesetExpressionCorrelationFetch(w http.ResponseWriter, r *http.Request) {
	genesetID := r.PathValue("genesetId")

	// Optional body: a custom list of sample ids to restrict the correlation to.
	var sampleIDs []string
	core.ReadBody(r, &sampleIDs)

	gsvaProfile := r.URL.Query().Get("geneticProfileId")
	sampleListID := r.URL.Query().Get("sampleListId")
	threshold := 0.3
	if t := r.URL.Query().Get("correlationThreshold"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil {
			threshold = v
		}
	}

	meta, ok := s.ProfileMeta(gsvaProfile)
	if !ok {
		core.WriteError(w, http.StatusNotFound, core.MsgMolecularProfileNotFound(gsvaProfile))
		return
	}
	if !strings.EqualFold(meta.AlterationType, "GENESET_SCORE") {
		core.WriteJSON(w, []GenesetCorrelation{})
		return
	}

	// Resolve the study's mRNA expression profiles: prefer a continuous profile
	// for the correlation and a z-score profile for the reported z-score id.
	type exprProfile struct {
		ID       string `json:"id"`
		StableID string `json:"stableId"`
		Datatype string `json:"datatype"`
	}
	exprRows, _ := ch.QueryP[exprProfile](s.CH,
		"SELECT molecular_profile_id AS id, profile_stable_id AS stableId, datatype "+
			"FROM molecular_profile WHERE study_id = {study:String} AND molecular_alteration_type = 'MRNA_EXPRESSION' "+
			"ORDER BY molecular_profile_id", ch.Params{"study": meta.StudyID})
	if len(exprRows) == 0 {
		core.WriteJSON(w, []GenesetCorrelation{})
		return
	}
	expr := exprRows[0]
	zscore := exprRows[0]
	for _, p := range exprRows {
		if strings.Contains(strings.ToUpper(p.Datatype), "CONTINUOUS") {
			expr = p
		}
		if strings.Contains(strings.ToUpper(p.Datatype), "Z-SCORE") {
			zscore = p
		}
	}

	if s.hasGenesetRef() {
		s.genesetMemberCorrelations(w, r, meta, genesetID, sampleListID, sampleIDs, threshold, expr.ID, expr.StableID, zscore.ID)
		return
	}

	p := ch.Params{"study": meta.StudyID, "profile": meta.StableID, "geneset": genesetID, "exprProfile": expr.StableID}
	sampleFilter := ""
	if len(sampleIDs) > 0 {
		sampleFilter = " AND sample_id IN {samples:Array(String)}"
		p["samples"] = sampleIDs
	} else if sampleListID != "" {
		sampleFilter = " AND sample_id IN " + s.BySampleList(sampleListID).ListMembers(core.ParamsBinder(p))
	}

	querySub := fmt.Sprintf(
		"SELECT sample_id, toFloat64OrZero(value) AS qval FROM %s "+
			"WHERE study_id = {study:String} AND profile_stable_id = {profile:String} AND geneset_id = {geneset:String} "+
			"AND toFloat64OrNull(value) IS NOT NULL%s",
		genesetScoreSource(s.State), sampleFilter)

	type corrRow struct {
		Entrez *int64   `json:"entity"`
		Hugo   string   `json:"hugo"`
		N      int      `json:"n"`
		SC     *float64 `json:"sc"`
	}
	rows, err := ch.QueryP[corrRow](s.CH, spearmanSQL(fmt.Sprintf(
		"SELECT toInt64OrNull(b.entrez_gene_id) AS entity, b.hugo_symbol AS hugo, "+
			"toFloat64OrZero(b.value) AS x, q.qval AS y "+
			"FROM %s AS b "+
			"INNER JOIN (%s) AS q USING (sample_id) "+
			"WHERE b.study_id = {study:String} AND b.profile_stable_id = {exprProfile:String} "+
			"AND toFloat64OrNull(b.value) IS NOT NULL",
		s.RawSource("mrna_expression_matrix", geneMatrixCols...), querySub),
		"1", threshold)+" ORDER BY sc DESC", p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}

	out := []GenesetCorrelation{}
	for _, row := range rows {
		if row.SC == nil {
			continue
		}
		gc := GenesetCorrelation{
			CorrelationValue:           *row.SC,
			HugoGeneSymbol:             row.Hugo,
			ExpressionGeneticProfileID: expr.ID,
			ZScoreGeneticProfileID:     zscore.ID,
		}
		if row.Entrez != nil {
			gc.EntrezGeneID = *row.Entrez
		}
		out = append(out, gc)
	}
	core.WriteJSON(w, out)
}
