package studyview

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

func TestWithStudies(t *testing.T) {
	studies := func() ([]string, error) { return []string{"a", "b"}, nil }
	clinical := `"clinicalDataFilters":[{"attributeId":"ONCOTREE_CODE","values":[{"value":"OS"}]}]`
	cases := []struct {
		name, body string
		want       []string // the filter's studyIds after; nil: body unchanged
		nested     bool
	}{
		{"no studies, a clinical filter", `{` + clinical + `}`, []string{"a", "b"}, false},
		{"empty lists, a clinical filter", `{"studyIds":[],"sampleIdentifiers":[],` + clinical + `}`, []string{"a", "b"}, false},
		{"nested filter", `{"attributes":[{"attributeId":"SEX"}],"studyViewFilter":{` + clinical + `}}`, []string{"a", "b"}, true},
		{"a case list", `{"caseLists":[["all"]]}`, []string{"a", "b"}, false},
		{"studies named", `{"studyIds":["x"],` + clinical + `}`, nil, false},
		{"samples named", `{"sampleIdentifiers":[{"studyId":"x","sampleId":"s"}],` + clinical + `}`, nil, false},
		{"nothing to narrow by", `{}`, nil, false},
		{"nested, nothing to narrow by", `{"studyViewFilter":{}}`, nil, true},
		// The body's own filters are not a study-view filter's.
		{"nested, no filter", `{"genericAssayDataFilters":[{"stableId":"x","profileType":"y"}]}`, nil, true},
		{"not an object", `[1]`, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, ok := withStudies([]byte(c.body), !c.nested, studies)
			if c.want == nil {
				if ok {
					t.Fatalf("changed the body: %s", out)
				}
				return
			}
			if !ok {
				t.Fatal("left the body unchanged")
			}
			var f map[string]json.RawMessage
			json.Unmarshal(out, &f)
			if c.nested {
				var inner map[string]json.RawMessage
				json.Unmarshal(f["studyViewFilter"], &inner)
				if f["attributes"] == nil || f["clinicalDataFilters"] != nil || inner["attributes"] != nil {
					t.Fatalf("mixed the outer fields and the filter's: %s", out)
				}
				f = inner
			}
			var got []string
			json.Unmarshal(f["studyIds"], &got)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("studyIds = %v, want %v (%s)", got, c.want, out)
			}
			if _, ok := f["sampleIdentifiers"]; ok {
				t.Fatalf("kept sampleIdentifiers: %s", out)
			}
			if f["clinicalDataFilters"] == nil && f["caseLists"] == nil {
				t.Fatalf("lost the filter: %s", out)
			}
		})
	}
}

func TestStudyReadable(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/filtered-samples/fetch", nil)
	if !core.StudyReadable(r, "a") {
		t.Fatal("without a study policy every study is readable")
	}
	r = core.WithStudyReadable(r, func(id string) bool { return id == "a" })
	if !core.StudyReadable(r, "a") || core.StudyReadable(r, "b") {
		t.Fatal("the recorded policy is not applied")
	}
}
