package main

import (
	"encoding/json"
	"slices"
	"strings"
)

// mixedLevels caches, per set of studies, the clinical attributes they
// define at different levels: patient-level in some, sample-level in
// others. Java counts such an attribute over every sample of the selected
// patients, also samples outside the selection (docs/rewrite/java-quirks.md).
type mixedLevels map[string]map[string]bool

// of returns the mixed-level attributes of the studies a study view request
// body selects (studyViewFilter's studyIds and sampleIdentifiers), asking
// origin A's clinical-attributes endpoint; nil when there are none or the
// body names no studies.
func (m mixedLevels) of(fa *fetcher, body string) map[string]bool {
	var b struct {
		StudyViewFilter struct {
			StudyIDs          []string `json:"studyIds"`
			SampleIdentifiers []struct {
				StudyID string `json:"studyId"`
			} `json:"sampleIdentifiers"`
		} `json:"studyViewFilter"`
	}
	if json.Unmarshal([]byte(body), &b) != nil {
		return nil
	}
	studies := slices.Clone(b.StudyViewFilter.StudyIDs)
	for _, s := range b.StudyViewFilter.SampleIdentifiers {
		studies = append(studies, s.StudyID)
	}
	slices.Sort(studies)
	studies = slices.Compact(studies)
	if len(studies) < 2 {
		return nil
	}
	key := strings.Join(studies, ",")
	if got, ok := m[key]; ok {
		return got
	}
	req, _ := json.Marshal(studies)
	r, err := fa.get(request{Method: "POST", Path: "/api/clinical-attributes/fetch?projection=SUMMARY", Body: string(req)})
	var attrs []struct {
		ID      string `json:"clinicalAttributeId"`
		Patient bool   `json:"patientAttribute"`
	}
	if err == nil && r.Status == 200 {
		if raw, err := r.bytes(); err == nil {
			json.Unmarshal(raw, &attrs)
		}
		r.release()
	}
	levels := map[string][2]bool{}
	for _, a := range attrs {
		l := levels[a.ID]
		if a.Patient {
			l[0] = true
		} else {
			l[1] = true
		}
		levels[a.ID] = l
	}
	var out map[string]bool
	for id, l := range levels {
		if l[0] && l[1] {
			if out == nil {
				out = map[string]bool{}
			}
			out[id] = true
		}
	}
	m[key] = out
	return out
}

// onlyAttributesDiffer reports whether two arrays of per-attribute
// elements (attributeId) are the same apart from the elements of attrs.
func onlyAttributesDiffer(ra, rb response, attrs map[string]bool, rq request, o compareOptions) bool {
	ba, errA := ra.bytes()
	bb, errB := rb.bytes()
	if errA != nil || errB != nil {
		return false
	}
	ja, errA := decodeJSON(ba)
	jb, errB := decodeJSON(bb)
	if errA != nil || errB != nil {
		return false
	}
	keep := func(v any) ([]any, bool) {
		arr, ok := v.([]any)
		if !ok {
			return nil, false
		}
		var out []any
		for _, e := range arr {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, false
			}
			if id, _ := m["attributeId"].(string); !attrs[id] {
				out = append(out, e)
			}
		}
		return out, true
	}
	fa, okA := keep(ja)
	fb, okB := keep(jb)
	o.Unordered = o.Unordered || rq.Opts.Unordered
	return okA && okB && compareJSON(fa, fb, o).Count == 0
}
