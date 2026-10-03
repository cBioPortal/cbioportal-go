package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestNamesUnavailable(t *testing.T) {
	unavailable := map[string]string{"locked": "locked", "locked_mutations": "locked", "locked_all": "locked"}
	cases := []struct {
		method, target, body, want string
	}{
		{"GET", "/api/studies/locked", "", "locked"},
		{"GET", "/api/molecular-profiles/locked_mutations", "", "locked"},
		{"GET", "/api/sample-lists/locked_all/sample-ids", "", "locked"},
		{"GET", "/export/study/locked.zip", "", "locked"},
		{"GET", "/api/molecular-profiles?studyIds=open,locked", "", "locked"},
		{"GET", "/api/studies/open", "", ""},
		// The study list keyword is a search, not an identifier.
		{"GET", "/api/studies?keyword=locked", "", ""},
		{"GET", "/api/studies/meta?keyword=locked", "", ""},
		{"GET", "/api/genes?keyword=locked", "", "locked"},
		{"POST", "/api/mutations/fetch", `{"molecularProfileIds":["locked_mutations"],"entrezGeneIds":[7157]}`, "locked"},
		{"POST", "/api/resource-definitions/fetch", `["open","locked"]`, "locked"},
		{"POST", "/api/column-store/clinical-data-counts/fetch", `{"studyViewFilter":{"studyIds":["open"],"sampleIdentifiers":[{"studyId":"locked","sampleId":"S1"}]}}`, "locked"},
		// Unique keys, base64 of "<id>:<studyId>" with or without padding.
		{"POST", "/api/samples/fetch", `{"uniqueSampleKeys":["UzE6bG9ja2Vk"]}`, "locked"},
		{"POST", "/api/samples/fetch", `{"uniqueSampleKeys":["UzE6b3Blbg"]}`, ""},
		{"POST", "/api/patients/fetch", `{"uniquePatientKeys":["UDE6bG9ja2Vk"]}`, "locked"},
		{"POST", "/api/mutations/fetch", `{"molecularProfileIds":["open_mutations"],"entrezGeneIds":[7157]}`, ""},
		{"POST", "/api/mutations/fetch", `not json`, ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
		if got := requestNamesUnavailable(r, unavailable); got != c.want {
			t.Errorf("%s %s %s = %q, want %q", c.method, c.target, c.body, got, c.want)
		}
	}
}

func TestMatchUnavailableKeys(t *testing.T) {
	unavailable := map[string]string{"locked": "locked"}
	for key, want := range map[string]string{
		"UzE6bG9ja2Vk":     "locked", // S1:locked
		"UzE6bG9ja2Vk==":   "",       // over-padded is not base64
		"UzE6bG9ja2Vk=":    "",       // padding where none belongs
		"UzE6bG9ja2VkOg==": "locked", // S1:locked: (trailing empty part dropped)
		"UzE6bG9ja2VkOg":   "locked",
		"UzE6YTpsb2NrZWQ":  "", // S1:a:locked
		"bG9ja2Vk":         "", // locked (no delimiter)
		"not base64!":      "",
		"":                 "",
		"locked":           "locked",
	} {
		if got := matchUnavailable(key, unavailable); got != want {
			t.Errorf("matchUnavailable(%q) = %q, want %q", key, got, want)
		}
	}
}
