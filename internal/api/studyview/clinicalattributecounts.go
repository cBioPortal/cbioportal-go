package studyview

import (
	"fmt"
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// ---------- Clinical attribute counts ----------

type ClinicalAttributeCount struct {
	ClinicalAttributeID string `json:"clinicalAttributeId"`
	Count               uint64 `json:"count"`
}

// ClinicalAttributeCountsFetch serves POST /api/clinical-attributes/counts/fetch.
func (s Server) ClinicalAttributeCountsFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		SampleIdentifiers []core.Identifier `json:"sampleIdentifiers"`
		SampleListID      string            `json:"sampleListId"`
		StudyViewFilter   studyViewFilter   `json:"studyViewFilter"`
		StudyIDs          []string          `json:"studyIds"`
	}
	core.ReadBody(r, &f)

	scopeFilter := studyViewFilter{}
	args := core.NewArgs()
	// sc holds the clinical rows to the selected samples' studies, a key
	// range; sampleScope selects the samples over (study_id, sample_id) and
	// samples is their sample rows.
	var sc core.Scope
	var sampleScope, samples string
	switch {
	case f.SampleListID != "":
		sc = s.BySampleList(f.SampleListID)
		inList := "(study_id, sample_id) IN (SELECT study_id, sample_id FROM " + sc.From(args, core.TSampleList) + ")"
		sampleScope = sc.Key(args, core.TClinicalSample, "") + inList
		samples = sc.From(args, core.TSample) + " AND " + inList
	case len(f.SampleIdentifiers) > 0:
		scopeFilter.SampleIdentifiers = f.SampleIdentifiers
	case len(f.StudyViewFilter.SampleIdentifiers) > 0 || len(f.StudyViewFilter.StudyIDs) > 0:
		scopeFilter = f.StudyViewFilter
	case len(f.StudyIDs) > 0:
		scopeFilter.StudyIDs = f.StudyIDs
	default:
		core.WriteJSON(w, []ClinicalAttributeCount{})
		return
	}
	if sampleScope == "" {
		sc = scopeFilter.scope()
		sampleScope = s.sampleScope(args, scopeFilter)
		samples = s.sampleScopeFrom(args, scopeFilter, core.TSample)
	}
	type countRow struct {
		AttrID string `json:"attr_id"`
		C      uint64 `json:"c"`
	}
	out := []ClinicalAttributeCount{}
	// ClinicalAttributeMapper counts rows: a sample attribute once per sample,
	// a patient attribute once per selected sample of the patient (the
	// patient row joined to its samples). Sample counts come first.
	queries := []string{
		fmt.Sprintf("SELECT attr_id, count() AS c FROM clinical_sample WHERE %s GROUP BY attr_id ORDER BY attr_id", sampleScope),
		fmt.Sprintf("SELECT cp.attr_id AS attr_id, count() AS c FROM (SELECT * FROM clinical_patient WHERE %s1) cp "+
			"JOIN (SELECT study_id, patient_id FROM %s) sm ON sm.study_id = cp.study_id AND sm.patient_id = cp.patient_id "+
			"GROUP BY cp.attr_id ORDER BY attr_id", sc.Key(args, core.TClinicalPatient, ""), samples),
	}
	for _, q := range queries {
		rows, err := core.QueryA[countRow](s.CH, q, args)
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		for _, r := range rows {
			out = append(out, ClinicalAttributeCount{ClinicalAttributeID: r.AttrID, Count: r.C})
		}
	}
	core.WriteJSON(w, out)
}
