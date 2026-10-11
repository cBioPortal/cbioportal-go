package studies

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
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
	// legacy orders the fields as the legacy CancerStudy model, rather than
	// as the column-store CancerStudyMetadataDTO.
	legacy bool
}

// MarshalJSON writes the study in its model's field order. The column-store
// DTO: the summary fields, the counts, referenceGenome, the treatment and
// structural variant counts, cancerType, readPermission, resourceCounts. The
// legacy model: the summary fields up to importDate, cancerType,
// allSampleCount, the counts, referenceGenome, readPermission, the treatment
// and structural variant counts, resourceCounts.
func (c CancerStudy) MarshalJSON() ([]byte, error) {
	var o core.JSONObject
	sum, cnt := c.studySummary, c.studyCounts
	o.Add("studyId", c.StudyID)
	if sum != nil {
		o.Add("cancerTypeId", sum.CancerTypeID)
		o.Add("name", sum.Name)
		o.Add("description", sum.Description)
		o.Add("publicStudy", sum.PublicStudy)
		o.AddIf(sum.Pmid != "", "pmid", sum.Pmid)
		o.AddIf(sum.Citation != "", "citation", sum.Citation)
		o.Add("groups", sum.Groups)
		o.Add("status", sum.Status)
		o.AddIf(sum.ImportDate != "", "importDate", sum.ImportDate)
	}
	if c.legacy {
		o.AddIf(c.CancerType != nil, "cancerType", c.CancerType)
	}
	if sum != nil {
		o.Add("allSampleCount", sum.AllSampleCount)
	}
	if cnt != nil {
		o.Add("sequencedSampleCount", cnt.SequencedSampleCount)
		o.Add("cnaSampleCount", cnt.CnaSampleCount)
		o.Add("mrnaRnaSeqSampleCount", cnt.MrnaRnaSeqSampleCount)
		o.Add("mrnaRnaSeqV2SampleCount", cnt.MrnaRnaSeqV2SampleCount)
		o.Add("mrnaMicroarraySampleCount", cnt.MrnaMicroarraySampleCount)
		o.Add("miRnaSampleCount", cnt.MiRnaSampleCount)
		o.Add("methylationHm27SampleCount", cnt.MethylationHm27SampleCount)
		o.Add("rppaSampleCount", cnt.RppaSampleCount)
		o.Add("massSpectrometrySampleCount", cnt.MassSpectrometrySampleCount)
		o.Add("completeSampleCount", cnt.CompleteSampleCount)
	}
	if sum != nil {
		o.Add("referenceGenome", sum.ReferenceGenome)
	}
	if c.legacy {
		o.Add("readPermission", c.ReadPermission)
	}
	if cnt != nil {
		o.Add("treatmentCount", cnt.TreatmentCount)
		o.Add("structuralVariantCount", cnt.StructuralVariantCount)
	}
	if !c.legacy {
		o.AddIf(c.CancerType != nil, "cancerType", c.CancerType)
		o.Add("readPermission", c.ReadPermission)
	}
	o.AddIf(c.ResourceCounts != nil, "resourceCounts", c.ResourceCounts)
	return o.Bytes()
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
	// legacy orders the fields as the legacy StudyController writes them:
	// cancerStudyIdentifier last.
	legacy bool
}

func (rc ResourceCount) MarshalJSON() ([]byte, error) {
	type plain ResourceCount
	if !rc.legacy {
		return json.Marshal(plain(rc))
	}
	return json.Marshal(struct {
		ResourceID            string `json:"resourceId"`
		DisplayName           string `json:"displayName"`
		Description           string `json:"description"`
		ResourceType          string `json:"resourceType"`
		Priority              string `json:"priority"`
		OpenByDefault         bool   `json:"openByDefault"`
		CustomMetaData        string `json:"customMetaData,omitempty"`
		SampleCount           int64  `json:"sampleCount"`
		PatientCount          int64  `json:"patientCount"`
		CancerStudyIdentifier string `json:"cancerStudyIdentifier"`
	}{rc.ResourceID, rc.DisplayName, rc.Description, rc.ResourceType, rc.Priority, rc.OpenByDefault,
		rc.CustomMetaData, rc.SampleCount, rc.PatientCount, rc.CancerStudyIdentifier})
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
func (s Server) studySelect() string {
	return strings.NewReplacer("{public}", s.StudyPublicSQL("c."), "{status}", s.studyStatusSQL("c.")).Replace(studySelectTmpl)
}

// studyStatusSQL is the status of a cancer_study row: 1 (AVAILABLE) unless
// the study is being (re)imported, and for databases built without it.
func (s Server) studyStatusSQL(alias string) string {
	if s.ModelHasColumn("cancer_study", "status") {
		return "toInt64(" + alias + "status)"
	}
	return "toInt64(1)"
}

// studyShape selects the parts of a study a response carries.
type studyShape struct {
	summary, detailed bool
	// columnStore keys the nested cancer type as `id` (CancerStudyMetadataDTO)
	// rather than `cancerTypeId` (legacy CancerStudy).
	columnStore bool
}

func toCancerStudy(r studyRow, shape studyShape, importDates map[string]string) CancerStudy {
	out := CancerStudy{StudyID: r.StudyID, ReadPermission: true, legacy: !shape.columnStore}
	if !shape.summary {
		return out
	}
	out.studySummary = &studySummary{
		CancerTypeID: r.CancerTypeID, Name: r.Name, Description: r.Description, PublicStudy: r.PublicFlag != 0,
		Pmid: r.Pmid, Citation: r.Citation, Groups: r.Groups, Status: r.Status, ImportDate: importDates[r.StudyID],
		AllSampleCount: r.AllSampleCount, ReferenceGenome: core.ReferenceGenome(r.ReferenceGenome),
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
func (s Server) studies(w http.ResponseWriter, r *http.Request) {
	if core.IsLegacyAlias(r) {
		s.legacyStudies(w, r)
		return
	}
	q := r.URL.Query()
	where := "1 = 1"
	args := core.NewArgs()
	if kw := strings.TrimSpace(q.Get("keyword")); kw != "" {
		var ands []string
		for term := range strings.FieldsSeq(kw) {
			t := args.Str("%" + term + "%")
			ands = append(ands, fmt.Sprintf("(c.name ILIKE %[1]s OR c.study_id ILIKE %[1]s OR "+
				"ifNull(t.name, '') ILIKE %[1]s OR c.cancer_type_id ILIKE %[1]s)", t))
		}
		where = strings.Join(ands, " AND ")
	}
	// Unsorted, the mapper returns the studies in cancer_study's storage
	// order (cancer_study_id, i.e. import order), which also orders ties.
	importOrder := s.StudyOrder("c.study_id")
	order := importOrder
	if strings.TrimSpace(q.Get("keyword")) != "" {
		order = "c.name, " + importOrder
	}
	if col, ok := studySortCols[q.Get("sortBy")]; ok {
		order = col
		if strings.EqualFold(q.Get("direction"), "DESC") {
			order += " DESC"
		}
		order += ", " + importOrder
	}
	projection := ProjectionParam(r)
	if projection == "ID" {
		core.WriteJSON(w, []CancerStudy{})
		return
	}
	rows, err := core.CachedQueryA[studyRow](s.State, fmt.Sprintf("%s WHERE %s ORDER BY %s", s.studySelect(), where, order), args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if projection == "META" {
		w.Header().Set("total-count", fmt.Sprint(len(rows)))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	shape := studyShape{summary: true, detailed: projection == "DETAILED", columnStore: true}
	dates := s.StudyImportDates()
	availability := s.StudyAvailabilityEnabled()
	out := make([]CancerStudy, len(rows))
	for i, r := range rows {
		out[i] = toCancerStudy(r, shape, dates)
		out[i].ReadPermission = studyReadable(availability, r.Status)
	}
	core.WriteJSON(w, s.withResourceCounts(out))
}

// studySortCols maps the studies' sortBy values (StudySortBy) to columns.
var studySortCols = map[string]string{"studyId": "c.study_id", "cancerTypeId": "c.cancer_type_id", "name": "c.name",
	"description": "c.description", "publicStudy": "publicFlag", "pmid": "c.pmid", "citation": "c.citation",
	"groups": "c.groups", "status": "c.study_id", "importDate": "c.study_id"}

// legacyStudies serves GET /api/legacy/studies (legacy StudyController
// .getAllStudies, StudyMapper.getStudies) in the legacy model: the ID
// projection lists the studies by id, META answers only the total-count
// header, and the nested cancer type is keyed cancerTypeId. A keyword is
// split at each space, and every part must occur, case-sensitively as
// ClickHouse's LIKE matches, in the study's name or id or its cancer type's
// name or id. Keyword matches come in the mapper's order (names starting
// with the keyword, then names containing "tcga", then by name) unless
// sortBy is given. Studies without sample lists are left out, as the
// mapper's inner joins drop them.
func (s Server) legacyStudies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projection := ProjectionParam(r)
	args := core.NewArgs()
	conds := []string{"ifNull(lc.study_id, '') != ''"}
	importOrder := s.StudyOrder("c.study_id")
	order := importOrder
	if projection == "ID" {
		order = "c.study_id"
	}
	if q.Has("keyword") {
		kw := q.Get("keyword")
		for _, term := range javaSplitSpace(kw) {
			t := args.Str("%" + term + "%")
			conds = append(conds, fmt.Sprintf("(c.name LIKE %[1]s OR c.study_id LIKE %[1]s OR "+
				"ifNull(t.name, '') LIKE %[1]s OR ifNull(t.type_of_cancer_id, '') LIKE %[1]s)", t))
		}
		order = fmt.Sprintf("c.name LIKE %s DESC, c.name LIKE '%%tcga%%' DESC, c.name, %s", args.Str(kw+"%"), importOrder)
	}
	if q.Has("keyword") || projection == "DETAILED" {
		conds = append(conds, "ifNull(t.type_of_cancer_id, '') != ''")
	}
	if col, ok := studySortCols[q.Get("sortBy")]; ok {
		if strings.EqualFold(q.Get("direction"), "DESC") {
			col += " DESC"
		}
		order = col + ", " + order
	}
	rows, err := core.CachedQueryA[studyRow](s.State, fmt.Sprintf("%s WHERE %s ORDER BY %s",
		s.studySelect(), strings.Join(conds, " AND "), order), args)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	var extra []studyRow
	if q.Has("keyword") {
		if extra, err = s.primarySiteStudies(q.Get("keyword"), rows); err != nil {
			core.Fail(w, r, err)
			return
		}
	}
	if projection == "META" {
		w.Header().Set("total-count", fmt.Sprint(len(rows)+len(extra)))
		w.WriteHeader(http.StatusOK)
		return
	}
	shape := studyShape{summary: projection != "ID", detailed: projection == "DETAILED"}
	dates := s.StudyImportDates()
	availability := s.StudyAvailabilityEnabled()
	out := make([]CancerStudy, len(rows), len(rows)+len(extra))
	for i, r := range rows {
		out[i] = toCancerStudy(r, shape, dates)
		out[i].ReadPermission = studyReadable(availability, r.Status)
	}
	if shape.summary {
		out = s.withResourceCounts(out)
	}
	for _, r := range extra {
		st := toCancerStudy(r, studyShape{summary: true}, dates)
		st.ReadPermission = studyReadable(availability, r.Status)
		out = append(out, st)
	}
	core.WriteJSON(w, out)
}

// primarySiteStudies are the studies a legacy keyword search adds after its
// matches (StudyServiceImpl.findPrimarySiteMatchingStudies): those whose
// cancer type's primary site (its ancestor right below "tissue") has an id
// or name containing the whole keyword, ignoring case, and that are not
// matches already. They come in import order, in the SUMMARY projection
// and without resource counts, whatever the request's projection.
func (s Server) primarySiteStudies(keyword string, matches []studyRow) ([]studyRow, error) {
	types, err := s.QueryCancerTypes("", nil)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]CancerType, len(types))
	for _, t := range types {
		byID[t.CancerTypeID] = t
	}
	kw := strings.ToLower(keyword)
	var matching []string
	for _, t := range types {
		if t.CancerTypeID == tissue {
			continue
		}
		site, ok := primarySite(byID, t)
		if ok && (strings.Contains(strings.ToLower(site.CancerTypeID), kw) || strings.Contains(strings.ToLower(site.Name), kw)) {
			matching = append(matching, t.CancerTypeID)
		}
	}
	if len(matching) == 0 {
		return nil, nil
	}
	args := core.NewArgs()
	rows, err := core.CachedQueryA[studyRow](s.State, fmt.Sprintf("%s WHERE ifNull(lc.study_id, '') != '' AND c.cancer_type_id IN %s ORDER BY %s",
		s.studySelect(), args.Strs(matching), s.StudyOrder("c.study_id")), args)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(matches))
	for _, r := range matches {
		seen[r.StudyID] = true
	}
	var out []studyRow
	for _, r := range rows {
		if !seen[r.StudyID] {
			out = append(out, r)
		}
	}
	return out, nil
}

// tissue is the root of the cancer type tree.
const tissue = "tissue"

// primarySite returns the ancestor of t whose parent is the root ("tissue"),
// or t itself when its parent is; false when the chain breaks off.
func primarySite(byID map[string]CancerType, t CancerType) (CancerType, bool) {
	for range len(byID) + 1 {
		if t.Parent == tissue {
			return t, true
		}
		p, ok := byID[t.Parent]
		if !ok {
			return CancerType{}, false
		}
		t = p
	}
	return CancerType{}, false
}

// javaSplitSpace splits s as Java's s.split(" ") does: at every space,
// keeping empty parts except trailing ones.
func javaSplitSpace(s string) []string {
	parts := strings.Split(s, " ")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// study serves GET /api/studies/{studyId} (ColumnStoreStudyController
// .getStudy): the DETAILED study with its resource counts. On
// /api/legacy/studies/{studyId} (legacy StudyController.getStudy) it is
// written in the legacy model.
func (s Server) study(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("studyId")
	rows, err := core.CachedQueryP[studyRow](s.State, s.studySelect()+" WHERE c.study_id = {study:String}", ch.Params{"study": id})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		core.WriteError(w, http.StatusNotFound, core.MsgStudyNotFound(id))
		return
	}
	shape := studyShape{summary: true, detailed: true, columnStore: !core.IsLegacyAlias(r)}
	st := toCancerStudy(rows[0], shape, s.StudyImportDates())
	core.WriteJSON(w, s.withResourceCounts([]CancerStudy{st})[0])
}

// studiesFetch serves POST /api/studies/fetch (legacy StudyController
// .fetchStudies) in the ID, SUMMARY or DETAILED projection.
func (s Server) studiesFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	core.ReadBody(r, &ids)
	if len(ids) == 0 {
		core.WriteJSON(w, []CancerStudy{})
		return
	}
	rows, err := core.CachedQueryP[studyRow](s.State, s.studySelect()+" WHERE c.study_id IN {ids:Array(String)} ORDER BY c.study_id",
		ch.Params{"ids": ids})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	projection := ProjectionParam(r)
	shape := studyShape{summary: projection != "ID", detailed: projection == "DETAILED"}
	dates := s.StudyImportDates()
	out := make([]CancerStudy, len(rows))
	for i, r := range rows {
		out[i] = toCancerStudy(r, shape, dates)
	}
	if shape.summary {
		out = s.withResourceCounts(out)
	}
	core.WriteJSON(w, out)
}

// ---------- Samples ----------

// IDRow is a sample's study, sample and patient id.
type IDRow struct {
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

func (s Server) samples(w http.ResponseWriter, r *http.Request) {
	args := core.NewArgs()
	s.writeSamples(w, r, core.ByStudy(r.PathValue("studyId")), "1", args)
}

func (s Server) samplesFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers []core.Identifier `json:"sampleIdentifiers"`
		SampleListIDs     []string          `json:"sampleListIds"`
		UniqueSampleKeys  []string          `json:"uniqueSampleKeys"`
	}
	core.ReadBody(r, &f)
	// SampleFilter is EITHER explicit (study,sample) identifiers, OR a set of
	// sample-list ids (resolved via the sample_list view), OR base64 unique
	// sample keys. The results view uses the sampleListIds form to expand a case
	// set into its samples.
	var where string
	var sc core.Scope
	args := core.NewArgs()
	switch {
	case len(f.SampleIdentifiers) > 0:
		pairs := make([][2]string, len(f.SampleIdentifiers))
		for i, id := range f.SampleIdentifiers {
			pairs[i] = [2]string{id.StudyID, id.ID()}
		}
		sc, where = core.ByStudies(core.PairStudies(pairs)), "(s.study_id, s.sample_id) IN "+args.Pairs(pairs)
	case len(f.UniqueSampleKeys) > 0:
		var pairs [][2]string
		for _, k := range f.UniqueSampleKeys {
			if st, sid, ok := core.DecodeUniqueKey(k); ok {
				pairs = append(pairs, [2]string{st, sid})
			}
		}
		if len(pairs) == 0 {
			core.WriteJSON(w, []Sample{})
			return
		}
		sc, where = core.ByStudies(core.PairStudies(pairs)), "(s.study_id, s.sample_id) IN "+args.Pairs(pairs)
	case len(f.SampleListIDs) > 0:
		sc = s.BySampleLists(f.SampleListIDs...)
		where = "(s.study_id, s.sample_id) IN (SELECT study_id, sample_id FROM " + sc.From(args, core.TSampleList) + ")"
	default:
		core.WriteJSON(w, []Sample{})
		return
	}
	s.writeSamples(w, r, sc, where, args)
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
func (s Server) patientOrder(r *http.Request) string {
	if core.ProjectionOf(r) != "ID" && s.ImportOrdered() {
		return "internal_id"
	}
	return "patient_id, study_id"
}

// patientSortable is PatientSortBy's column (stableId).
var patientSortable = map[string]string{"patientId": "patient_id"}

func (s Server) patients(w http.ResponseWriter, r *http.Request) {
	p := ch.Params{}
	s.writePatientRows(w, r, "SELECT study_id AS studyId, patient_id AS patientId FROM "+core.ByStudy(r.PathValue("studyId")).From(core.ParamsBinder(p), core.TPatient)+
		core.SQLPageOrder(r, patientSortable, s.patientOrder(r)), p)
}

// writePatientRows streams the patients sql selects, or counts them for
// projection=META.
func (s Server) writePatientRows(w http.ResponseWriter, r *http.Request, sql string, p ch.Params) {
	if core.ProjectionOf(r) == "META" {
		core.WriteMetaCount(w, r, s.CH, "SELECT count() AS n FROM ("+sql+")", p)
		return
	}
	out := core.NewJSONArray(w, r)
	err := ch.QueryEachP(s.CH, sql, p, func(r *IDRow) error {
		return out.Add(Patient{PatientID: r.PatientID, StudyID: r.StudyID, UniquePatientKey: core.UniqueKey(r.StudyID, r.PatientID)})
	})
	out.Finish(err)
}

func (s Server) patientsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		PatientIdentifiers []core.Identifier `json:"patientIdentifiers"`
		UniquePatientKeys  []string          `json:"uniquePatientKeys"`
	}
	core.ReadBody(r, &f)
	// PatientFilter is EITHER explicit (studyId, patientId) identifiers OR a list
	// of base64 unique patient keys. The results view (coverage / plots) uses the
	// uniquePatientKeys form.
	var pairs [][2]string
	switch {
	case len(f.PatientIdentifiers) > 0:
		pairs = make([][2]string, len(f.PatientIdentifiers))
		for i, id := range f.PatientIdentifiers {
			pairs[i] = [2]string{id.StudyID, id.ID()}
		}
	case len(f.UniquePatientKeys) > 0:
		for _, k := range f.UniquePatientKeys {
			if st, pid, ok := core.DecodeUniqueKey(k); ok {
				pairs = append(pairs, [2]string{st, pid})
			}
		}
	}
	if len(pairs) == 0 {
		core.WriteJSON(w, []Patient{})
		return
	}
	p := ch.Params{"ids": pairs}
	rows, err := ch.QueryP[IDRow](s.CH,
		"SELECT study_id AS studyId, patient_id AS patientId FROM "+core.ByStudies(core.PairStudies(pairs)).From(core.ParamsBinder(p), core.TPatient)+
			" AND (study_id, patient_id) IN {ids:Array(Tuple(String, String))}"+core.SQLListOrder(r, patientSortable, s.patientOrder(r)), p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, toPatients(rows))
}

func toPatients(rows []IDRow) []Patient {
	out := make([]Patient, len(rows))
	for i, r := range rows {
		out[i] = Patient{PatientID: r.PatientID, StudyID: r.StudyID, UniquePatientKey: core.UniqueKey(r.StudyID, r.PatientID)}
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

func (s Server) cancerTypes(w http.ResponseWriter, r *http.Request) {
	out, err := s.QueryCancerTypes("", nil)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, out)
}

// withResourceCounts attaches each study's resource counts (resources shown in
// the study list's data-type filter).
func (s Server) withResourceCounts(studies []CancerStudy) []CancerStudy {
	if len(studies) == 0 {
		return studies
	}
	ids := make([]string, len(studies))
	for i, st := range studies {
		ids[i] = st.StudyID
	}
	p := ch.Params{"ids": ids}
	rows, err := core.CachedQueryP[ResourceCount](s.State,
		"SELECT rd.resource_id AS resourceId, any(rd.display_name) AS displayName, any(rd.description) AS description, "+
			"any(rd.resource_type) AS resourceType, toString(any(rd.priority)) AS priority, toBool(any(rd.open_by_default) = 1) AS openByDefault, "+
			"rd.study_id AS cancerStudyIdentifier, ifNull(any(rd.custom_metadata), '') AS customMetaData, "+
			"toInt64(uniqExact(x.sample_id)) AS sampleCount, toInt64(uniqExact(x.patient_id)) AS patientCount FROM ("+
			"SELECT study_id, resource_id, sample_id, patient_id FROM resource_sample "+
			"UNION ALL SELECT r.study_id, r.resource_id, s.sample_id, r.patient_id FROM resource_patient r "+
			"LEFT JOIN "+core.ByStudies(ids).Samples(core.ParamsBinder(p))+" s ON s.study_id = r.study_id AND s.patient_id = r.patient_id) x "+
			"JOIN resource_definition rd ON rd.study_id = x.study_id AND rd.resource_id = x.resource_id "+
			"WHERE x.study_id IN {ids:Array(String)} GROUP BY rd.study_id, rd.resource_id ORDER BY resourceId", p)
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
		} else if studies[i].legacy {
			rc = slices.Clone(rc)
			for j := range rc {
				rc[j].legacy = true
			}
		}
		studies[i].ResourceCounts = &rc
	}
	return studies
}

// studyReadable is a listed study's readPermission before the permission
// evaluator (studyAccess, with authorization on) applies, as
// ColumnStoreStudyController.getAllStudies sets it: readable unless, with
// study_availability.enabled, the study is being (re)imported (status other
// than 1). The study's public flag plays no part.
func studyReadable(availability bool, status int) bool {
	return !availability || status == 1
}
