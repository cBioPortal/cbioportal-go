package api

import (
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// ---------- Studies ----------

type studyRow struct {
	StudyID         string `json:"studyId"`
	CancerTypeID    string `json:"cancerTypeId"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Pmid            string `json:"pmid"`
	Citation        string `json:"citation"`
	Groups          string `json:"groups"`
	ReferenceGenome string `json:"referenceGenome"`
	AllSampleCount   int64  `json:"allSampleCount"`
	PublicFlag       int    `json:"publicFlag"`
	CancerTypeName   string `json:"cancerTypeName"`
	CancerTypeColor  string `json:"cancerTypeColor"`
	CancerTypeParent string `json:"cancerTypeParent"`
}

type CancerStudy struct {
	StudyID         string `json:"studyId"`
	CancerTypeID    string `json:"cancerTypeId"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	PublicStudy     bool   `json:"publicStudy"`
	Pmid            string `json:"pmid"`
	Citation        string `json:"citation"`
	Groups          string `json:"groups"`
	Status          int    `json:"status"`
	AllSampleCount  int64       `json:"allSampleCount"`
	ReferenceGenome string      `json:"referenceGenome"`
	ReadPermission  bool        `json:"readPermission"`
	CancerType      *CancerType `json:"cancerType"`
}

const studySelect = "SELECT c.study_id AS studyId, c.cancer_type_id AS cancerTypeId, " +
	"c.name AS name, c.description AS description, c.pmid AS pmid, c.citation AS citation, " +
	"c.groups AS groups, c.reference_genome AS referenceGenome, " +
	"toInt64(ifNull(sc.cnt, 0)) AS allSampleCount, " +
	"toUInt8(position(c.groups, 'PUBLIC') > 0) AS publicFlag, " +
	"ifNull(t.name, '') AS cancerTypeName, ifNull(t.dedicated_color, '') AS cancerTypeColor, ifNull(t.parent, '') AS cancerTypeParent " +
	"FROM cancer_study c LEFT JOIN (SELECT study_id, count() AS cnt FROM sample GROUP BY study_id) sc " +
	"ON c.study_id = sc.study_id " +
	"LEFT JOIN cbioportal_ref.type_of_cancer t ON c.cancer_type_id = t.type_of_cancer_id"

func toCancerStudy(r studyRow) CancerStudy {
	return CancerStudy{
		StudyID: r.StudyID, CancerTypeID: r.CancerTypeID, Name: r.Name, Description: r.Description,
		PublicStudy: r.PublicFlag != 0, Pmid: r.Pmid, Citation: r.Citation, Groups: r.Groups,
		AllSampleCount: r.AllSampleCount, ReferenceGenome: r.ReferenceGenome, ReadPermission: true,
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

func (s *State) studies(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.Query[studyRow](s.CH, studySelect+" ORDER BY c.study_id")
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]CancerStudy, len(rows))
	for i, r := range rows {
		out[i] = toCancerStudy(r)
	}
	writeJSON(w, out)
}

func (s *State) study(w http.ResponseWriter, r *http.Request) {
	sql := fmt.Sprintf("%s WHERE c.study_id = '%s'", studySelect, sqlLit(r.PathValue("studyId")))
	rows, err := ch.Query[studyRow](s.CH, sql)
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "study not found", http.StatusNotFound)
		return
	}
	writeJSON(w, toCancerStudy(rows[0]))
}

func (s *State) studiesFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	if len(ids) == 0 {
		writeJSON(w, []CancerStudy{})
		return
	}
	sql := fmt.Sprintf("%s WHERE c.study_id IN (%s) ORDER BY c.study_id", studySelect, inList(ids))
	rows, err := ch.Query[studyRow](s.CH, sql)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]CancerStudy, len(rows))
	for i, r := range rows {
		out[i] = toCancerStudy(r)
	}
	writeJSON(w, out)
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

func (s *State) idSet(sql string) map[string]bool {
	rows, _ := ch.Query[idRow](s.CH, sql)
	set := map[string]bool{}
	for _, r := range rows {
		set[r.SampleID] = true
	}
	return set
}

func (s *State) samples(w http.ResponseWriter, r *http.Request) {
	sid := sqlLit(r.PathValue("studyId"))
	rows, err := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM sample WHERE study_id = '%s' ORDER BY sample_id", sid))
	if err != nil {
		fail(w, err)
		return
	}
	seg := s.idSet(fmt.Sprintf("SELECT DISTINCT sample_id AS sampleId FROM seg WHERE study_id = '%s'", sid))
	seq := s.idSet(fmt.Sprintf("SELECT DISTINCT sample_id AS sampleId FROM mutation WHERE study_id = '%s'", sid))
	type tr struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	typeRows, _ := ch.Query[tr](s.CH, fmt.Sprintf(
		"SELECT sample_id AS id, attr_value AS value FROM clinical_sample WHERE study_id = '%s' AND attr_id = 'SAMPLE_TYPE'", sid))
	sampleTypes := map[string]string{}
	for _, t := range typeRows {
		sampleTypes[t.ID] = t.Value
	}
	out := make([]Sample, len(rows))
	for i, r := range rows {
		out[i] = Sample{
			SampleID: r.SampleID, SampleType: sampleTypes[r.SampleID], PatientID: r.PatientID, StudyID: r.StudyID,
			Sequenced: seq[r.SampleID], CopyNumberSegmentPresent: seg[r.SampleID],
			UniqueSampleKey: uniqueKey(r.StudyID, r.SampleID), UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
		}
	}
	writeJSON(w, out)
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
	}
	readBody(r, &f)
	// SampleFilter is EITHER explicit (study,sample) identifiers OR a set of
	// sample-list ids (resolved via the sample_list view). The results view uses
	// the sampleListIds form to expand a case set into its samples.
	var where string
	switch {
	case len(f.SampleIdentifiers) > 0:
		pairs := make([][2]string, len(f.SampleIdentifiers))
		for i, id := range f.SampleIdentifiers {
			pairs[i] = [2]string{id.StudyID, id.id()}
		}
		where = fmt.Sprintf("(study_id, sample_id) IN (%s)", tupleIn(pairs))
	case len(f.SampleListIDs) > 0:
		where = fmt.Sprintf(
			"(study_id, sample_id) IN (SELECT study_id, sample_id FROM sample_list WHERE list_id IN (%s))",
			inList(f.SampleListIDs))
	default:
		writeJSON(w, []Sample{})
		return
	}
	rows, err := ch.Query[idRow](s.CH, fmt.Sprintf(
		"SELECT study_id AS studyId, sample_id AS sampleId, patient_id AS patientId FROM sample WHERE %s ORDER BY study_id, sample_id", where))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]Sample, len(rows))
	for i, r := range rows {
		out[i] = Sample{
			SampleID: r.SampleID, PatientID: r.PatientID, StudyID: r.StudyID,
			UniqueSampleKey: uniqueKey(r.StudyID, r.SampleID), UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
		}
	}
	writeJSON(w, out)
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

type cancerTypeRow struct {
	TypeOfCancerID string `json:"typeOfCancerId"`
	Name           string `json:"name"`
	DedicatedColor string `json:"dedicatedColor"`
	Parent         string `json:"parent"`
}

type CancerType struct {
	CancerTypeID   string `json:"cancerTypeId"`
	Name           string `json:"name"`
	DedicatedColor string `json:"dedicatedColor"`
	ShortName      string `json:"shortName"`
	Parent         string `json:"parent"`
}

func (s *State) cancerTypes(w http.ResponseWriter, r *http.Request) {
	rows, _ := ch.Query[cancerTypeRow](s.CH,
		"SELECT type_of_cancer_id AS typeOfCancerId, name, dedicated_color AS dedicatedColor, parent FROM cbioportal_ref.type_of_cancer "+
			"UNION DISTINCT SELECT type_of_cancer_id AS typeOfCancerId, name, dedicated_color AS dedicatedColor, parent FROM type_of_cancer "+
			"ORDER BY typeOfCancerId")
	out := make([]CancerType, len(rows))
	for i, r := range rows {
		out[i] = CancerType{CancerTypeID: r.TypeOfCancerID, Name: r.Name, DedicatedColor: r.DedicatedColor, ShortName: r.TypeOfCancerID, Parent: r.Parent}
	}
	writeJSON(w, out)
}

var _ = strings.TrimSpace
