package studies

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// ClinicalRow is a clinical data value as read from ClickHouse.
type ClinicalRow struct {
	StudyID             string `json:"studyId"`
	ID                  string `json:"id"`
	PatientID           string `json:"patientId"`
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	Value               string `json:"value"`
}

type ClinicalData struct {
	ClinicalAttributeID string             `json:"clinicalAttributeId"`
	Value               string             `json:"value"`
	SampleID            string             `json:"sampleId,omitempty"`
	PatientID           string             `json:"patientId"`
	StudyID             string             `json:"studyId"`
	UniqueSampleKey     string             `json:"uniqueSampleKey,omitempty"`
	UniquePatientKey    string             `json:"uniquePatientKey"`
	PatientAttribute    bool               `json:"patientAttribute"`
	ClinicalAttribute   *ClinicalAttribute `json:"clinicalAttribute,omitempty"`
}

func isPatient(r *http.Request) bool {
	return strings.EqualFold(r.URL.Query().Get("clinicalDataType"), "PATIENT")
}

// toClinicalData maps a clinical row to its record.
func toClinicalData(r *ClinicalRow, patient bool) ClinicalData {
	d := ClinicalData{
		ClinicalAttributeID: r.ClinicalAttributeID, Value: r.Value, StudyID: r.StudyID, PatientAttribute: patient,
	}
	if patient {
		d.PatientID = r.ID
	} else {
		d.SampleID, d.PatientID = r.ID, r.PatientID
		d.UniqueSampleKey = core.UniqueKey(r.StudyID, r.ID)
	}
	d.UniquePatientKey = core.UniqueKey(r.StudyID, d.PatientID)
	return d
}

// clinicalSQL selects clinical rows (sample- or patient-level) matching where,
// which may reference study_id, the entity id column (sample_id / patient_id)
// and attr_id, and holds the rows to studies. Sample rows carry their
// patient id, read from those studies' samples. Rows are in
// ClinicalDataMapper's order where the database carries internal ids: by the
// internal id of their sample or patient, then attribute (the storage order
// of clinical_sample / clinical_patient); a page, sorted by
// ClinicalDataSortBy or not, is read as sqlListOrder orders it.
func clinicalSQL(r *http.Request, b core.Binder, patient, importOrder bool, where string, studies []string) string {
	def := "studyId, id, clinicalAttributeId"
	if importOrder {
		def = "c.internal_id, studyId, id, clinicalAttributeId"
	}
	order := core.SQLListOrder(r, clinicalDataSortable, def)
	if patient {
		return "SELECT c.study_id AS studyId, c.patient_id AS id, c.patient_id AS patientId, c.attr_id AS clinicalAttributeId, " +
			"c.attr_value AS value FROM clinical_patient c WHERE " + where + order
	}
	return "SELECT c.study_id AS studyId, c.sample_id AS id, s.patient_id AS patientId, c.attr_id AS clinicalAttributeId, " +
		"c.attr_value AS value FROM (SELECT * FROM clinical_sample WHERE " + where + ") c " +
		"LEFT JOIN " + core.ByStudies(studies).Samples(b) + " s ON s.study_id = c.study_id AND s.sample_id = c.sample_id" + order
}

// clinicalDataSortable are the ClinicalDataSortBy columns (attrId,
// attrValue) as clinicalSQL names them.
var clinicalDataSortable = map[string]string{"clinicalAttributeId": "clinicalAttributeId", "value": "value"}

// clinicalDataOut is a legacy ClinicalData record as Jackson writes it
// (NON_NULL, see ClinicalDataMapper.selectSample / selectPatient): ID carries
// the identifying fields only, SUMMARY adds the value and DETAILED the
// attribute metadata, with patientAttribute derived from it.
type clinicalDataOut struct {
	UniqueSampleKey     string             `json:"uniqueSampleKey,omitempty"`
	UniquePatientKey    string             `json:"uniquePatientKey"`
	SampleID            string             `json:"sampleId,omitempty"`
	PatientID           string             `json:"patientId"`
	StudyID             string             `json:"studyId"`
	ClinicalAttribute   *ClinicalAttribute `json:"clinicalAttribute,omitempty"`
	PatientAttribute    *bool              `json:"patientAttribute,omitempty"`
	ClinicalAttributeID string             `json:"clinicalAttributeId"`
	Value               *string            `json:"value,omitempty"`
}

// writeClinical streams the query's rows in the requested projection (ID,
// SUMMARY, DETAILED with the attribute metadata of studies, or META as a
// count header). where holds the rows to studies and may hold placeholders
// bound in args.
func (s Server) writeClinical(w http.ResponseWriter, r *http.Request, patient bool, studies []string, where string, args *core.SQLArgs) {
	projection := ProjectionParam(r)
	if projection == "META" {
		type c struct {
			N uint64 `json:"n"`
		}
		view := "clinical_sample"
		if patient {
			view = "clinical_patient"
		}
		rows, _ := core.QueryA[c](s.CH, fmt.Sprintf("SELECT count() AS n FROM %s WHERE %s", view, where), args)
		var n uint64
		if len(rows) > 0 {
			n = rows[0].N
		}
		w.Header().Set("total-count", fmt.Sprint(n))
		w.WriteHeader(http.StatusOK)
		return
	}
	var attrs map[string]ClinicalAttribute
	if projection == "DETAILED" {
		attrs = s.clinicalAttributes(studies)
	}
	out := core.NewJSONArray(w, r)
	sql := clinicalSQL(r, args, patient, s.ImportOrdered(), where, studies)
	err := ch.QueryEachP(s.CH, sql, args.Params(sql), func(row *ClinicalRow) error {
		d := toClinicalData(row, patient)
		o := clinicalDataOut{
			UniqueSampleKey: d.UniqueSampleKey, UniquePatientKey: d.UniquePatientKey, SampleID: d.SampleID,
			PatientID: d.PatientID, StudyID: d.StudyID, ClinicalAttributeID: d.ClinicalAttributeID,
		}
		if projection != "ID" {
			o.Value = &d.Value
		}
		if a, ok := attrs[d.StudyID+"\x00"+d.ClinicalAttributeID]; ok {
			o.ClinicalAttribute = &a
			o.PatientAttribute = &a.PatientAttribute
		}
		return out.Add(o)
	})
	out.Finish(err)
}

func (s Server) clinicalData(w http.ResponseWriter, r *http.Request) {
	args := core.NewArgs()
	where := "study_id = " + args.Str(r.PathValue("studyId"))
	if a := r.URL.Query().Get("attributeId"); a != "" {
		where += " AND attr_id = " + args.Str(a)
	}
	s.writeClinical(w, r, isPatient(r), []string{r.PathValue("studyId")}, where, args)
}

func (s Server) clinicalDataStudyFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		IDs          []string `json:"ids"`
		AttributeIDs []string `json:"attributeIds"`
	}
	core.ReadBody(r, &f)
	patient := isPatient(r)
	idCol := "sample_id"
	if patient {
		idCol = "patient_id"
	}
	args := core.NewArgs()
	where := "study_id = " + args.Str(r.PathValue("studyId"))
	if len(f.IDs) > 0 {
		where += fmt.Sprintf(" AND %s IN %s", idCol, args.Strs(f.IDs))
	}
	if len(f.AttributeIDs) > 0 {
		where += " AND attr_id IN " + args.Strs(f.AttributeIDs)
	}
	s.writeClinical(w, r, patient, []string{r.PathValue("studyId")}, where, args)
}

// clinicalDataFetch serves POST /api/clinical-data/fetch: clinical data for
// (studyId, entityId) identifiers across studies, optionally limited to
// attributeIds.
func (s Server) clinicalDataFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Identifiers []struct {
			StudyID  string `json:"studyId"`
			EntityID string `json:"entityId"`
		} `json:"identifiers"`
		AttributeIDs []string `json:"attributeIds"`
	}
	core.ReadBody(r, &f)
	if len(f.Identifiers) == 0 {
		core.WriteJSON(w, []ClinicalData{})
		return
	}
	patient := isPatient(r)
	idCol := "sample_id"
	if patient {
		idCol = "patient_id"
	}
	pairs := make([][2]string, len(f.Identifiers))
	studies := map[string]bool{}
	for i, id := range f.Identifiers {
		pairs[i] = [2]string{id.StudyID, id.EntityID}
		studies[id.StudyID] = true
	}
	args := core.NewArgs()
	where := fmt.Sprintf("(study_id, %s) IN %s", idCol, args.Pairs(pairs))
	if len(f.AttributeIDs) > 0 {
		where += " AND attr_id IN " + args.Strs(f.AttributeIDs)
	}
	s.writeClinical(w, r, patient, core.SortedKeys(studies), where, args)
}

// clinicalAttributes indexes the studies' attribute metadata by study and id.
func (s Server) clinicalAttributes(studyIDs []string) map[string]ClinicalAttribute {
	out := map[string]ClinicalAttribute{}
	rows, _ := ch.QueryP[ClinAttrRow](s.CH,
		"SELECT study_id AS studyId, clinical_attribute_id AS clinicalAttributeId, display_name AS displayName, "+
			"description, datatype, priority, patient_attribute AS patientFlag FROM clinical_attribute_meta WHERE study_id IN {studies:Array(String)} "+
			"ORDER BY clinical_attribute_id, study_id",
		ch.Params{"studies": studyIDs})
	for _, r := range rows {
		out[r.StudyID+"\x00"+r.ClinicalAttributeID] = r.toAttribute()
	}
	return out
}

// ClinAttrRow is a clinical attribute as read from ClickHouse.
type ClinAttrRow struct {
	StudyID             string `json:"studyId"`
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	DisplayName         string `json:"displayName"`
	Description         string `json:"description"`
	Datatype            string `json:"datatype"`
	Priority            string `json:"priority"`
	PatientFlag         int    `json:"patientFlag"`
}

type ClinicalAttribute struct {
	DisplayName         string `json:"displayName"`
	Description         string `json:"description"`
	Datatype            string `json:"datatype"`
	PatientAttribute    bool   `json:"patientAttribute"`
	Priority            string `json:"priority"`
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	StudyID             string `json:"studyId"`
}

// clinicalAttributesFetch serves POST /api/clinical-attributes/fetch, ordered
// as ClinicalAttributeMapper does: by attribute id, then internal study id.
func (s Server) clinicalAttributesFetch(w http.ResponseWriter, r *http.Request) {
	var studyIDs []string
	core.ReadBody(r, &studyIDs)
	if len(studyIDs) == 0 {
		core.WriteJSON(w, []ClinicalAttribute{})
		return
	}
	rows, err := ch.QueryP[ClinAttrRow](s.CH,
		"SELECT study_id AS studyId, clinical_attribute_id AS clinicalAttributeId, display_name AS displayName, "+
			"description, datatype, priority, patient_attribute AS patientFlag FROM clinical_attribute_meta WHERE study_id IN {studies:Array(String)} "+
			"ORDER BY clinical_attribute_id, "+s.StudyOrder("study_id"),
		ch.Params{"studies": studyIDs})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]ClinicalAttribute, len(rows))
	for i, r := range rows {
		out[i] = r.toAttribute()
	}
	core.WriteJSON(w, out)
}

func (r ClinAttrRow) toAttribute() ClinicalAttribute {
	dt := r.Datatype
	if dt == "" {
		dt = "STRING"
	}
	pr := r.Priority
	if pr == "" {
		pr = "1"
	}
	return ClinicalAttribute{
		ClinicalAttributeID: r.ClinicalAttributeID, DisplayName: r.DisplayName, Description: r.Description,
		Datatype: dt, PatientAttribute: r.PatientFlag != 0, Priority: pr, StudyID: r.StudyID,
	}
}
