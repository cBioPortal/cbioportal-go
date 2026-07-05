package api

import (
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeBulkList registers the unpaginated GET list endpoints, single-resource
// GETs, and assorted infra trivials. Paging query params (pageSize/pageNumber)
// are accepted but ignored — the full list is returned. All filtering is pushed
// into ClickHouse.
func (s *State) routeBulkList(mux *http.ServeMux) {
	// Bulk GET list endpoints (no study scope).
	mux.HandleFunc("GET /api/samples", s.allSamples)
	mux.HandleFunc("GET /api/patients", s.allPatients)
	mux.HandleFunc("GET /api/genes", s.allGenes)
	mux.HandleFunc("GET /api/sample-lists", s.allSampleLists)
	mux.HandleFunc("GET /api/clinical-attributes", s.allClinicalAttributes)

	// Single-resource / patient-view GETs.
	mux.HandleFunc("GET /api/sample-lists/{sampleListId}/sample-ids", s.sampleListSampleIDs)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}", s.sampleInStudy)
	mux.HandleFunc("GET /api/studies/{studyId}/samples/{sampleId}/copy-number-segments", s.sampleCopyNumberSegments)
	mux.HandleFunc("GET /api/studies/{studyId}/tags", s.studyTags)

	// Treatments sample counts (no treatment data ingested).
	mux.HandleFunc("POST /api/treatments/sample-counts/fetch", func(w http.ResponseWriter, r *http.Request) {
		readBody(r, nil)
		writeJSON(w, []any{})
	})

	// Infra trivials — health / timestamps / cache.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{}) })
	mux.HandleFunc("GET /api/timestamps", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{}) })
	mux.HandleFunc("GET /api/cache", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{}) })
	mux.HandleFunc("GET /api/cache/{studyId}", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{}) })

	// Resource endpoints — no resource data ingested; typed empty arrays.
	for _, p := range []string{
		"GET /api/studies/{studyId}/resource-definitions",
		"GET /api/studies/{studyId}/resource-definitions/{resourceId}",
		"GET /api/studies/{studyId}/resource-data",
		"GET /api/studies/{studyId}/resource-data-all",
		"GET /api/studies/{studyId}/patients/{patientId}/resource-data",
		"GET /api/studies/{studyId}/samples/{sampleId}/resource-data",
	} {
		mux.HandleFunc(p, emptyArray)
	}

	// Data-access tokens — only used if download tokens are enabled; placeholder.
	mux.HandleFunc("GET /api/data-access-token", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{}) })
	mux.HandleFunc("POST /api/data-access-token", func(w http.ResponseWriter, r *http.Request) {
		readBody(r, nil)
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("GET /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, []any{}) })
}

// ---------- Bulk samples ----------

// sampleFullRow carries the per-sample flags computed in ClickHouse so the
// all-samples / single-sample responses do not collide sample ids across
// studies (keys are (study_id, sample_id) tuples).
type sampleFullRow struct {
	StudyID       string `json:"studyId"`
	SampleID      string `json:"sampleId"`
	PatientID     string `json:"patientId"`
	SampleType    string `json:"sampleType"`
	SequencedFlag int    `json:"sequencedFlag"`
	SegFlag       int    `json:"segFlag"`
}

func toFullSample(r sampleFullRow) Sample {
	return Sample{
		SampleID: r.SampleID, SampleType: r.SampleType, PatientID: r.PatientID, StudyID: r.StudyID,
		Sequenced: r.SequencedFlag != 0, CopyNumberSegmentPresent: r.SegFlag != 0,
		UniqueSampleKey: uniqueKey(r.StudyID, r.SampleID), UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
	}
}

// sampleFullSelect builds the sample rows plus sampleType / sequenced / seg
// flags for the given WHERE predicate over alias `s`.
func sampleFullSelect(where string) string {
	return "SELECT s.study_id AS studyId, s.sample_id AS sampleId, s.patient_id AS patientId, " +
		"ifNull(cs.attr_value, '') AS sampleType, " +
		"toUInt8((s.study_id, s.sample_id) IN (SELECT study_id, sample_id FROM mutation)) AS sequencedFlag, " +
		"toUInt8((s.study_id, s.sample_id) IN (SELECT study_id, sample_id FROM seg)) AS segFlag " +
		"FROM sample s " +
		"LEFT JOIN (SELECT study_id, sample_id, any(attr_value) AS attr_value FROM clinical_sample " +
		"WHERE attr_id = 'SAMPLE_TYPE' GROUP BY study_id, sample_id) cs " +
		"ON s.study_id = cs.study_id AND s.sample_id = cs.sample_id " +
		"WHERE " + where + " ORDER BY s.study_id, s.sample_id"
}

// allSamples — GET /api/samples. Optional `keyword` (matches sample or patient
// id) and `studyIds` (comma-separated). Returns []Sample.
func (s *State) allSamples(w http.ResponseWriter, r *http.Request) {
	conds := []string{"1"}
	if kw := r.URL.Query().Get("keyword"); kw != "" {
		lit := sqlLit(kw)
		conds = append(conds, fmt.Sprintf(
			"(positionCaseInsensitive(s.sample_id, '%s') > 0 OR positionCaseInsensitive(s.patient_id, '%s') > 0)", lit, lit))
	}
	if sids := r.URL.Query().Get("studyIds"); sids != "" {
		conds = append(conds, fmt.Sprintf("s.study_id IN (%s)", inList(splitCSV(sids))))
	}
	rows, err := ch.Query[sampleFullRow](s.CH, sampleFullSelect(strings.Join(conds, " AND ")))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]Sample, len(rows))
	for i, r := range rows {
		out[i] = toFullSample(r)
	}
	writeJSON(w, out)
}

// sampleInStudy — GET /api/studies/{studyId}/samples/{sampleId}. Single Sample.
func (s *State) sampleInStudy(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	smp := sqlLit(r.PathValue("sampleId"))
	where := fmt.Sprintf("s.study_id = '%s' AND s.sample_id = '%s'", sid, smp)
	rows, err := ch.Query[sampleFullRow](s.CH, sampleFullSelect(where))
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "sample not found", http.StatusNotFound)
		return
	}
	writeJSON(w, toFullSample(rows[0]))
}

// ---------- Bulk patients ----------

// allPatients — GET /api/patients. Returns []Patient across all studies.
func (s *State) allPatients(w http.ResponseWriter, r *http.Request) {
	conds := []string{"1"}
	if sids := r.URL.Query().Get("studyIds"); sids != "" {
		conds = append(conds, fmt.Sprintf("study_id IN (%s)", inList(splitCSV(sids))))
	}
	rows, err := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, patient_id AS patientId FROM patient WHERE %s ORDER BY study_id, patient_id",
		strings.Join(conds, " AND ")))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toPatients(rows))
}

// ---------- Bulk genes ----------

// allGenes — GET /api/genes. Optional `keyword` (matches hugo symbol) and
// `alias` (matches gene_alias). Returns []geneRow.
func (s *State) allGenes(w http.ResponseWriter, r *http.Request) {
	conds := []string{"1"}
	if kw := r.URL.Query().Get("keyword"); kw != "" {
		conds = append(conds, fmt.Sprintf("positionCaseInsensitive(hugo_gene_symbol, '%s') > 0", sqlLit(kw)))
	}
	if alias := r.URL.Query().Get("alias"); alias != "" {
		conds = append(conds, fmt.Sprintf(
			"entrez_gene_id IN (SELECT entrez_gene_id FROM cbioportal_ref.gene_alias WHERE positionCaseInsensitive(gene_alias, '%s') > 0)",
			sqlLit(alias)))
	}
	rows, err := ch.Query[geneRow](s.CH, fmt.Sprintf(
		"SELECT entrez_gene_id AS entrezGeneId, hugo_gene_symbol AS hugoGeneSymbol, type, "+
			"genetic_entity_id AS geneticEntityId FROM cbioportal_ref.gene WHERE %s ORDER BY hugo_gene_symbol",
		strings.Join(conds, " AND ")))
	if err != nil {
		fail(w, err)
		return
	}
	if rows == nil {
		rows = []geneRow{}
	}
	writeJSON(w, rows)
}

// ---------- Bulk sample lists ----------

// allSampleLists — GET /api/sample-lists. Returns []SampleList across studies.
func (s *State) allSampleLists(w http.ResponseWriter, r *http.Request) {
	s.sampleListsQuery(w, "")
}

// sampleListSampleIDs — GET /api/sample-lists/{sampleListId}/sample-ids.
// Returns []string of sample ids in the list.
func (s *State) sampleListSampleIDs(w http.ResponseWriter, r *http.Request) {
	lid := sqlLit(r.PathValue("sampleListId"))
	type row struct {
		SampleID string `json:"sampleId"`
	}
	rows, err := ch.Query[row](s.CH, fmt.Sprintf(
		"SELECT sample_id AS sampleId FROM sample_list WHERE list_id = '%s' ORDER BY sample_id", lid))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SampleID
	}
	writeJSON(w, out)
}

// ---------- Bulk clinical attributes ----------

// allClinicalAttributes — GET /api/clinical-attributes. Returns
// []ClinicalAttribute across all studies from clinical_attribute_meta.
func (s *State) allClinicalAttributes(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.Query[clinAttrRow](s.CH, clinAttrSelect+" ORDER BY study_id, clinical_attribute_id")
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]ClinicalAttribute, len(rows))
	for i, r := range rows {
		out[i] = toClinicalAttribute(r)
	}
	writeJSON(w, out)
}

// ---------- Single-sample copy-number segments ----------

// sampleCopyNumberSegments — GET
// /api/studies/{studyId}/samples/{sampleId}/copy-number-segments. Returns
// []CopyNumberSeg for one sample (same shape as copyNumberSegmentsFetch).
func (s *State) sampleCopyNumberSegments(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	smp := sqlLit(r.PathValue("sampleId"))
	rows, err := ch.Query[segRow](s.CH, fmt.Sprintf(
		"SELECT g.study_id AS studyId, g.sample_id AS sampleId, ifNull(sp.patient_id, '') AS patientId, "+
			"g.chrom AS chromosome, toInt64OrZero(g.start) AS start, toInt64OrZero(g.end) AS end, "+
			"toInt64OrZero(g.num_probes) AS numberOfProbes, toFloat64OrZero(g.segment_mean) AS segmentMean "+
			"FROM seg g LEFT JOIN sample sp ON g.study_id = sp.study_id AND g.sample_id = sp.sample_id "+
			"WHERE g.study_id = '%s' AND g.sample_id = '%s' ORDER BY g.chrom, start", sid, smp))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]CopyNumberSeg, len(rows))
	for i, r := range rows {
		out[i] = CopyNumberSeg{
			UniqueSampleKey:  uniqueKey(r.StudyID, r.SampleID),
			UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
			PatientID:        r.PatientID, SampleID: r.SampleID, StudyID: r.StudyID,
			Chromosome: r.Chromosome, Start: r.Start, End: r.End,
			NumberOfProbes: r.NumberOfProbes, SegmentMean: r.SegmentMean,
			Genes: []any{},
		}
	}
	writeJSON(w, out)
}

// ---------- Study tags ----------

// studyTags — GET /api/studies/{studyId}/tags. No tag data ingested; the
// frontend expects an object, so return an empty one.
func (s *State) studyTags(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{})
}

// splitCSV splits a comma-separated query param, trimming empties.
func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
