package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
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
	if noSamples(f.SampleIDs, f.SampleListID) {
		writeJSON(w, []DiscreteCopyNumberData{})
		return
	}
	s.writeDiscreteCopyNumber(w, r, mpid, f.EntrezGeneIDs, f.SampleIDs, f.SampleListID,
		r.URL.Query().Get("discreteCopyNumberEventType"), projectionOf(r))
}

func (s *State) discreteCopyNumberGet(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("id")
	q := r.URL.Query()
	if blankSampleList(q) {
		writeJSON(w, []DiscreteCopyNumberData{})
		return
	}
	s.writeDiscreteCopyNumber(w, r, mpid, nil, nil, q.Get("sampleListId"), q.Get("discreteCopyNumberEventType"), projectionOf(r))
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

func (s *State) writeDiscreteCopyNumber(w http.ResponseWriter, r *http.Request, mpid string, entrez []int64, sampleIDs []string, sampleListID, eventType, projection string) {
	meta, ok := s.profileMeta(mpid)
	if !ok || !strings.EqualFold(meta.AlterationType, "COPY_NUMBER_ALTERATION") {
		writeJSON(w, []DiscreteCopyNumberData{})
		return
	}
	alterations := cnaEventAlterations(eventType)
	detailed := projection == "DETAILED"
	if projection == "META" && !homdelOrAmpOnly(alterations) {
		// The values discreteCopyNumberFromValues keeps: integers among the
		// alterations.
		args := newArgs()
		sel, _, ok := s.molecularDataSelect(args, mpid, molecularDataFilter{
			EntrezGeneIDs: entrez, SampleIDs: sampleIDs, SampleListID: sampleListID}, "")
		if !ok {
			sel = "SELECT '' AS value WHERE 0"
		}
		sql := fmt.Sprintf("SELECT count() AS n FROM (%s) WHERE %s", sel, cnaValueKept(alterations))
		writeMetaCount(w, r, s.CH, sql, args.params(sql))
		return
	}
	if !homdelOrAmpOnly(alterations) {
		out := newJSONArray(w, r)
		out.finish(s.discreteCopyNumberFromValues(r, mpid, meta, molecularDataFilter{
			EntrezGeneIDs: entrez, SampleIDs: sampleIDs, SampleListID: sampleListID}, alterations, detailed, func(d DiscreteCopyNumberData) error {
			return out.add(d)
		}))
		return
	}
	where := fmt.Sprintf("c.study_id = {study:String} AND c.profile_stable_id = {profile:String} AND toInt64OrNull(c.alteration) IN (%s)",
		intListSQL(alterations))
	p := ch.Params{"study": meta.StudyID, "profile": meta.StableID}
	genes := "cbioportal_ref.gene"
	if len(entrez) > 0 {
		where += fmt.Sprintf(" AND toInt64OrNull(c.entrez_gene_id) IN (%s)", intListSQL(entrez))
		genes = fmt.Sprintf("(SELECT * FROM cbioportal_ref.gene WHERE entrez_gene_id IN (%s))", intListSQL(entrez))
	}
	if len(sampleIDs) > 0 {
		where += " AND c.sample_id IN {samples:Array(String)}"
		p["samples"] = sampleIDs
	} else if sampleListID != "" {
		where += " AND c.sample_id IN " + s.bySampleList(sampleListID).listMembers(paramsBinder(p))
	}
	samples := byStudy(meta.StudyID).samples(paramsBinder(p))
	sel := fmt.Sprintf(
		"SELECT c.sample_id AS sampleId, sm.patient_id AS patientId, "+
			"toInt64OrNull(c.entrez_gene_id) AS entrezGeneId, if(rg.hugo_gene_symbol != '', rg.hugo_gene_symbol, c.hugo_symbol) AS hugoGeneSymbol, "+
			"rg.type AS geneType, toInt64OrNull(c.alteration) AS alteration, c.namespace_columns AS namespaceColumns, "+
			"c.cbp_driver AS driverFilter, c.cbp_driver_annotation AS driverFilterAnnotation, "+
			"c.cbp_driver_tiers AS driverTiersFilter, c.cbp_driver_tiers_annotation AS driverTiersFilterAnnotation "+
			"FROM cna c LEFT JOIN %s sm "+
			"ON c.study_id = sm.study_id AND c.sample_id = sm.sample_id "+
			"LEFT JOIN %s rg ON rg.entrez_gene_id = toInt64OrNull(c.entrez_gene_id) "+
			"WHERE %s AND toInt64OrNull(c.entrez_gene_id) > 0", samples, genes, where)
	if projection == "META" {
		writeMetaCount(w, r, s.CH, "SELECT count() AS n FROM ("+sel+")", p)
		return
	}
	out := newJSONArray(w, r)
	// DiscreteCopyNumberMapper: ORDER BY entrez_gene_id, alteration, sample stable id.
	query := sel + sqlListOrder(r, cnaEventSortable, "entrezGeneId, alteration, c.sample_id")
	err := ch.QueryEachP(s.CH, query, p,
		func(r *discreteCnaRow) error {
			// CnaUtil reads the driver columns with getPartString: empty is "NA".
			d := newDriverAnnotation(r.DriverFilter, r.DriverFilterAnnotation, r.DriverTiersFilter, r.DriverTiersFilterAnnotation, naString)
			rec := DiscreteCopyNumberData{
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
				rec.Gene = &Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol, Type: r.GeneType}
			}
			return out.add(rec)
		})
	out.finish(err)
}

// cnaEventSortable are the sortBy fields of discrete copy-number records
// read from the CNA events.
var cnaEventSortable = map[string]string{
	"entrezGeneId": "entrezGeneId", "sampleId": "sampleId", "patientId": "patientId", "alteration": "alteration",
}

// cnaValueSortable are the sortBy fields of discrete copy-number records
// read from the profile's values.
var cnaValueSortable = map[string]string{
	"entrezGeneId": "entrezGeneId", "sampleId": "sampleId", "patientId": "patientId", "alteration": cnaValueSQL,
}

// cnaValueSQL is a profile value as the integer discreteCopyNumberFromValues
// reads it.
const cnaValueSQL = "toInt64OrNull(replaceRegexpOne(value, '^[+]', ''))"

// cnaValueKept is the SQL predicate over a profile's value column of the
// values discreteCopyNumberFromValues keeps: integers among the
// alterations.
func cnaValueKept(alterations []int64) string {
	return fmt.Sprintf("match(value, '^[+-]?0*[0-9]{1,10}$') AND %s IN (%s)", cnaValueSQL, intListSQL(alterations))
}

// discreteCopyNumberFromValues is the molecular-data path of
// DiscreteCopyNumberServiceImpl: it calls fn with the profile's per-sample
// values whose integer value (Integer.parseInt) is one of the requested
// alterations, in molecular-data order. A page r asks for is read in SQL.
func (s *State) discreteCopyNumberFromValues(r *http.Request, mpid string, meta profileMeta, f molecularDataFilter, alterations []int64,
	detailed bool, fn func(DiscreteCopyNumberData) error) error {
	want := map[int64]bool{}
	for _, a := range alterations {
		want[a] = true
	}
	return s.molecularDataPage(r, mpid, f, cnaValueSortable, cnaValueKept(alterations), func(d MolecularData) error {
		v, err := strconv.ParseInt(d.Value, 10, 32)
		if err != nil || !want[v] || d.EntrezGeneID == nil {
			return nil
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
		return fn(rec)
	})
}

// profileSampleCount is MolecularDataService.getNumberOfSamplesInMolecularProfile:
// the samples of the profile's matrix (genetic_profile_samples), i.e. the
// data file's sample columns that are samples of the study, or the samples of
// a discrete-long file. A profile without such a list counts its profiled
// samples.
func (s *State) profileSampleCount(meta profileMeta, mpid string) int64 {
	p := ch.Params{"study": meta.StudyID, "profile": meta.StableID, "mp": mpid}
	inStudy := " AND sample_id IN (SELECT sample_id FROM " + byStudy(meta.StudyID).from(paramsBinder(p), tSample) + ")"
	if len(s.rawColumns("profile_sample")) > 0 {
		if n := s.countQuery("SELECT uniqExact(sample_id) AS n FROM cbioportal_raw.profile_sample "+
			"WHERE study_id = {study:String} AND profile_stable_id = {profile:String} AND kind = 'header'"+inStudy, p); n > 0 {
			return n
		}
	}
	if strings.EqualFold(meta.Datatype, "DISCRETE_LONG") && len(s.rawColumns("cna_discrete_long")) > 0 {
		return s.countQuery("SELECT uniqExact(sample_id) AS n FROM "+
			byStudy(meta.StudyID).from(paramsBinder(p), rawMatrix("cna_discrete_long"))+" AND profile_stable_id = {profile:String}"+inStudy, p)
	}
	return s.countQuery("SELECT uniqExact(sample_id) AS n FROM sample_profile "+
		"WHERE study_id = {study:String} AND molecular_profile_id = {mp:String}", p)
}

// variantSampleCount is VariantCountServiceImpl.getNumberOfSamplesInMolecularProfile:
// the size of the study's sequenced sample list, else the number of samples
// with a mutation in the profile.
func (s *State) variantSampleCount(meta profileMeta, mpid string) int64 {
	p := ch.Params{"study": meta.StudyID, "mp": mpid}
	if n := s.countQuery("SELECT count() AS n FROM "+s.bySampleList(meta.StudyID+"_sequenced").from(paramsBinder(p), tSampleList), p); n > 0 {
		return n
	}
	return s.countQuery("SELECT uniqExact(sample_id) AS n FROM mutation WHERE study_id = {study:String} AND molecular_profile_id = {mp:String}", p)
}

// countQuery runs a query returning one count column n; 0 on error.
func (s *State) countQuery(sql string, p ch.Params) int64 {
	type c struct {
		N int64 `json:"n"`
	}
	rows, err := ch.QueryP[c](s.CH, sql, p)
	if err != nil || len(rows) == 0 {
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

	total := s.profileSampleCount(meta, mpid)

	pairs := make([]string, len(ids))
	for i, id := range ids {
		pairs[i] = fmt.Sprintf("(%d, %d)", id.EntrezGeneID, id.Alteration)
	}
	rows, err := ch.QueryP[cnCountRow](s.CH, fmt.Sprintf(
		"SELECT entrezGeneId, alteration, uniqExact(sampleId) AS n FROM ("+
			"SELECT toInt64OrNull(entrez_gene_id) AS entrezGeneId, toInt64OrNull(alteration) AS alteration, sample_id AS sampleId "+
			"FROM cna WHERE study_id = {study:String} AND profile_stable_id = {profile:String} AND alteration IN ('-2', '2')) "+
			"WHERE (entrezGeneId, alteration) IN (%s) GROUP BY entrezGeneId, alteration",
		strings.Join(pairs, ", ")), ch.Params{"study": meta.StudyID, "profile": meta.StableID})
	if err != nil {
		fail(w, r, err)
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
	if noSamples(f.SampleIDs, f.SampleListID) {
		writeJSON(w, []struct{}{})
		return
	}
	study := profileStudy(mpid)
	// MutationSpectrumServiceImpl reads the profile's mutations with snpOnly:
	// reference and tumor allele are both single bases.
	where := "m.study_id = {study:String} AND m.molecular_profile_id = {profile:String} " +
		"AND m.reference_allele IN ('A', 'T', 'C', 'G') AND m.tumor_seq_allele2 IN ('A', 'T', 'C', 'G')"
	p := ch.Params{"study": study, "profile": mpid}
	if len(f.SampleIDs) > 0 {
		where += " AND m.sample_id IN {samples:Array(String)}"
		p["samples"] = f.SampleIDs
	} else if f.SampleListID != "" {
		where += " AND m.sample_id IN " + s.bySampleList(f.SampleListID).listMembers(paramsBinder(p))
	}
	samples := byStudy(study).samples(paramsBinder(p))
	// Substitution classes normalised to the pyrimidine strand (C/T reference).
	classes := map[string]string{
		"CtoT": "(m.reference_allele = 'C' AND m.tumor_seq_allele2 = 'T') OR (m.reference_allele = 'G' AND m.tumor_seq_allele2 = 'A')",
		"CtoG": "(m.reference_allele = 'C' AND m.tumor_seq_allele2 = 'G') OR (m.reference_allele = 'G' AND m.tumor_seq_allele2 = 'C')",
		"CtoA": "(m.reference_allele = 'C' AND m.tumor_seq_allele2 = 'A') OR (m.reference_allele = 'G' AND m.tumor_seq_allele2 = 'T')",
		"TtoC": "(m.reference_allele = 'T' AND m.tumor_seq_allele2 = 'C') OR (m.reference_allele = 'A' AND m.tumor_seq_allele2 = 'G')",
		"TtoG": "(m.reference_allele = 'T' AND m.tumor_seq_allele2 = 'G') OR (m.reference_allele = 'A' AND m.tumor_seq_allele2 = 'C')",
		"TtoA": "(m.reference_allele = 'T' AND m.tumor_seq_allele2 = 'A') OR (m.reference_allele = 'A' AND m.tumor_seq_allele2 = 'T')",
	}
	rows, err := ch.QueryP[spectrumRow](s.CH, fmt.Sprintf(
		"SELECT m.sample_id AS sampleId, sm.patient_id AS patientId, "+
			"countIf(%s) AS CtoT, countIf(%s) AS CtoG, countIf(%s) AS CtoA, "+
			"countIf(%s) AS TtoC, countIf(%s) AS TtoG, countIf(%s) AS TtoA "+
			"FROM mutation m LEFT JOIN %s sm "+
			"ON m.study_id = sm.study_id AND m.sample_id = sm.sample_id "+
			"WHERE %s GROUP BY m.sample_id, sm.patient_id ORDER BY m.sample_id",
		classes["CtoT"], classes["CtoG"], classes["CtoA"], classes["TtoC"], classes["TtoG"], classes["TtoA"], samples, where), p)
	if err != nil {
		fail(w, r, err)
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
	MolecularProfileID                string `json:"molecularProfileId"`
	EntrezGeneID                      int64  `json:"entrezGeneId"`
	Keyword                           string `json:"keyword"`
	NumberOfSamples                   int64  `json:"numberOfSamples"`
	NumberOfSamplesWithMutationInGene int64  `json:"numberOfSamplesWithMutationInGene"`
	NumberOfSamplesWithKeyword        int64  `json:"numberOfSamplesWithKeyword"`
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

	// VariantCountServiceImpl.validateMolecularProfile: only a mutation
	// profile is found.
	meta, ok := s.profileMeta(mpid)
	if !ok || (meta.AlterationType != "MUTATION_EXTENDED" && meta.AlterationType != "MUTATION_UNCALLED") {
		writeError(w, http.StatusNotFound, msgMolecularProfileNotFound(mpid))
		return
	}
	total := s.variantSampleCount(meta, mpid)

	// Per-gene sample counts.
	geneCount := map[int64]int64{}
	{
		type gr struct {
			EntrezGeneID *int64 `json:"entrezGeneId"`
			N            int64  `json:"n"`
		}
		rows, _ := ch.QueryP[gr](s.CH, fmt.Sprintf(
			"SELECT toInt64OrNull(entrez_gene_id) AS entrezGeneId, uniqExact(sample_id) AS n "+
				"FROM mutation WHERE molecular_profile_id = {profile:String} AND toInt64OrNull(entrez_gene_id) IN (%s) "+
				"GROUP BY entrezGeneId", intListSQL(entrez)), ch.Params{"profile": mpid})
		for _, r := range rows {
			geneCount[derefInt(r.EntrezGeneID)] = r.N
		}
	}

	// Per-keyword sample counts, with each mutation's keyword derived as the
	// importer stores it on the mutation event.
	keywordCount := map[string]int64{}
	wantKeyword := map[string]bool{}
	for _, id := range ids {
		wantKeyword[fmt.Sprintf("%d\x00%s", id.EntrezGeneID, id.Keyword)] = true
	}
	if len(wantKeyword) > 0 {
		type kr struct {
			EntrezGeneID   *int64 `json:"entrezGeneId"`
			SampleID       string `json:"sampleId"`
			HugoGeneSymbol string `json:"hugoGeneSymbol"`
			ProteinChange  string `json:"proteinChange"`
			MutationType   string `json:"mutationType"`
		}
		rows, _ := ch.QueryP[kr](s.CH, fmt.Sprintf(
			"SELECT DISTINCT toInt64OrNull(m.entrez_gene_id) AS entrezGeneId, m.sample_id AS sampleId, "+
				"if(rg.hugo_gene_symbol != '', rg.hugo_gene_symbol, m.hugo_symbol) AS hugoGeneSymbol, "+
				"m.protein_change AS proteinChange, m.variant_classification AS mutationType "+
				"FROM mutation m LEFT JOIN cbioportal_ref.gene rg ON rg.entrez_gene_id = toInt64OrNull(m.entrez_gene_id) "+
				"WHERE m.molecular_profile_id = {profile:String} AND toInt64OrNull(m.entrez_gene_id) IN (%s)",
			intListSQL(entrez)), ch.Params{"profile": mpid})
		samples := map[string]map[string]bool{}
		for _, r := range rows {
			kw := mutationKeyword(r.HugoGeneSymbol, r.ProteinChange, naString(r.MutationType))
			k := fmt.Sprintf("%d\x00%s", derefInt(r.EntrezGeneID), kw)
			if !wantKeyword[k] {
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

	// VariantCountMapper reads mutation_count_by_keyword, which has a row
	// only for a gene and keyword some mutation has, once per row. Its rows
	// for mutations without a keyword hold the text "null", which the
	// mapper's `keyword IS NULL` never matches: an identifier without a
	// keyword has no count.
	out := make([]VariantCount, 0, len(ids))
	emitted := map[string]bool{}
	for _, id := range ids {
		k := fmt.Sprintf("%d\x00%s", id.EntrezGeneID, id.Keyword)
		if id.Keyword == "" || keywordCount[k] == 0 || emitted[k] {
			continue
		}
		emitted[k] = true
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
	entrez := make([]int64, len(ids))
	for i, id := range ids {
		entrez[i] = id.EntrezGeneID
	}
	// The requested genes' stored mutations, by distinct protein position and
	// change; their positions are the importer's (ExtendedMutationUtil
	// getProteinPosStart / End: the Protein_position column, else parsed from
	// the protein change). MutationMapper.getMutationCountByPosition then
	// counts, per requested position in turn, the mutations lying within it.
	type posRow struct {
		EntrezGeneID    int64  `json:"entrezGeneId"`
		ProteinPosition string `json:"proteinPosition"`
		ProteinChange   string `json:"proteinChange"`
		Count           int64  `json:"count"`
	}
	rows, err := ch.Query[posRow](s.CH, fmt.Sprintf(
		"SELECT toInt64OrZero(entrez_gene_id) AS entrezGeneId, protein_position AS proteinPosition, "+
			"protein_change AS proteinChange, count() AS count FROM mutation "+
			"WHERE toInt64OrZero(entrez_gene_id) IN (%s) GROUP BY entrezGeneId, proteinPosition, proteinChange",
		intListSQL(entrez)))
	if err != nil {
		fail(w, r, err)
		return
	}
	type located struct{ gene, start, end, n int64 }
	muts := make([]located, len(rows))
	for i, r := range rows {
		ps, pe := javaProteinPositions(r.ProteinPosition, r.ProteinChange)
		muts[i] = located{r.EntrezGeneID, ps, pe, r.Count}
	}
	// A position without mutations is null: the mapper's grouped query
	// returns no row for it.
	out := make([]*MutationCountByPosition, 0, len(ids))
	for _, id := range ids {
		var n int64
		for _, m := range muts {
			if m.gene == id.EntrezGeneID && m.start >= id.ProteinPosStart && m.end <= id.ProteinPosEnd {
				n += m.n
			}
		}
		if n == 0 {
			out = append(out, nil)
			continue
		}
		out = append(out, &MutationCountByPosition{Count: n, EntrezGeneID: id.EntrezGeneID,
			ProteinPosStart: id.ProteinPosStart, ProteinPosEnd: id.ProteinPosEnd})
	}
	writeJSON(w, out)
}
