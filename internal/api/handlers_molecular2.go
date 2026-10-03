package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// routeMolecular2 registers discrete copy-number, mutation-spectrum,
// variant-count, mutation-count-by-position and structural-variant-count
// endpoints. All filtering/aggregation is pushed into ClickHouse; Go only maps
// the final rows into the cBioPortal client JSON shapes.
func (s *State) routeMolecular2(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/molecular-profiles/{id}/discrete-copy-number/fetch", s.discreteCopyNumberFetch)
	mux.HandleFunc("GET /api/molecular-profiles/{id}/discrete-copy-number", s.discreteCopyNumberGet)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/discrete-copy-number-counts/fetch", s.copyNumberCountsFetch)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/mutation-spectrums/fetch", s.mutationSpectrumsFetch)
	mux.HandleFunc("POST /api/molecular-profiles/{id}/variant-counts/fetch", s.variantCountsFetch)
	mux.HandleFunc("POST /api/mutation-counts-by-position/fetch", s.mutationCountsByPositionFetch)
	mux.HandleFunc("POST /api/structuralvariant-counts/fetch", s.svCountsFetch)
}

// cnaEventAlterations maps a discreteCopyNumberEventType enum value to the set of
// GISTIC alteration codes it selects. Default (empty) mirrors the Java backend's
// HOMDEL_AND_AMP.
func cnaEventAlterations(eventType string) []int64 {
	switch strings.ToUpper(eventType) {
	case "AMP":
		return []int64{2}
	case "GAIN":
		return []int64{1}
	case "DIPLOID":
		return []int64{0}
	case "HETLOSS":
		return []int64{-1}
	case "HOMDEL":
		return []int64{-2}
	case "ALL":
		return []int64{-2, -1, 0, 1, 2}
	default: // "" or HOMDEL_AND_AMP
		return []int64{-2, 2}
	}
}

func intListSQL(vals []int64) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, ", ")
}

// ---------- Discrete copy-number data ----------

// DiscreteCopyNumberData is the Java model as Jackson writes it (NON_NULL).
// Records from CNA events carry the driver annotation and namespace columns
// when present; records of the molecular-data path (any event type beyond
// HOMDEL / AMP) carry neither. The nested gene is DETAILED-only.
type DiscreteCopyNumberData struct {
	UniqueSampleKey             string          `json:"uniqueSampleKey"`
	UniquePatientKey            string          `json:"uniquePatientKey"`
	MolecularProfileID          string          `json:"molecularProfileId"`
	SampleID                    string          `json:"sampleId"`
	PatientID                   string          `json:"patientId"`
	EntrezGeneID                int64           `json:"entrezGeneId"`
	Gene                        *Gene           `json:"gene,omitempty"`
	StudyID                     string          `json:"studyId"`
	DriverFilter                *string         `json:"driverFilter,omitempty"`
	DriverFilterAnnotation      *string         `json:"driverFilterAnnotation,omitempty"`
	DriverTiersFilter           *string         `json:"driverTiersFilter,omitempty"`
	DriverTiersFilterAnnotation *string         `json:"driverTiersFilterAnnotation,omitempty"`
	Alteration                  int64           `json:"alteration"`
	NamespaceColumns            json.RawMessage `json:"namespaceColumns,omitempty"`
}

type discreteCnaRow struct {
	SampleID         string `json:"sampleId"`
	PatientID        string `json:"patientId"`
	EntrezGeneID     *int64 `json:"entrezGeneId"`
	HugoGeneSymbol   string `json:"hugoGeneSymbol"`
	GeneType         string `json:"geneType"`
	Alteration       *int64 `json:"alteration"`
	NamespaceColumns string `json:"namespaceColumns"`

	DriverFilter                string `json:"driverFilter"`
	DriverFilterAnnotation      string `json:"driverFilterAnnotation"`
	DriverTiersFilter           string `json:"driverTiersFilter"`
	DriverTiersFilterAnnotation string `json:"driverTiersFilterAnnotation"`
}

func (s *State) discreteCopyNumberFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("id")
	var f struct {
		EntrezGeneIDs []int64  `json:"entrezGeneIds"`
		SampleIDs     []string `json:"sampleIds"`
		SampleListID  string   `json:"sampleListId"`
	}
	readBody(r, &f)
	s.writeDiscreteCopyNumber(w, mpid, f.EntrezGeneIDs, f.SampleIDs, f.SampleListID,
		r.URL.Query().Get("discreteCopyNumberEventType"), projectionOf(r))
}

func (s *State) discreteCopyNumberGet(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("id")
	q := r.URL.Query()
	s.writeDiscreteCopyNumber(w, mpid, nil, nil, q.Get("sampleListId"), q.Get("discreteCopyNumberEventType"), projectionOf(r))
}

// homdelOrAmpOnly is DiscreteCopyNumberServiceImpl.isHomdelOrAmpOnly: such
// requests read the CNA events; any other set reads the profile's values.
func homdelOrAmpOnly(alterations []int64) bool {
	for _, a := range alterations {
		if a == -1 || a == 0 || a == 1 {
			return false
		}
	}
	return true
}

func (s *State) writeDiscreteCopyNumber(w http.ResponseWriter, mpid string, entrez []int64, sampleIDs []string, sampleListID, eventType, projection string) {
	meta, ok := s.profileMeta(mpid)
	if !ok || !strings.EqualFold(meta.AlterationType, "COPY_NUMBER_ALTERATION") {
		writeJSON(w, []DiscreteCopyNumberData{})
		return
	}
	alterations := cnaEventAlterations(eventType)
	detailed := projection == "DETAILED"
	if !homdelOrAmpOnly(alterations) {
		out, err := s.discreteCopyNumberFromValues(mpid, meta, molecularDataFilter{
			EntrezGeneIDs: entrez, SampleIDs: sampleIDs, SampleListID: sampleListID}, alterations, detailed)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, out)
		return
	}
	where := fmt.Sprintf("c.study_id = '%s' AND c.profile_stable_id = '%s' AND toInt64OrNull(c.alteration) IN (%s)",
		sqlLit(meta.StudyID), sqlLit(meta.StableID), intListSQL(alterations))
	if len(entrez) > 0 {
		where += fmt.Sprintf(" AND toInt64OrNull(c.entrez_gene_id) IN (%s)", intListSQL(entrez))
	}
	if len(sampleIDs) > 0 {
		where += fmt.Sprintf(" AND c.sample_id IN (%s)", inList(sampleIDs))
	} else if sampleListID != "" {
		where += fmt.Sprintf(" AND c.sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(sampleListID))
	}
	// DiscreteCopyNumberMapper: ORDER BY entrez_gene_id, alteration, sample stable id.
	rows, err := ch.Query[discreteCnaRow](s.CH, fmt.Sprintf(
		"SELECT c.sample_id AS sampleId, sm.patient_id AS patientId, "+
			"toInt64OrNull(c.entrez_gene_id) AS entrezGeneId, if(rg.hugo_gene_symbol != '', rg.hugo_gene_symbol, c.hugo_symbol) AS hugoGeneSymbol, "+
			"rg.type AS geneType, toInt64OrNull(c.alteration) AS alteration, c.namespace_columns AS namespaceColumns, "+
			"c.cbp_driver AS driverFilter, c.cbp_driver_annotation AS driverFilterAnnotation, "+
			"c.cbp_driver_tiers AS driverTiersFilter, c.cbp_driver_tiers_annotation AS driverTiersFilterAnnotation "+
			"FROM cna c LEFT JOIN sample sm "+
			"ON c.study_id = sm.study_id AND c.sample_id = sm.sample_id "+
			"LEFT JOIN cbioportal_ref.gene rg ON rg.entrez_gene_id = toInt64OrNull(c.entrez_gene_id) "+
			"WHERE %s AND toInt64OrNull(c.entrez_gene_id) > 0 ORDER BY entrezGeneId, alteration, c.sample_id", where))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]DiscreteCopyNumberData, len(rows))
	for i, r := range rows {
		d := newDriverAnnotation(r.DriverFilter, r.DriverFilterAnnotation, r.DriverTiersFilter, r.DriverTiersFilterAnnotation,
			func(v string) string { return v })
		out[i] = DiscreteCopyNumberData{
			Alteration:                  derefInt(r.Alteration),
			EntrezGeneID:                derefInt(r.EntrezGeneID),
			MolecularProfileID:          mpid,
			PatientID:                   r.PatientID,
			SampleID:                    r.SampleID,
			StudyID:                     meta.StudyID,
			UniquePatientKey:            uniqueKey(meta.StudyID, r.PatientID),
			UniqueSampleKey:             uniqueKey(meta.StudyID, r.SampleID),
			NamespaceColumns:            namespaceColumnsJSON(r.NamespaceColumns),
			DriverFilter:                d.Filter,
			DriverFilterAnnotation:      d.FilterAnn,
			DriverTiersFilter:           d.TiersFilter,
			DriverTiersFilterAnnotation: d.TiersFilterAnn,
		}
		if detailed {
			out[i].Gene = &Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol, Type: r.GeneType}
		}
	}
	writeJSON(w, out)
}

// discreteCopyNumberFromValues is the molecular-data path of
// DiscreteCopyNumberServiceImpl: the profile's per-sample values whose
// integer value (Integer.parseInt) is one of the requested alterations, in
// molecular-data order.
func (s *State) discreteCopyNumberFromValues(mpid string, meta profileMeta, f molecularDataFilter, alterations []int64, detailed bool) ([]DiscreteCopyNumberData, error) {
	data, err := s.molecularData(mpid, f)
	if err != nil {
		return nil, err
	}
	want := map[int64]bool{}
	for _, a := range alterations {
		want[a] = true
	}
	out := []DiscreteCopyNumberData{}
	for _, d := range data {
		v, err := strconv.ParseInt(d.Value, 10, 32)
		if err != nil || !want[v] || d.EntrezGeneID == nil {
			continue
		}
		rec := DiscreteCopyNumberData{
			UniqueSampleKey: d.UniqueSampleKey, UniquePatientKey: d.UniquePatientKey,
			MolecularProfileID: mpid, SampleID: d.SampleID, PatientID: d.PatientID,
			EntrezGeneID: *d.EntrezGeneID, StudyID: meta.StudyID, Alteration: v,
		}
		if detailed {
			g := d.Gene
			rec.Gene = &g
		}
		out = append(out, rec)
	}
	return out, nil
}

// profileSampleCount is MolecularDataService.getNumberOfSamplesInMolecularProfile:
// every sample of the profile, altered or not.
func (s *State) profileSampleCount(mpid string) int64 {
	type c struct {
		N int64 `json:"n"`
	}
	rows, _ := ch.Query[c](s.CH, fmt.Sprintf(
		"SELECT uniqExact(sample_id) AS n FROM sample_profile WHERE molecular_profile_id = '%s'", sqlLit(mpid)))
	if len(rows) == 0 {
		return 0
	}
	return rows[0].N
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---------- Discrete copy-number counts ----------

// CopyNumberCount is the legacy model; the altered-sample count is null (left
// out) for a gene and alteration without events.
type CopyNumberCount struct {
	MolecularProfileID                  string `json:"molecularProfileId"`
	EntrezGeneID                        int64  `json:"entrezGeneId"`
	Alteration                          int64  `json:"alteration"`
	NumberOfSamples                     int64  `json:"numberOfSamples"`
	NumberOfSamplesWithAlterationInGene *int64 `json:"numberOfSamplesWithAlterationInGene,omitempty"`
}

type cnCountRow struct {
	EntrezGeneID *int64 `json:"entrezGeneId"`
	Alteration   *int64 `json:"alteration"`
	N            int64  `json:"n"`
}

func (s *State) copyNumberCountsFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("id")
	var ids []struct {
		EntrezGeneID int64 `json:"entrezGeneId"`
		Alteration   int64 `json:"alteration"`
	}
	readBody(r, &ids)
	if len(ids) == 0 || len(ids) > 50000 {
		writeError(w, http.StatusBadRequest, "copyNumberCountIdentifiers size must be between 1 and 50000")
		return
	}
	// DiscreteCopyNumberServiceImpl.validateMolecularProfile: only a discrete
	// CNA profile is found (DISCRETE_LONG files are stored as DISCRETE).
	meta, ok := s.profileMeta(mpid)
	if !ok || meta.AlterationType != "COPY_NUMBER_ALTERATION" || (meta.Datatype != "DISCRETE" && meta.Datatype != "DISCRETE_LONG") {
		writeError(w, http.StatusNotFound, msgMolecularProfileNotFound(mpid))
		return
	}

	total := s.profileSampleCount(mpid)

	pairs := make([][2]string, len(ids))
	for i, id := range ids {
		pairs[i] = [2]string{fmt.Sprintf("%d", id.EntrezGeneID), fmt.Sprintf("%d", id.Alteration)}
	}
	rows, err := ch.Query[cnCountRow](s.CH, fmt.Sprintf(
		"SELECT entrezGeneId, alteration, uniqExact(sampleId) AS n FROM ("+
			"SELECT toInt64OrNull(entrez_gene_id) AS entrezGeneId, toInt64OrNull(alteration) AS alteration, sample_id AS sampleId "+
			"FROM cna WHERE study_id = '%s' AND profile_stable_id = '%s' AND alteration IN ('-2', '2')) "+
			"WHERE (entrezGeneId, alteration) IN (%s) GROUP BY entrezGeneId, alteration",
		sqlLit(meta.StudyID), sqlLit(meta.StableID), tupleIn(pairs)))
	if err != nil {
		fail(w, err)
		return
	}
	found := map[[2]int64]int64{}
	for _, r := range rows {
		found[[2]int64{derefInt(r.EntrezGeneID), derefInt(r.Alteration)}] = r.N
	}
	out := make([]CopyNumberCount, 0, len(ids))
	for _, id := range ids {
		c := CopyNumberCount{
			Alteration:         id.Alteration,
			EntrezGeneID:       id.EntrezGeneID,
			MolecularProfileID: mpid,
			NumberOfSamples:    total,
		}
		if n, ok := found[[2]int64{id.EntrezGeneID, id.Alteration}]; ok {
			c.NumberOfSamplesWithAlterationInGene = &n
		}
		out = append(out, c)
	}
	writeJSON(w, out)
}

// ---------- Mutation spectrums ----------

// MutationSpectrum is MutationSpectrumDTO, fields in its record order.
type MutationSpectrum struct {
	UniqueSampleKey    string `json:"uniqueSampleKey"`
	UniquePatientKey   string `json:"uniquePatientKey"`
	MolecularProfileID string `json:"molecularProfileId"`
	SampleID           string `json:"sampleId"`
	PatientID          string `json:"patientId"`
	StudyID            string `json:"studyId"`
	CtoA               int64  `json:"CtoA"`
	CtoG               int64  `json:"CtoG"`
	CtoT               int64  `json:"CtoT"`
	TtoA               int64  `json:"TtoA"`
	TtoC               int64  `json:"TtoC"`
	TtoG               int64  `json:"TtoG"`
}

type spectrumRow struct {
	SampleID  string `json:"sampleId"`
	PatientID string `json:"patientId"`
	CtoA      int64  `json:"CtoA"`
	CtoG      int64  `json:"CtoG"`
	CtoT      int64  `json:"CtoT"`
	TtoA      int64  `json:"TtoA"`
	TtoC      int64  `json:"TtoC"`
	TtoG      int64  `json:"TtoG"`
}

func (s *State) mutationSpectrumsFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("id")
	var f struct {
		SampleIDs    []string `json:"sampleIds"`
		SampleListID string   `json:"sampleListId"`
	}
	readBody(r, &f)
	study := profileStudy(mpid)
	// MutationSpectrumServiceImpl reads the profile's mutations with snpOnly:
	// reference and tumor allele are both single bases.
	where := fmt.Sprintf("m.study_id = '%s' AND m.molecular_profile_id = '%s' "+
		"AND m.reference_allele IN ('A', 'T', 'C', 'G') AND m.tumor_seq_allele2 IN ('A', 'T', 'C', 'G')",
		sqlLit(study), sqlLit(mpid))
	if len(f.SampleIDs) > 0 {
		where += fmt.Sprintf(" AND m.sample_id IN (%s)", inList(f.SampleIDs))
	} else if f.SampleListID != "" {
		where += fmt.Sprintf(" AND m.sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(f.SampleListID))
	}
	// Substitution classes normalised to the pyrimidine strand (C/T reference).
	classes := map[string]string{
		"CtoT": "(m.reference_allele = 'C' AND m.tumor_seq_allele2 = 'T') OR (m.reference_allele = 'G' AND m.tumor_seq_allele2 = 'A')",
		"CtoG": "(m.reference_allele = 'C' AND m.tumor_seq_allele2 = 'G') OR (m.reference_allele = 'G' AND m.tumor_seq_allele2 = 'C')",
		"CtoA": "(m.reference_allele = 'C' AND m.tumor_seq_allele2 = 'A') OR (m.reference_allele = 'G' AND m.tumor_seq_allele2 = 'T')",
		"TtoC": "(m.reference_allele = 'T' AND m.tumor_seq_allele2 = 'C') OR (m.reference_allele = 'A' AND m.tumor_seq_allele2 = 'G')",
		"TtoG": "(m.reference_allele = 'T' AND m.tumor_seq_allele2 = 'G') OR (m.reference_allele = 'A' AND m.tumor_seq_allele2 = 'C')",
		"TtoA": "(m.reference_allele = 'T' AND m.tumor_seq_allele2 = 'A') OR (m.reference_allele = 'A' AND m.tumor_seq_allele2 = 'T')",
	}
	rows, err := ch.Query[spectrumRow](s.CH, fmt.Sprintf(
		"SELECT m.sample_id AS sampleId, sm.patient_id AS patientId, "+
			"countIf(%s) AS CtoT, countIf(%s) AS CtoG, countIf(%s) AS CtoA, "+
			"countIf(%s) AS TtoC, countIf(%s) AS TtoG, countIf(%s) AS TtoA "+
			"FROM mutation m LEFT JOIN sample sm "+
			"ON m.study_id = sm.study_id AND m.sample_id = sm.sample_id "+
			"WHERE %s GROUP BY m.sample_id, sm.patient_id ORDER BY m.sample_id",
		classes["CtoT"], classes["CtoG"], classes["CtoA"], classes["TtoC"], classes["TtoG"], classes["TtoA"], where))
	if err != nil {
		fail(w, err)
		return
	}
	// One entry per sample, in the iteration order of the HashMap the
	// mutations are grouped into.
	bySample := make(map[string]spectrumRow, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		bySample[r.SampleID] = r
		ids[i] = r.SampleID
	}
	out := make([]MutationSpectrum, len(rows))
	for i, id := range javaHashMapOrder(ids) {
		r := bySample[id]
		out[i] = MutationSpectrum{
			CtoA: r.CtoA, CtoG: r.CtoG, CtoT: r.CtoT, TtoA: r.TtoA, TtoC: r.TtoC, TtoG: r.TtoG,
			MolecularProfileID: mpid, PatientID: r.PatientID, SampleID: r.SampleID, StudyID: study,
			UniquePatientKey: uniqueKey(study, r.PatientID), UniqueSampleKey: uniqueKey(study, r.SampleID),
		}
	}
	writeJSON(w, out)
}

// ---------- Variant counts ----------

type VariantCount struct {
	EntrezGeneID                      int64  `json:"entrezGeneId"`
	Keyword                           string `json:"keyword"`
	MolecularProfileID                string `json:"molecularProfileId"`
	NumberOfSamples                   int64  `json:"numberOfSamples"`
	NumberOfSamplesWithKeyword        int64  `json:"numberOfSamplesWithKeyword"`
	NumberOfSamplesWithMutationInGene int64  `json:"numberOfSamplesWithMutationInGene"`
}

func (s *State) variantCountsFetch(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("id")
	var ids []struct {
		EntrezGeneID int64  `json:"entrezGeneId"`
		Keyword      string `json:"keyword"`
	}
	readBody(r, &ids)
	if len(ids) == 0 {
		writeJSON(w, []VariantCount{})
		return
	}

	entrezSet := map[int64]bool{}
	for _, id := range ids {
		entrezSet[id.EntrezGeneID] = true
	}
	entrez := make([]int64, 0, len(entrezSet))
	for e := range entrezSet {
		entrez = append(entrez, e)
	}

	total := s.profileSampleCount(mpid)

	// Per-gene sample counts.
	geneCount := map[int64]int64{}
	{
		type gr struct {
			EntrezGeneID *int64 `json:"entrezGeneId"`
			N            int64  `json:"n"`
		}
		rows, _ := ch.Query[gr](s.CH, fmt.Sprintf(
			"SELECT toInt64OrNull(entrez_gene_id) AS entrezGeneId, uniqExact(sample_id) AS n "+
				"FROM mutation WHERE molecular_profile_id = '%s' AND toInt64OrNull(entrez_gene_id) IN (%s) "+
				"GROUP BY entrezGeneId", sqlLit(mpid), intListSQL(entrez)))
		for _, r := range rows {
			geneCount[derefInt(r.EntrezGeneID)] = r.N
		}
	}

	// Per-keyword sample counts, with each mutation's keyword derived as the
	// importer stores it on the mutation event.
	keywordCount := map[string]int64{}
	wantKeyword := map[string]bool{}
	for _, id := range ids {
		if id.Keyword != "" {
			wantKeyword[fmt.Sprintf("%d\x00%s", id.EntrezGeneID, id.Keyword)] = true
		}
	}
	if len(wantKeyword) > 0 {
		type kr struct {
			EntrezGeneID   *int64 `json:"entrezGeneId"`
			SampleID       string `json:"sampleId"`
			HugoGeneSymbol string `json:"hugoGeneSymbol"`
			ProteinChange  string `json:"proteinChange"`
			MutationType   string `json:"mutationType"`
		}
		rows, _ := ch.Query[kr](s.CH, fmt.Sprintf(
			"SELECT DISTINCT toInt64OrNull(m.entrez_gene_id) AS entrezGeneId, m.sample_id AS sampleId, "+
				"if(rg.hugo_gene_symbol != '', rg.hugo_gene_symbol, m.hugo_symbol) AS hugoGeneSymbol, "+
				"m.protein_change AS proteinChange, m.variant_classification AS mutationType "+
				"FROM mutation m LEFT JOIN cbioportal_ref.gene rg ON rg.entrez_gene_id = toInt64OrNull(m.entrez_gene_id) "+
				"WHERE m.molecular_profile_id = '%s' AND toInt64OrNull(m.entrez_gene_id) IN (%s)",
			sqlLit(mpid), intListSQL(entrez)))
		samples := map[string]map[string]bool{}
		for _, r := range rows {
			kw := mutationKeyword(r.HugoGeneSymbol, r.ProteinChange, naString(r.MutationType))
			k := fmt.Sprintf("%d\x00%s", derefInt(r.EntrezGeneID), kw)
			if kw == "" || !wantKeyword[k] {
				continue
			}
			if samples[k] == nil {
				samples[k] = map[string]bool{}
			}
			samples[k][r.SampleID] = true
		}
		for k, set := range samples {
			keywordCount[k] = int64(len(set))
		}
	}

	out := make([]VariantCount, 0, len(ids))
	for _, id := range ids {
		out = append(out, VariantCount{
			EntrezGeneID:                      id.EntrezGeneID,
			Keyword:                           id.Keyword,
			MolecularProfileID:                mpid,
			NumberOfSamples:                   total,
			NumberOfSamplesWithKeyword:        keywordCount[fmt.Sprintf("%d\x00%s", id.EntrezGeneID, id.Keyword)],
			NumberOfSamplesWithMutationInGene: geneCount[id.EntrezGeneID],
		})
	}
	writeJSON(w, out)
}

// ---------- Mutation counts by protein position (all studies) ----------

type MutationCountByPosition struct {
	Count           int64 `json:"count"`
	EntrezGeneID    int64 `json:"entrezGeneId"`
	ProteinPosEnd   int64 `json:"proteinPosEnd"`
	ProteinPosStart int64 `json:"proteinPosStart"`
}

type posCountRow struct {
	EntrezGeneID    int64 `json:"entrezGeneId"`
	ProteinPosStart int64 `json:"proteinPosStart"`
	ProteinPosEnd   int64 `json:"proteinPosEnd"`
	Count           int64 `json:"count"`
}

func (s *State) mutationCountsByPositionFetch(w http.ResponseWriter, r *http.Request) {
	var ids []struct {
		EntrezGeneID    int64 `json:"entrezGeneId"`
		ProteinPosStart int64 `json:"proteinPosStart"`
		ProteinPosEnd   int64 `json:"proteinPosEnd"`
	}
	readBody(r, &ids)
	if len(ids) == 0 {
		writeJSON(w, []MutationCountByPosition{})
		return
	}
	valTuples := make([]string, len(ids))
	entrez := make([]int64, len(ids))
	for i, id := range ids {
		valTuples[i] = fmt.Sprintf("(%d, %d, %d)", id.EntrezGeneID, id.ProteinPosStart, id.ProteinPosEnd)
		entrez[i] = id.EntrezGeneID
	}
	genes := intListSQL(entrez)
	// Aggregate the requested genes' mutations (the stored records, under
	// their resolved gene) to (entrez, protein start, protein end) once, then
	// count, per requested (gene, start, end), the mutations lying within it
	// (MutationMapper.getMutationCountByPosition). A protein position is
	// "start[-end][/length]"; a single position is both start and end, and an
	// unknown one ("?") is -1.
	sql := fmt.Sprintf(
		"WITH mut AS ("+
			"SELECT toInt64OrNull(entrez_gene_id) AS g, splitByChar('/', protein_position)[1] AS r, "+
			"if(startsWith(r, '?'), -1, toInt64OrNull(extract(r, '^(-?[0-9]+)'))) AS ps, "+
			"multiIf(endsWith(r, '-?'), -1, match(r, '^(-?[0-9]+|[?])-'), toInt64OrNull(extract(r, '^(?:-?[0-9]+|[?])-(-?[0-9]+)')), ps) AS pe, "+
			"count() AS c "+
			"FROM mutation WHERE g IN (%[2]s) AND ps IS NOT NULL AND pe IS NOT NULL "+
			"GROUP BY g, r, ps, pe) "+
			"SELECT ids.e AS entrezGeneId, ids.s AS proteinPosStart, ids.en AS proteinPosEnd, "+
			"toInt64(sum(if(mut.g = ids.e AND mut.ps >= ids.s AND mut.pe <= ids.en, mut.c, 0))) AS count "+
			"FROM (SELECT * FROM values('e Int64, s Int64, en Int64', %[1]s)) ids "+
			"CROSS JOIN mut GROUP BY entrezGeneId, proteinPosStart, proteinPosEnd",
		strings.Join(valTuples, ", "), genes)
	rows, err := ch.Query[posCountRow](s.CH, sql)
	if err != nil {
		fail(w, err)
		return
	}
	// Only positions with at least one mutation are returned, mirroring Java.
	out := make([]MutationCountByPosition, 0, len(rows))
	for _, r := range rows {
		if r.Count == 0 {
			continue
		}
		out = append(out, MutationCountByPosition{
			Count: r.Count, EntrezGeneID: r.EntrezGeneID,
			ProteinPosEnd: r.ProteinPosEnd, ProteinPosStart: r.ProteinPosStart,
		})
	}
	writeJSON(w, out)
}
