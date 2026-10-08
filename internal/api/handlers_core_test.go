package api

import (
	"strings"
	"testing"
)

// A study's license follows citation in both study models (column-store
// CancerStudyMetadataDTO and legacy CancerStudy) and, as a null under
// Jackson NON_NULL, is left out when the study has none.
func TestCancerStudyLicense(t *testing.T) {
	row := studyRow{StudyID: "s1", CancerTypeID: "brca", Name: "S1", Citation: "Cell 2018", License: "CC-BY-NC-ND-4.0"}
	for _, shape := range []studyShape{
		{summary: true}, {summary: true, detailed: true}, {summary: true, columnStore: true},
		{summary: true, detailed: true, columnStore: true},
	} {
		b, err := toCancerStudy(row, shape, nil).MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"citation":"Cell 2018","license":"CC-BY-NC-ND-4.0","groups":`) {
			t.Errorf("%+v: license not after citation: %s", shape, b)
		}
		noLicense := row
		noLicense.License = ""
		b, _ = toCancerStudy(noLicense, shape, nil).MarshalJSON()
		if strings.Contains(string(b), `"license"`) {
			t.Errorf("%+v: unset license written: %s", shape, b)
		}
	}
	b, _ := toCancerStudy(row, studyShape{}, nil).MarshalJSON()
	if strings.Contains(string(b), `"license"`) {
		t.Errorf("ID projection carries the license: %s", b)
	}
}
