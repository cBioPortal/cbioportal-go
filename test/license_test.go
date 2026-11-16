package contract

import (
	"encoding/json"
	"slices"
	"testing"
)

// A study's optional license (meta_study `license:`, cBioPortal/cbioportal
// #12399): ascn_test_study is imported with CC-BY-NC-ND-4.0
// (test/setup_test_db.sh), the other fixture studies without one.
const fixtureLicense = "CC-BY-NC-ND-4.0"

func TestContractStudyLicense(t *testing.T) {
	requireAPI(t)
	var one map[string]any
	get(t, "/api/studies/ascn_test_study", &one)
	wantEq(t, one["license"], fixtureLicense)

	// SUMMARY and DETAILED carry it; a study without one has no license key
	// (Jackson NON_NULL), and the ID projection carries neither.
	for _, c := range []struct{ method, url, body string }{
		{"GET", "/api/studies?projection=SUMMARY", ""},
		{"GET", "/api/studies?projection=DETAILED", ""},
		{"POST", "/api/studies/fetch?projection=SUMMARY", `["ascn_test_study","study_es_0"]`},
		{"POST", "/api/studies/fetch?projection=DETAILED", `["ascn_test_study","study_es_0"]`},
	} {
		var list []map[string]any
		var body any
		if c.body != "" {
			body = json.RawMessage(c.body)
		}
		do(t, c.method, c.url, body, false, &list)
		byID := map[string]map[string]any{}
		for _, s := range list {
			byID[s["studyId"].(string)] = s
		}
		if byID["ascn_test_study"] == nil || byID["study_es_0"] == nil {
			t.Fatalf("%s %s: lacks ascn_test_study or study_es_0", c.method, c.url)
		}
		if got := byID["ascn_test_study"]["license"]; got != fixtureLicense {
			t.Errorf("%s %s: ascn_test_study license = %v", c.method, c.url, got)
		}
		if _, ok := byID["study_es_0"]["license"]; ok {
			t.Errorf("%s %s: study_es_0 has a license key", c.method, c.url)
		}
	}
	var ids []map[string]any
	post(t, "/api/studies/fetch?projection=ID", []string{"ascn_test_study"}, &ids)
	if len(ids) != 1 || ids[0]["license"] != nil {
		t.Errorf("ID projection: %v", ids)
	}
}

// The license follows citation (here absent, so publicStudy / pmid) in the
// column-store DTO and the legacy CancerStudy alike.
func TestContractStudyLicenseFieldOrder(t *testing.T) {
	requireAPI(t)
	for _, c := range []struct{ method, url, body string }{
		{"GET", "/api/studies/ascn_test_study", ""},
		{"GET", "/api/studies?keyword=ascn_test_study&projection=SUMMARY", ""},
		{"POST", "/api/studies/fetch?projection=SUMMARY", `["ascn_test_study"]`},
		{"POST", "/api/studies/fetch?projection=DETAILED", `["ascn_test_study"]`},
	} {
		keys := fieldOrder(t, c.method, c.url, c.body)
		i := slices.Index(keys, "license")
		if i < 1 || keys[i-1] != "publicStudy" || keys[i+1] != "groups" {
			t.Errorf("%s %s: license misplaced: %v", c.method, c.url, keys)
		}
	}
}

// Study export writes the license back into meta_study.txt.
func TestContractExportStudyLicense(t *testing.T) {
	base := exportAPI(t)
	files, _ := exportZip(t, base, "ascn_test_study")
	wantEq(t, files["meta_study.txt"], "cancer_study_identifier: ascn_test_study\n"+
		"type_of_cancer: mel\nname: ASCN Study\ndescription: There is ASCN data in here.\n"+
		"license: CC-BY-NC-ND-4.0\ngroups: PUBLIC\nreference_genome: hg19\n")
}
