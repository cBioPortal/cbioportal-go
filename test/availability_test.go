package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// Study availability (cBioPortal/cbioportal#12349): study_hg38 has
// cancer_study.status 0 in the test database. The study list marks it
// unreadable whatever the settings; with study_availability.enabled requests
// naming it, its profiles, sample lists or unique keys get 423.
func TestContractStudyAvailability(t *testing.T) {
	requireAPI(t)
	var studies []map[string]any
	get(t, "/api/studies?projection=SUMMARY", &studies)
	byID := map[string]map[string]any{}
	for _, s := range studies {
		byID[s["studyId"].(string)] = s
	}
	if byID["study_hg38"] == nil || byID["study_es_0"] == nil {
		t.Fatalf("study list lacks study_hg38 or study_es_0")
	}
	wantEq(t, []any{byID["study_hg38"]["status"], byID["study_hg38"]["readPermission"]}, []any{float64(0), false})
	wantEq(t, []any{byID["study_es_0"]["status"], byID["study_es_0"]["readPermission"]}, []any{float64(1), true})

	var cfg map[string]any
	get(t, "/config_service", &cfg)
	enabled := cfg["study_availability_enabled"] == true

	call := func(method, path string, body any) (int, string) {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, apiBase()+path, r)
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		var e struct {
			Message string `json:"message"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return resp.StatusCode, e.Message
	}
	locked := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/studies/study_hg38", nil},
		{"GET", "/api/molecular-profiles/study_hg38_mutations", nil},
		{"GET", "/api/sample-lists/study_hg38_all", nil},
		{"GET", "/api/molecular-profiles?studyIds=study_es_0,study_hg38", nil},
		{"POST", "/api/mutations/fetch", map[string]any{"molecularProfileIds": []string{"study_hg38_mutations"}, "entrezGeneIds": []int{7157}}},
		{"POST", "/api/resource-definitions/fetch", []string{"study_es_0", "study_hg38"}},
		// base64("TCGA-A2-A04P:study_hg38") without padding
		{"POST", "/api/patients/fetch?projection=SUMMARY", map[string]any{"uniquePatientKeys": []string{"VENHQS1BMi1BMDRQOnN0dWR5X2hnMzg"}}},
	}
	for _, c := range locked {
		status, msg := call(c.method, c.path, c.body)
		if enabled {
			wantEq(t, []any{c.path, status, msg}, []any{c.path, 423, "Study study_hg38 is being updated. Please check back later."})
		} else if status == 423 {
			t.Errorf("%s %s: 423 with study_availability.enabled off", c.method, c.path)
		}
	}
	for _, path := range []string{"/api/studies/study_es_0", "/api/studies?keyword=study_hg38&projection=SUMMARY"} {
		if status, _ := call("GET", path, nil); status != 200 {
			t.Errorf("GET %s -> %d", path, status)
		}
	}
}
