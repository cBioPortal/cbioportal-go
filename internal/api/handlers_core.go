package api

import (
	"fmt"
	"net/http"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
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
	Status           int    `json:"status"`
	CancerTypeName   string `json:"cancerTypeName"`
	CancerTypeColor  string `json:"cancerTypeColor"`
	CancerTypeParent string `json:"cancerTypeParent"`
	CancerTypeShort  string `json:"cancerTypeShort"`
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

// CancerStudy is a study as the Java controllers write it (Jackson NON_NULL):
// the summary block is absent from the ID projection, and the per-data-type
// counts and nested cancer type belong to the DETAILED projection only.
type CancerStudy struct {
	StudyID string `json:"studyId"`
	*studySummary
	*studyCounts
	CancerType     *studyCancerType `json:"cancerType,omitempty"`
	ReadPermission bool             `json:"readPermission"`
	ResourceCounts *[]ResourceCount `json:"resourceCounts,omitempty"`
}

// studySummary is the SUMMARY block of a study.
type studySummary struct {
	CancerTypeID    string `json:"cancerTypeId"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	PublicStudy     bool   `json:"publicStudy"`
	Pmid            string `json:"pmid,omitempty"`
	Citation        string `json:"citation,omitempty"`
	Groups          string `json:"groups"`
	Status          int    `json:"status"`
	ImportDate      string `json:"importDate,omitempty"`
	AllSampleCount  int64  `json:"allSampleCount"`
	ReferenceGenome string `json:"referenceGenome"`
}

// studyCancerType is a study's nested cancer type. The column-store study
// endpoints (CancerStudyMetadataDTO) key it as `id`, the legacy ones
// (TypeOfCancerMixin) as `cancerTypeId`.
type studyCancerType struct {
	ID             string `json:"id,omitempty"`
	CancerTypeID   string `json:"cancerTypeId,omitempty"`
	Name           string `json:"name"`
	DedicatedColor string `json:"dedicatedColor"`
	ShortName      string `json:"shortName"`
	Parent         string `json:"parent"`
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

// studyCountCols / studyCountJoins add the counts of the column-store
// CancerStudyMapper: the members of the standard case lists (allSampleCount
// is the `_all` list), treated patients and samples with structural variants.
const studyCountCols = "toInt64(ifNull(lc.seq, 0)) AS sequencedSampleCount, toInt64(ifNull(lc.cna, 0)) AS cnaSampleCount, " +
	"toInt64(ifNull(lc.rnaseq, 0)) AS mrnaRnaSeqSampleCount, toInt64(ifNull(lc.rnaseqv2, 0)) AS mrnaRnaSeqV2SampleCount, " +
	"toInt64(ifNull(lc.microarray, 0)) AS mrnaMicroarraySampleCount, toInt64(ifNull(lc.mirna, 0)) AS miRnaSampleCount, " +
	"toInt64(ifNull(lc.hm27, 0)) AS methylationHm27SampleCount, toInt64(ifNull(lc.rppa, 0)) AS rppaSampleCount, " +
	"toInt64(ifNull(lc.ms, 0)) AS massSpectrometrySampleCount, toInt64(ifNull(lc.complete, 0)) AS completeSampleCount, " +
	"toInt64(ifNull(tr.n, 0)) AS treatmentCount, toInt64(ifNull(sv.n, 0)) AS structuralVariantCount "

const studyCountJoins = "LEFT JOIN (SELECT study_id, countIf(list_id = concat(study_id, '_all')) AS all_samples, " +
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

const studySelectTmpl = "SELECT c.study_id AS studyId, c.cancer_type_id AS cancerTypeId, " +
	"c.name AS name, c.description AS description, c.pmid AS pmid, c.citation AS citation, " +
	"c.groups AS groups, c.reference_genome AS referenceGenome, " +
	"toInt64(ifNull(lc.all_samples, 0)) AS allSampleCount, " +
	"{public} AS publicFlag, {status} AS status, " +
	"ifNull(t.name, '') AS cancerTypeName, ifNull(t.dedicated_color, '') AS cancerTypeColor, ifNull(t.parent, '') AS cancerTypeParent, " +
	"ifNull(t.short_name, '') AS cancerTypeShort, " +
	studyCountCols +
	"FROM cancer_study c " + studyCountJoins +
	"LEFT JOIN (" + cancerTypesSQL + ") t ON c.cancer_type_id = t.type_of_cancer_id"

// studySelect reads the studies with their counts and cancer type. A study is
// public when the portal cancer_study says so (public_study, built from a
// production clone), else when its groups include PUBLIC.
func (s *State) studySelect() string {
	return strings.NewReplacer("{public}", s.studyPublicSQL("c."), "{status}", s.studyStatusSQL("c.")).Replace(studySelectTmpl)
}

// studyStatusSQL is the status of a cancer_study row: 1 (AVAILABLE) unless
// the study is being (re)imported, and for databases built without it.
func (s *State) studyStatusSQL(alias string) string {
	if s.modelHasColumn("cancer_study", "status") {
		return "toInt64(" + alias + "status)"
	}
	return "toInt64(1)"
}

// studyPublicSQL is the UInt8 public flag of a cancer_study row (alias is
// the table alias with its dot, or empty).
func (s *State) studyPublicSQL(alias string) string {
	if s.modelHasColumn("cancer_study", "public_study") {
		return "toUInt8(" + alias + "public_study)"
	}
	return "toUInt8(position(" + alias + "groups, 'PUBLIC') > 0)"
}

// studyShape selects the parts of a study a response carries.
type studyShape struct {
	summary, detailed bool
	// columnStore keys the nested cancer type as `id` (CancerStudyMetadataDTO)
	// rather than `cancerTypeId` (legacy CancerStudy).
	columnStore bool
}

func toCancerStudy(r studyRow, shape studyShape, importDates map[string]string) CancerStudy {
	out := CancerStudy{StudyID: r.StudyID, ReadPermission: true}
	if !shape.summary {
		return out
	}
	out.studySummary = &studySummary{
		CancerTypeID: r.CancerTypeID, Name: r.Name, Description: r.Description, PublicStudy: r.PublicFlag != 0,
		Pmid: r.Pmid, Citation: r.Citation, Groups: r.Groups, Status: r.Status, ImportDate: importDates[r.StudyID],
		AllSampleCount: r.AllSampleCount, ReferenceGenome: referenceGenome(r.ReferenceGenome),
	}
	if shape.detailed {
		counts := r.studyCounts
		out.studyCounts = &counts
		ct := &studyCancerType{
			Name: cancerTypeName(r.CancerTypeName, r.CancerTypeID), DedicatedColor: r.CancerTypeColor,
			ShortName: r.CancerTypeShort, Parent: r.CancerTypeParent,
		}
		if shape.columnStore {
			ct.ID = r.CancerTypeID
		} else {
			ct.CancerTypeID = r.CancerTypeID
		}
		out.CancerType = ct
	}
	return out
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

// studies serves GET /api/studies (ColumnStoreStudyController.getAllStudies):
// keyword search over name / id / cancer type and sorting. The ID projection
// yields an empty list and META an empty list with the total-count header;
// pages are numbered from 1 (see pageList). Studies come in import order, a
// keyword search ordered by name, unless sortBy is given.
func (s *State) studies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := "1 = 1"
	args := newArgs()
	if kw := strings.TrimSpace(q.Get("keyword")); kw != "" {
		var ands []string
		for term := range strings.FieldsSeq(kw) {
			t := args.str("%" + term + "%")
			ands = append(ands, fmt.Sprintf("(c.name ILIKE %[1]s OR c.study_id ILIKE %[1]s OR "+
				"ifNull(t.name, '') ILIKE %[1]s OR c.cancer_type_id ILIKE %[1]s)", t))
		}
		where = strings.Join(ands, " AND ")
	}
	// Unsorted, the mapper returns the studies in cancer_study's storage
	// order (cancer_study_id, i.e. import order), which also orders ties.
	importOrder := s.studyOrder("c.study_id")
	order := importOrder
	if strings.TrimSpace(q.Get("keyword")) != "" {
		order = "c.name, " + importOrder
	}
	sortCols := map[string]string{"studyId": "c.study_id", "cancerTypeId": "c.cancer_type_id", "name": "c.name",
		"description": "c.description", "publicStudy": "publicFlag", "pmid": "c.pmid", "citation": "c.citation",
		"groups": "c.groups", "status": "c.study_id", "importDate": "c.study_id"}
	if col, ok := sortCols[q.Get("sortBy")]; ok {
		order = col
		if strings.EqualFold(q.Get("direction"), "DESC") {
			order += " DESC"
		}
		order += ", " + importOrder
	}
	projection := projectionParam(r)
	if projection == "ID" {
		writeJSON(w, []CancerStudy{})
		return
	}
	rows, err := cachedQueryA[studyRow](s, fmt.Sprintf("%s WHERE %s ORDER BY %s", s.studySelect(), where, order), args)
	if err != nil {
		fail(w, err)
		return
	}
	if projection == "META" {
		w.Header().Set("total-count", fmt.Sprint(len(rows)))
		writeJSON(w, []CancerStudy{})
		return
	}
	shape := studyShape{summary: true, detailed: projection == "DETAILED", columnStore: true}
	dates := s.studyImportDates()
	availability := s.studyAvailabilityEnabled()
	out := make([]CancerStudy, len(rows))
	for i, r := range rows {
		out[i] = toCancerStudy(r, shape, dates)
		// CancerStudyMetadataMapper.toDtos maps readPermission from publicStudy;
		// with study_availability.enabled, nobody can read a study that is being
		// (re)imported.
		out[i].ReadPermission = r.PublicFlag != 0 && (!availability || r.Status == 1)
	}
	writeJSON(w, s.withResourceCounts(out))
}

// study serves GET /api/studies/{studyId} (ColumnStoreStudyController
// .getStudy): the DETAILED study with its resource counts.
func (s *State) study(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("studyId")
	rows, err := cachedQueryP[studyRow](s, s.studySelect()+" WHERE c.study_id = {study:String}", ch.Params{"study": id})
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusNotFound, msgStudyNotFound(id))
		return
	}
	st := toCancerStudy(rows[0], studyShape{summary: true, detailed: true, columnStore: true}, s.studyImportDates())
	writeJSON(w, s.withResourceCounts([]CancerStudy{st})[0])
}

// studiesFetch serves POST /api/studies/fetch (legacy StudyController
// .fetchStudies) in the ID, SUMMARY or DETAILED projection.
func (s *State) studiesFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	if len(ids) == 0 {
		writeJSON(w, []CancerStudy{})
		return
	}
	rows, err := cachedQueryP[studyRow](s, s.studySelect()+" WHERE c.study_id IN {ids:Array(String)} ORDER BY c.study_id",
		ch.Params{"ids": ids})
	if err != nil {
		fail(w, err)
		return
	}
	projection := projectionParam(r)
	shape := studyShape{summary: projection != "ID", detailed: projection == "DETAILED"}
	dates := s.studyImportDates()
	out := make([]CancerStudy, len(rows))
	for i, r := range rows {
		out[i] = toCancerStudy(r, shape, dates)
	}
	if shape.summary {
		out = s.withResourceCounts(out)
	}
	writeJSON(w, out)
}

// studyImportDates maps each study to its import time, formatted as Java's
// importDate: the portal cancer_study's import_date where the database
// carries one (built from a production clone), else the time its study row
// was loaded (the raw cancer_study part it lives in). Studies are absent when
// the raw database is not reachable.
func (s *State) studyImportDates() map[string]string {
	type row struct {
		StudyID    string `json:"studyId"`
		ImportDate string `json:"importDate"`
	}
	out := map[string]string{}
	if s.CH == nil {
		return out
	}
	sql := "SELECT c.study_id AS studyId, " +
		"formatDateTime(max(p.modification_time), '%Y-%m-%d %H:%i:%S') AS importDate " +
		"FROM (SELECT study_id, _part FROM cbioportal_raw.cancer_study) c " +
		"JOIN (SELECT name, modification_time FROM system.parts " +
		"WHERE database = 'cbioportal_raw' AND table = 'cancer_study' AND active) p ON p.name = c._part " +
		"GROUP BY c.study_id"
	if s.modelHasColumn("cancer_study", "import_date") {
		sql = "SELECT study_id AS studyId, formatDateTime(import_date, '%Y-%m-%d %H:%i:%S') AS importDate " +
			"FROM cancer_study WHERE import_date IS NOT NULL"
	}
	rows, err := cachedQuery[row](s, sql)
	if err != nil {
		return out
	}
	for _, r := range rows {
		out[r.StudyID] = r.ImportDate
	}
	return out
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
	args := newArgs()
	s.writeSamples(w, r, "s.study_id = "+args.str(r.PathValue("studyId")), args)
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
	args := newArgs()
	switch {
	case len(f.SampleIdentifiers) > 0:
		pairs := make([][2]string, len(f.SampleIdentifiers))
		for i, id := range f.SampleIdentifiers {
			pairs[i] = [2]string{id.StudyID, id.id()}
		}
		where = "(s.study_id, s.sample_id) IN " + args.pairs(pairs)
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
		where = "(s.study_id, s.sample_id) IN " + args.pairs(pairs)
	case len(f.SampleListIDs) > 0:
		where = s.listStudyCondA(args, "s.study_id", f.SampleListIDs...) +
			"(s.study_id, s.sample_id) IN (SELECT study_id, sample_id FROM sample_list WHERE " +
			s.listStudyCondA(args, "study_id", f.SampleListIDs...) + "list_id IN " + args.strs(f.SampleListIDs) + ")"
	default:
		writeJSON(w, []Sample{})
		return
	}
	s.writeSamples(w, r, where, args)
}

// ---------- Patients ----------

// Patient is the legacy Patient model; the DETAILED projection adds the
// patient's study (see javaContract).
type Patient struct {
	PatientID        string `json:"patientId"`
	StudyID          string `json:"studyId"`
	UniquePatientKey string `json:"uniquePatientKey"`
}

// patientOrder orders patient rows as PatientMapper.getPatients returns
// them: by stable id for the ID projection, else unsorted, i.e. in the patient
// table's internal id order where the database carries it.
func (s *State) patientOrder(r *http.Request) string {
	if projectionOf(r) != "ID" && s.importOrdered() {
		return "internal_id"
	}
	return "patient_id, study_id"
}

// patientSortable is PatientSortBy's column (stableId).
var patientSortable = map[string]string{"patientId": "patient_id"}

func (s *State) patients(w http.ResponseWriter, r *http.Request) {
	s.writePatientRows(w, r, "SELECT study_id AS studyId, patient_id AS patientId FROM patient WHERE study_id = {study:String}"+
		sqlPageOrder(r, patientSortable, s.patientOrder(r)), ch.Params{"study": r.PathValue("studyId")})
}

// writePatientRows streams the patients sql selects, or counts them for
// projection=META.
func (s *State) writePatientRows(w http.ResponseWriter, r *http.Request, sql string, p ch.Params) {
	if projectionOf(r) == "META" {
		writeMetaCount(w, s.CH, "SELECT count() AS n FROM ("+sql+")", p)
		return
	}
	out := newJSONArray(w)
	err := ch.QueryEachP(s.CH, sql, p, func(r *idRow) error {
		return out.add(Patient{PatientID: r.PatientID, StudyID: r.StudyID, UniquePatientKey: uniqueKey(r.StudyID, r.PatientID)})
	})
	out.finish(err)
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
	rows, err := ch.QueryP[idRow](s.CH,
		"SELECT study_id AS studyId, patient_id AS patientId FROM patient WHERE (study_id, patient_id) IN {ids:Array(Tuple(String, String))} "+
			"ORDER BY "+s.patientOrder(r), ch.Params{"ids": pairs})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, toPatients(rows))
}

func toPatients(rows []idRow) []Patient {
	out := make([]Patient, len(rows))
	for i, r := range rows {
		out[i] = Patient{PatientID: r.PatientID, StudyID: r.StudyID, UniquePatientKey: uniqueKey(r.StudyID, r.PatientID)}
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
	out, err := s.queryCancerTypes("", nil)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

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
	rows, err := cachedQueryP[ResourceCount](s,
		"SELECT rd.resource_id AS resourceId, any(rd.display_name) AS displayName, any(rd.description) AS description, "+
			"any(rd.resource_type) AS resourceType, toString(any(rd.priority)) AS priority, toBool(any(rd.open_by_default) = 1) AS openByDefault, "+
			"rd.study_id AS cancerStudyIdentifier, ifNull(any(rd.custom_metadata), '') AS customMetaData, "+
			"toInt64(uniqExact(x.sample_id)) AS sampleCount, toInt64(uniqExact(x.patient_id)) AS patientCount FROM ("+
			"SELECT study_id, resource_id, sample_id, patient_id FROM resource_sample "+
			"UNION ALL SELECT r.study_id, r.resource_id, s.sample_id, r.patient_id FROM resource_patient r "+
			"LEFT JOIN sample s ON s.study_id = r.study_id AND s.patient_id = r.patient_id) x "+
			"JOIN resource_definition rd ON rd.study_id = x.study_id AND rd.resource_id = x.resource_id "+
			"WHERE x.study_id IN {ids:Array(String)} GROUP BY rd.study_id, rd.resource_id ORDER BY resourceId",
		ch.Params{"ids": ids})
	if err != nil {
		rows = nil
	}
	byStudy := map[string][]ResourceCount{}
	for _, r := range rows {
		byStudy[r.CancerStudyIdentifier] = append(byStudy[r.CancerStudyIdentifier], r)
	}
	for i := range studies {
		rc := byStudy[studies[i].StudyID]
		if rc == nil {
			rc = []ResourceCount{}
		}
		studies[i].ResourceCounts = &rc
	}
	return studies
}
