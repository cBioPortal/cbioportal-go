package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// ---------- Studies ----------

type studyRow struct {
	StudyID          string `json:"studyId"`
	CancerTypeID     string `json:"cancerTypeId"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Pmid             string `json:"pmid"`
	Citation         string `json:"citation"`
	Groups           string `json:"groups"`
	ReferenceGenome  string `json:"referenceGenome"`
	AllSampleCount   int64  `json:"allSampleCount"`
	PublicFlag       int    `json:"publicFlag"`
	CancerTypeName   string `json:"cancerTypeName"`
	CancerTypeColor  string `json:"cancerTypeColor"`
	CancerTypeParent string `json:"cancerTypeParent"`
	studyCounts
}

// studyCounts are the per-data-type sample counts (from the study's standard
// case lists), treated patients and samples with structural variants.
type studyCounts struct {
	SequencedSampleCount        int64 `json:"sequencedSampleCount"`
	CnaSampleCount              int64 `json:"cnaSampleCount"`
	MrnaRnaSeqSampleCount       int64 `json:"mrnaRnaSeqSampleCount"`
	MrnaRnaSeqV2SampleCount     int64 `json:"mrnaRnaSeqV2SampleCount"`
	MrnaMicroarraySampleCount   int64 `json:"mrnaMicroarraySampleCount"`
	MiRnaSampleCount            int64 `json:"miRnaSampleCount"`
	MethylationHm27SampleCount  int64 `json:"methylationHm27SampleCount"`
	RppaSampleCount             int64 `json:"rppaSampleCount"`
	MassSpectrometrySampleCount int64 `json:"massSpectrometrySampleCount"`
	CompleteSampleCount         int64 `json:"completeSampleCount"`
	TreatmentCount              int64 `json:"treatmentCount"`
	StructuralVariantCount      int64 `json:"structuralVariantCount"`
}

type CancerStudy struct {
	StudyID         string      `json:"studyId"`
	CancerTypeID    string      `json:"cancerTypeId"`
	Name            string      `json:"name"`
	Description     string      `json:"description"`
	PublicStudy     bool        `json:"publicStudy"`
	Pmid            string      `json:"pmid"`
	Citation        string      `json:"citation"`
	Groups          string      `json:"groups"`
	Status          int         `json:"status"`
	AllSampleCount  int64       `json:"allSampleCount"`
	ReferenceGenome string      `json:"referenceGenome"`
	ReadPermission  bool        `json:"readPermission"`
	CancerType      *CancerType `json:"cancerType"`
	studyCounts
	ResourceCounts []ResourceCount `json:"resourceCounts,omitempty"`
}

type ResourceCount struct {
	ResourceID            string `json:"resourceId"`
	DisplayName           string `json:"displayName"`
	Description           string `json:"description"`
	ResourceType          string `json:"resourceType"`
	Priority              string `json:"priority"`
	OpenByDefault         bool   `json:"openByDefault"`
	CancerStudyIdentifier string `json:"cancerStudyIdentifier"`
	CustomMetaData        string `json:"customMetaData,omitempty"`
	SampleCount           int64  `json:"sampleCount"`
	PatientCount          int64  `json:"patientCount"`
}

// studyCountCols / studyCountJoins add the per-data-type counts of the
// column-store CancerStudyMapper.
const studyCountCols = "toInt64(ifNull(lc.seq, 0)) AS sequencedSampleCount, toInt64(ifNull(lc.cna, 0)) AS cnaSampleCount, " +
	"toInt64(ifNull(lc.rnaseq, 0)) AS mrnaRnaSeqSampleCount, toInt64(ifNull(lc.rnaseqv2, 0)) AS mrnaRnaSeqV2SampleCount, " +
	"toInt64(ifNull(lc.microarray, 0)) AS mrnaMicroarraySampleCount, toInt64(ifNull(lc.mirna, 0)) AS miRnaSampleCount, " +
	"toInt64(ifNull(lc.hm27, 0)) AS methylationHm27SampleCount, toInt64(ifNull(lc.rppa, 0)) AS rppaSampleCount, " +
	"toInt64(ifNull(lc.ms, 0)) AS massSpectrometrySampleCount, toInt64(ifNull(lc.complete, 0)) AS completeSampleCount, " +
	"toInt64(ifNull(tr.n, 0)) AS treatmentCount, toInt64(ifNull(sv.n, 0)) AS structuralVariantCount "

const studyCountJoins = "LEFT JOIN (SELECT study_id, " +
	"countIf(list_id = concat(study_id, '_sequenced')) AS seq, countIf(list_id = concat(study_id, '_cna')) AS cna, " +
	"countIf(list_id = concat(study_id, '_rna_seq_mrna')) AS rnaseq, countIf(list_id = concat(study_id, '_rna_seq_v2_mrna')) AS rnaseqv2, " +
	"countIf(list_id = concat(study_id, '_mrna')) AS microarray, countIf(list_id = concat(study_id, '_microrna')) AS mirna, " +
	"countIf(list_id = concat(study_id, '_methylation_hm27')) AS hm27, countIf(list_id = concat(study_id, '_rppa')) AS rppa, " +
	"countIf(list_id = concat(study_id, '_protein_quantification')) AS ms, countIf(list_id = concat(study_id, '_3way_complete')) AS complete " +
	"FROM sample_list GROUP BY study_id) lc ON c.study_id = lc.study_id " +
	"LEFT JOIN (SELECT cancer_study_identifier AS study_id, uniqExact(patient_unique_id) AS n FROM clinical_event_data_derived " +
	"WHERE event_type IN ('Treatment', 'TREATMENT') GROUP BY cancer_study_identifier) tr ON c.study_id = tr.study_id " +
	"LEFT JOIN (SELECT cancer_study_identifier AS study_id, uniqExact(sample_unique_id) AS n FROM genomic_event_derived " +
	"WHERE variant_type = 'structural_variant' GROUP BY cancer_study_identifier) sv ON c.study_id = sv.study_id "

const studySelect = "SELECT c.study_id AS studyId, c.cancer_type_id AS cancerTypeId, " +
	"c.name AS name, c.description AS description, c.pmid AS pmid, c.citation AS citation, " +
	"c.groups AS groups, c.reference_genome AS referenceGenome, " +
	"toInt64(ifNull(sc.cnt, 0)) AS allSampleCount, " +
	"toUInt8(position(c.groups, 'PUBLIC') > 0) AS publicFlag, " +
	"ifNull(t.name, '') AS cancerTypeName, ifNull(t.dedicated_color, '') AS cancerTypeColor, ifNull(t.parent, '') AS cancerTypeParent, " +
	studyCountCols +
	"FROM cancer_study c LEFT JOIN (SELECT study_id, count() AS cnt FROM sample GROUP BY study_id) sc " +
	"ON c.study_id = sc.study_id " + studyCountJoins +
	"LEFT JOIN (" + studyCancerTypesSQL + ") t ON c.cancer_type_id = t.type_of_cancer_id"

func toCancerStudy(r studyRow) CancerStudy {
	return CancerStudy{
		StudyID: r.StudyID, CancerTypeID: r.CancerTypeID, Name: r.Name, Description: r.Description,
		PublicStudy: r.PublicFlag != 0, Pmid: r.Pmid, Citation: r.Citation, Groups: r.Groups,
		AllSampleCount: r.AllSampleCount, ReferenceGenome: referenceGenome(r.ReferenceGenome), ReadPermission: true,
		studyCounts: r.studyCounts,
		CancerType: &CancerType{
			CancerTypeID: r.CancerTypeID, Name: cancerTypeName(r.CancerTypeName, r.CancerTypeID),
			DedicatedColor: r.CancerTypeColor, ShortName: r.CancerTypeID, Parent: r.CancerTypeParent,
		},
	}
}

// cancerTypeName falls back to the id (then a generic label) so the nested
// cancerType.name the frontend reads is never empty.
func cancerTypeName(name, id string) string {
	if name != "" {
		return name
	}
	if id != "" {
		return id
	}
	return "Other"
}

// studies serves GET /api/studies: keyword search over name / id / cancer
// type, sorting, paging and the META projection's total-count header.
func (s *State) studies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := "1 = 1"
	if kw := strings.TrimSpace(q.Get("keyword")); kw != "" {
		var ands []string
		for term := range strings.FieldsSeq(kw) {
			t := sqlLit(term)
			ands = append(ands, fmt.Sprintf("(c.name ILIKE '%%%[1]s%%' OR c.study_id ILIKE '%%%[1]s%%' OR "+
				"ifNull(t.name, '') ILIKE '%%%[1]s%%' OR c.cancer_type_id ILIKE '%%%[1]s%%')", t))
		}
		where = strings.Join(ands, " AND ")
	}
	order := "c.study_id"
	sortCols := map[string]string{"studyId": "c.study_id", "cancerTypeId": "c.cancer_type_id", "name": "c.name",
		"description": "c.description", "publicStudy": "publicFlag", "pmid": "c.pmid", "citation": "c.citation",
		"groups": "c.groups", "status": "c.study_id", "importDate": "c.study_id"}
	if col, ok := sortCols[q.Get("sortBy")]; ok {
		order = col
		if strings.EqualFold(q.Get("direction"), "DESC") {
			order += " DESC"
		}
	}
	rows, err := cachedQuery[studyRow](s, fmt.Sprintf("%s WHERE %s ORDER BY %s", studySelect, where, order))
	if err != nil {
		fail(w, err)
		return
	}
	if strings.EqualFold(q.Get("projection"), "META") {
		w.Header().Set("total-count", fmt.Sprint(len(rows)))
		w.WriteHeader(http.StatusOK)
		return
	}
	if size, err := strconv.Atoi(q.Get("pageSize")); err == nil && size >= 0 {
		page, _ := strconv.Atoi(q.Get("pageNumber"))
		start := min(page*size, len(rows))
		end := min(start+size, len(rows))
		rows = rows[start:end]
	}
	out := make([]CancerStudy, len(rows))
	for i, r := range rows {
		out[i] = toCancerStudy(r)
	}
	writeJSON(w, s.withResourceCounts(out))
}

func (s *State) study(w http.ResponseWriter, r *http.Request) {
	sql := fmt.Sprintf("%s WHERE c.study_id = '%s'", studySelect, sqlLit(r.PathValue("studyId")))
	rows, err := cachedQuery[studyRow](s, sql)
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "study not found", http.StatusNotFound)
		return
	}
	writeJSON(w, s.withResourceCounts([]CancerStudy{toCancerStudy(rows[0])})[0])
}

func (s *State) studiesFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	if len(ids) == 0 {
		writeJSON(w, []CancerStudy{})
		return
	}
	sql := fmt.Sprintf("%s WHERE c.study_id IN (%s) ORDER BY c.study_id", studySelect, inList(ids))
	rows, err := cachedQuery[studyRow](s, sql)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]CancerStudy, len(rows))
	for i, r := range rows {
		out[i] = toCancerStudy(r)
	}
	writeJSON(w, s.withResourceCounts(out))
}

// ---------- Samples ----------

type idRow struct {
	StudyID   string `json:"studyId"`
	SampleID  string `json:"sampleId"`
	PatientID string `json:"patientId"`
}

type Sample struct {
	SampleID                 string `json:"sampleId"`
	SampleType               string `json:"sampleType"`
	PatientID                string `json:"patientId"`
	StudyID                  string `json:"studyId"`
	Sequenced                bool   `json:"sequenced"`
	CopyNumberSegmentPresent bool   `json:"copyNumberSegmentPresent"`
	UniqueSampleKey          string `json:"uniqueSampleKey"`
	UniquePatientKey         string `json:"uniquePatientKey"`
}

func (s *State) samples(w http.ResponseWriter, r *http.Request) {
	s.writeSamples(w, fmt.Sprintf("s.study_id = '%s'", sqlLit(r.PathValue("studyId"))))
}

type identifier struct {
	StudyID  string `json:"studyId"`
	SampleID string `json:"sampleId"`
	// patient fetches use patientId; entity fetches use entityId.
	PatientID string `json:"patientId"`
	EntityID  string `json:"entityId"`
}

func (i identifier) id() string {
	for _, v := range []string{i.SampleID, i.PatientID, i.EntityID} {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *State) samplesFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers []identifier `json:"sampleIdentifiers"`
		SampleListIDs     []string     `json:"sampleListIds"`
		UniqueSampleKeys  []string     `json:"uniqueSampleKeys"`
	}
	readBody(r, &f)
	// SampleFilter is EITHER explicit (study,sample) identifiers, OR a set of
	// sample-list ids (resolved via the sample_list view), OR base64 unique
	// sample keys. The results view uses the sampleListIds form to expand a case
	// set into its samples.
	var where string
	switch {
	case len(f.SampleIdentifiers) > 0:
		pairs := make([][2]string, len(f.SampleIdentifiers))
		for i, id := range f.SampleIdentifiers {
			pairs[i] = [2]string{id.StudyID, id.id()}
		}
		where = fmt.Sprintf("(s.study_id, s.sample_id) IN (%s)", tupleIn(pairs))
	case len(f.UniqueSampleKeys) > 0:
		var pairs [][2]string
		for _, k := range f.UniqueSampleKeys {
			if st, sid, ok := decodeUniqueKey(k); ok {
				pairs = append(pairs, [2]string{st, sid})
			}
		}
		if len(pairs) == 0 {
			writeJSON(w, []Sample{})
			return
		}
		where = fmt.Sprintf("(s.study_id, s.sample_id) IN (%s)", tupleIn(pairs))
	case len(f.SampleListIDs) > 0:
		where = fmt.Sprintf(
			"(s.study_id, s.sample_id) IN (SELECT study_id, sample_id FROM sample_list WHERE list_id IN (%s))",
			inList(f.SampleListIDs))
	default:
		writeJSON(w, []Sample{})
		return
	}
	s.writeSamples(w, where)
}

// ---------- Patients ----------

type Patient struct {
	PatientID        string `json:"patientId"`
	StudyID          string `json:"studyId"`
	UniqueSampleKey  string `json:"uniqueSampleKey"`
	UniquePatientKey string `json:"uniquePatientKey"`
}

func (s *State) patients(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	rows, err := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, patient_id AS patientId FROM patient WHERE study_id = '%s' ORDER BY patient_id", sid))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toPatients(rows))
}

func (s *State) patientsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		PatientIdentifiers []identifier `json:"patientIdentifiers"`
		UniquePatientKeys  []string     `json:"uniquePatientKeys"`
	}
	readBody(r, &f)
	// PatientFilter is EITHER explicit (studyId, patientId) identifiers OR a list
	// of base64 unique patient keys. The results view (coverage / plots) uses the
	// uniquePatientKeys form.
	var pairs [][2]string
	switch {
	case len(f.PatientIdentifiers) > 0:
		pairs = make([][2]string, len(f.PatientIdentifiers))
		for i, id := range f.PatientIdentifiers {
			pairs[i] = [2]string{id.StudyID, id.id()}
		}
	case len(f.UniquePatientKeys) > 0:
		for _, k := range f.UniquePatientKeys {
			if st, pid, ok := decodeUniqueKey(k); ok {
				pairs = append(pairs, [2]string{st, pid})
			}
		}
	}
	if len(pairs) == 0 {
		writeJSON(w, []Patient{})
		return
	}
	rows, err := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, patient_id AS patientId FROM patient WHERE (study_id, patient_id) IN (%s) ORDER BY study_id, patient_id", tupleIn(pairs)))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toPatients(rows))
}

func toPatients(rows []idRow) []Patient {
	out := make([]Patient, len(rows))
	for i, r := range rows {
		key := uniqueKey(r.StudyID, r.PatientID)
		out[i] = Patient{PatientID: r.PatientID, StudyID: r.StudyID, UniqueSampleKey: key, UniquePatientKey: key}
	}
	return out
}

// ---------- Cancer types ----------

type CancerType struct {
	CancerTypeID   string `json:"cancerTypeId"`
	Name           string `json:"name"`
	DedicatedColor string `json:"dedicatedColor"`
	ShortName      string `json:"shortName"`
	Parent         string `json:"parent"`
}

func (s *State) cancerTypes(w http.ResponseWriter, r *http.Request) {
	out, err := s.queryCancerTypes("")
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

var _ = strings.TrimSpace

// referenceGenome defaults to hg19, the importer's default for studies whose
// meta file names none.
func referenceGenome(g string) string {
	if g == "" {
		return "hg19"
	}
	return g
}

// withResourceCounts attaches each study's resource counts (resources shown in
// the study list's data-type filter).
func (s *State) withResourceCounts(studies []CancerStudy) []CancerStudy {
	if len(studies) == 0 {
		return studies
	}
	ids := make([]string, len(studies))
	for i, st := range studies {
		ids[i] = st.StudyID
	}
	rows, err := cachedQuery[ResourceCount](s, fmt.Sprintf(
		"SELECT rd.resource_id AS resourceId, any(rd.display_name) AS displayName, any(rd.description) AS description, "+
			"any(rd.resource_type) AS resourceType, toString(any(rd.priority)) AS priority, toBool(any(rd.open_by_default) = 1) AS openByDefault, "+
			"rd.study_id AS cancerStudyIdentifier, ifNull(any(rd.custom_metadata), '') AS customMetaData, "+
			"toInt64(uniqExact(x.sample_id)) AS sampleCount, toInt64(uniqExact(x.patient_id)) AS patientCount FROM ("+
			"SELECT study_id, resource_id, sample_id, patient_id FROM resource_sample "+
			"UNION ALL SELECT r.study_id, r.resource_id, s.sample_id, r.patient_id FROM resource_patient r "+
			"LEFT JOIN sample s ON s.study_id = r.study_id AND s.patient_id = r.patient_id) x "+
			"JOIN resource_definition rd ON rd.study_id = x.study_id AND rd.resource_id = x.resource_id "+
			"WHERE x.study_id IN (%s) GROUP BY rd.study_id, rd.resource_id ORDER BY resourceId", inList(ids)))
	if err != nil {
		return studies
	}
	byStudy := map[string][]ResourceCount{}
	for _, r := range rows {
		byStudy[r.CancerStudyIdentifier] = append(byStudy[r.CancerStudyIdentifier], r)
	}
	for i := range studies {
		studies[i].ResourceCounts = byStudy[studies[i].StudyID]
	}
	return studies
}
