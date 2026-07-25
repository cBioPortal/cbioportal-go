package api

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	// SV counts share the study-view SV aggregation (same shape/inputs).
	mux.HandleFunc("POST /api/structuralvariant-counts/fetch", s.svGenesFetch)
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

type DiscreteCopyNumberData struct {
	Alteration                  int64    `json:"alteration"`
	DriverFilter                string   `json:"driverFilter"`
	DriverFilterAnnotation      string   `json:"driverFilterAnnotation"`
	DriverTiersFilter           string   `json:"driverTiersFilter"`
	DriverTiersFilterAnnotation string   `json:"driverTiersFilterAnnotation"`
	EntrezGeneID                int64    `json:"entrezGeneId"`
	Gene                        Gene     `json:"gene"`
	MolecularProfileID          string          `json:"molecularProfileId"`
	NamespaceColumns            json.RawMessage `json:"namespaceColumns"`
	PatientID                   string          `json:"patientId"`
	SampleID                    string          `json:"sampleId"`
	StudyID                     string          `json:"studyId"`
	UniquePatientKey            string          `json:"uniquePatientKey"`
	UniqueSampleKey             string          `json:"uniqueSampleKey"`
}

type discreteCnaRow struct {
	SampleID         string `json:"sampleId"`
	PatientID        string `json:"patientId"`
	EntrezGeneID     *int64 `json:"entrezGeneId"`
	HugoGeneSymbol   string `json:"hugoGeneSymbol"`
	Alteration       *int64 `json:"alteration"`
	NamespaceColumns string `json:"namespaceColumns"`
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
		r.URL.Query().Get("discreteCopyNumberEventType"))
}

func (s *State) discreteCopyNumberGet(w http.ResponseWriter, r *http.Request) {
	mpid := r.PathValue("id")
	q := r.URL.Query()
	s.writeDiscreteCopyNumber(w, mpid, nil, nil, q.Get("sampleListId"), q.Get("discreteCopyNumberEventType"))
}

func (s *State) writeDiscreteCopyNumber(w http.ResponseWriter, mpid string, entrez []int64, sampleIDs []string, sampleListID, eventType string) {
	meta, ok := s.profileMeta(mpid)
	if !ok || !strings.EqualFold(meta.AlterationType, "COPY_NUMBER_ALTERATION") {
		writeJSON(w, []DiscreteCopyNumberData{})
		return
	}
	where := fmt.Sprintf("c.study_id = '%s' AND c.profile_stable_id = '%s' AND toInt64OrNull(c.alteration) IN (%s)",
		sqlLit(meta.StudyID), sqlLit(meta.StableID), intListSQL(cnaEventAlterations(eventType)))
	if len(entrez) > 0 {
		where += fmt.Sprintf(" AND toInt64OrNull(c.entrez_gene_id) IN (%s)", intListSQL(entrez))
	}
	if len(sampleIDs) > 0 {
		where += fmt.Sprintf(" AND c.sample_id IN (%s)", inList(sampleIDs))
	} else if sampleListID != "" {
		where += fmt.Sprintf(" AND c.sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(sampleListID))
	}
	rows, err := ch.Query[discreteCnaRow](s.CH, fmt.Sprintf(
		"SELECT c.sample_id AS sampleId, sm.patient_id AS patientId, "+
			"toInt64OrNull(c.entrez_gene_id) AS entrezGeneId, c.hugo_symbol AS hugoGeneSymbol, "+
			"toInt64OrNull(c.alteration) AS alteration, c.namespace_columns AS namespaceColumns "+
			"FROM cbioportal.cna c LEFT JOIN cbioportal.sample sm "+
			"ON c.study_id = sm.study_id AND c.sample_id = sm.sample_id "+
			"WHERE %s ORDER BY c.sample_id, c.hugo_symbol", where))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]DiscreteCopyNumberData, len(rows))
	for i, r := range rows {
		out[i] = DiscreteCopyNumberData{
			Alteration:         derefInt(r.Alteration),
			EntrezGeneID:       derefInt(r.EntrezGeneID),
			Gene:               Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol},
			MolecularProfileID: mpid,
			PatientID:          r.PatientID,
			SampleID:           r.SampleID,
			StudyID:            meta.StudyID,
			UniquePatientKey:   uniqueKey(meta.StudyID, r.PatientID),
			UniqueSampleKey:    uniqueKey(meta.StudyID, r.SampleID),
			NamespaceColumns:   rawJSONOrEmpty(r.NamespaceColumns),
		}
	}
	writeJSON(w, out)
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---------- Discrete copy-number counts ----------

type CopyNumberCount struct {
	Alteration                          int64  `json:"alteration"`
	EntrezGeneID                        int64  `json:"entrezGeneId"`
	MolecularProfileID                  string `json:"molecularProfileId"`
	NumberOfSamples                     int64  `json:"numberOfSamples"`
	NumberOfSamplesWithAlterationInGene int64  `json:"numberOfSamplesWithAlterationInGene"`
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
	meta, ok := s.profileMeta(mpid)
	if !ok || !strings.EqualFold(meta.AlterationType, "COPY_NUMBER_ALTERATION") || len(ids) == 0 {
		writeJSON(w, []CopyNumberCount{})
		return
	}

	// Total distinct samples profiled in this CNA profile.
	var total int64
	{
		type c struct {
			N int64 `json:"n"`
		}
		tr, _ := ch.Query[c](s.CH, fmt.Sprintf(
			"SELECT uniqExact(sample_id) AS n FROM cbioportal.cna WHERE study_id = '%s' AND profile_stable_id = '%s'",
			sqlLit(meta.StudyID), sqlLit(meta.StableID)))
		if len(tr) > 0 {
			total = tr[0].N
		}
	}

	pairs := make([][2]string, len(ids))
	for i, id := range ids {
		pairs[i] = [2]string{fmt.Sprintf("%d", id.EntrezGeneID), fmt.Sprintf("%d", id.Alteration)}
	}
	rows, err := ch.Query[cnCountRow](s.CH, fmt.Sprintf(
		"SELECT entrezGeneId, alteration, uniqExact(sampleId) AS n FROM ("+
			"SELECT toInt64OrNull(entrez_gene_id) AS entrezGeneId, toInt64OrNull(alteration) AS alteration, sample_id AS sampleId "+
			"FROM cbioportal.cna WHERE study_id = '%s' AND profile_stable_id = '%s') "+
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
		out = append(out, CopyNumberCount{
			Alteration:                          id.Alteration,
			EntrezGeneID:                        id.EntrezGeneID,
			MolecularProfileID:                  mpid,
			NumberOfSamples:                     total,
			NumberOfSamplesWithAlterationInGene: found[[2]int64{id.EntrezGeneID, id.Alteration}],
		})
	}
	writeJSON(w, out)
}

// ---------- Mutation spectrums ----------

type MutationSpectrum struct {
	CtoA               int64  `json:"CtoA"`
	CtoG               int64  `json:"CtoG"`
	CtoT               int64  `json:"CtoT"`
	TtoA               int64  `json:"TtoA"`
	TtoC               int64  `json:"TtoC"`
	TtoG               int64  `json:"TtoG"`
	MolecularProfileID string `json:"molecularProfileId"`
	PatientID          string `json:"patientId"`
	SampleID           string `json:"sampleId"`
	StudyID            string `json:"studyId"`
	UniquePatientKey   string `json:"uniquePatientKey"`
	UniqueSampleKey    string `json:"uniqueSampleKey"`
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
	where := fmt.Sprintf("m.study_id = '%s'", sqlLit(study))
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
			"FROM cbioportal.mutation m LEFT JOIN cbioportal.sample sm "+
			"ON m.study_id = sm.study_id AND m.sample_id = sm.sample_id "+
			"WHERE %s GROUP BY m.sample_id, sm.patient_id ORDER BY m.sample_id",
		classes["CtoT"], classes["CtoG"], classes["CtoA"], classes["TtoC"], classes["TtoG"], classes["TtoA"], where))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]MutationSpectrum, len(rows))
	for i, r := range rows {
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

// mutationKeywordExpr reproduces cBioPortal's mutation "keyword" grouping well
// enough to match keyword-count requests: truncating events collapse to
// "<gene> truncating", point substitutions to "<gene> <wt-residue><position>",
// everything else to "<gene> <proteinChange>".
const mutationKeywordExpr = "multiIf(" +
	"m.variant_classification IN ('Nonsense_Mutation','Frame_Shift_Del','Frame_Shift_Ins','Splice_Site','Nonstop_Mutation','Translation_Start_Site'), concat(m.hugo_symbol, ' truncating'), " +
	"match(m.protein_change, '^[A-Za-z][0-9]+'), concat(m.hugo_symbol, ' ', extract(m.protein_change, '^[A-Za-z][0-9]+')), " +
	"concat(m.hugo_symbol, ' ', m.protein_change))"

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
	study := profileStudy(mpid)

	entrezSet := map[int64]bool{}
	for _, id := range ids {
		entrezSet[id.EntrezGeneID] = true
	}
	entrez := make([]int64, 0, len(entrezSet))
	for e := range entrezSet {
		entrez = append(entrez, e)
	}

	// Total distinct samples in the mutation profile.
	var total int64
	{
		type c struct {
			N int64 `json:"n"`
		}
		tr, _ := ch.Query[c](s.CH, fmt.Sprintf(
			"SELECT uniqExact(sample_id) AS n FROM cbioportal.mutation WHERE study_id = '%s'", sqlLit(study)))
		if len(tr) > 0 {
			total = tr[0].N
		}
	}

	// Per-gene sample counts.
	geneCount := map[int64]int64{}
	{
		type gr struct {
			EntrezGeneID *int64 `json:"entrezGeneId"`
			N            int64  `json:"n"`
		}
		rows, _ := ch.Query[gr](s.CH, fmt.Sprintf(
			"SELECT toInt64OrNull(entrez_gene_id) AS entrezGeneId, uniqExact(sample_id) AS n "+
				"FROM cbioportal.mutation WHERE study_id = '%s' AND toInt64OrNull(entrez_gene_id) IN (%s) "+
				"GROUP BY entrezGeneId", sqlLit(study), intListSQL(entrez)))
		for _, r := range rows {
			geneCount[derefInt(r.EntrezGeneID)] = r.N
		}
	}

	// Per-keyword sample counts (best-effort keyword derivation).
	keywordCount := map[string]int64{}
	var wantKeyword bool
	var kwPairs [][2]string
	for _, id := range ids {
		if id.Keyword != "" {
			wantKeyword = true
			kwPairs = append(kwPairs, [2]string{fmt.Sprintf("%d", id.EntrezGeneID), id.Keyword})
		}
	}
	if wantKeyword {
		type kr struct {
			EntrezGeneID *int64 `json:"entrezGeneId"`
			Keyword      string `json:"keyword"`
			N            int64  `json:"n"`
		}
		rows, _ := ch.Query[kr](s.CH, fmt.Sprintf(
			"SELECT entrezGeneId, keyword, uniqExact(sampleId) AS n FROM ("+
				"SELECT toInt64OrNull(m.entrez_gene_id) AS entrezGeneId, m.sample_id AS sampleId, %s AS keyword "+
				"FROM cbioportal.mutation m WHERE m.study_id = '%s') "+
				"WHERE (entrezGeneId, keyword) IN (%s) GROUP BY entrezGeneId, keyword",
			mutationKeywordExpr, sqlLit(study), tupleIn(kwPairs)))
		for _, r := range rows {
			keywordCount[fmt.Sprintf("%d\x00%s", derefInt(r.EntrezGeneID), r.Keyword)] = r.N
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
	for i, id := range ids {
		valTuples[i] = fmt.Sprintf("(%d, %d, %d)", id.EntrezGeneID, id.ProteinPosStart, id.ProteinPosEnd)
	}
	// Aggregate mutations to (entrez, protein-position) once, then fold each
	// requested (gene, start, end) range against that reduced set.
	sql := fmt.Sprintf(
		"WITH mut AS ("+
			"SELECT toInt64OrNull(if(toInt64OrNull(m.entrez_gene_id) > 0, m.entrez_gene_id, toString(g.entrez_gene_id))) AS g, "+
			"toInt64OrNull(splitByChar('/', m.protein_position)[1]) AS p, count() AS c "+
			"FROM cbioportal_raw.mutation_extended m "+
			"LEFT JOIN cbioportal_ref.gene g ON upper(m.hugo_symbol) = upper(g.hugo_gene_symbol) "+
			"WHERE toInt64OrNull(splitByChar('/', m.protein_position)[1]) IS NOT NULL GROUP BY g, p) "+
			"SELECT ids.e AS entrezGeneId, ids.s AS proteinPosStart, ids.en AS proteinPosEnd, "+
			"toInt64(sum(if(mut.g = ids.e AND mut.p >= ids.s AND mut.p <= ids.en, mut.c, 0))) AS count "+
			"FROM (SELECT * FROM values('e Int64, s Int64, en Int64', %s)) ids "+
			"CROSS JOIN mut GROUP BY entrezGeneId, proteinPosStart, proteinPosEnd",
		strings.Join(valTuples, ", "))
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
