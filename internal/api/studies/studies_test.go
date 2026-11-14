package studies

import (
	"strings"
	"testing"
)

func TestStudyReadable(t *testing.T) {
	for _, c := range []struct {
		availability bool
		status       int
		want         bool
	}{
		{false, 0, true}, {false, 1, true}, {true, 1, true}, {true, 0, false},
	} {
		if got := studyReadable(c.availability, c.status); got != c.want {
			t.Errorf("studyReadable(%v, %d) = %v, want %v", c.availability, c.status, got, c.want)
		}
	}
}

// Every study endpoint counts treated patients over both spellings of the
// event type, as the column-store CancerStudyMapper does; Java's legacy
// StudyMapper counts only 'Treatment' (docs/rewrite/java-quirks.md).
func TestTreatmentCountBothSpellings(t *testing.T) {
	if !strings.Contains(studyCountJoins, "event_type IN ('Treatment', 'TREATMENT')") {
		t.Error("studyCountJoins does not count both Treatment and TREATMENT events")
	}
}

// A cancer type's primary site is its ancestor right below "tissue"
// (CancerTypeServiceImpl.getParent).
func TestPrimarySite(t *testing.T) {
	byID := map[string]CancerType{}
	for _, c := range []CancerType{
		{CancerTypeID: "tissue", Parent: ""},
		{CancerTypeID: "breast", Name: "Breast", Parent: "tissue"},
		{CancerTypeID: "brca", Parent: "breast"},
		{CancerTypeID: "idc", Parent: "brca"},
		{CancerTypeID: "orphan", Parent: "missing"},
		{CancerTypeID: "loop", Parent: "loop"},
	} {
		byID[c.CancerTypeID] = c
	}
	for id, want := range map[string]string{"breast": "breast", "brca": "breast", "idc": "breast", "orphan": "", "loop": ""} {
		site, ok := primarySite(byID, byID[id])
		if ok != (want != "") || site.CancerTypeID != want {
			t.Errorf("%s: %q %v, want %q", id, site.CancerTypeID, ok, want)
		}
	}
}

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
