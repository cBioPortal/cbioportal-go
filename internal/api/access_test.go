package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestAuthorities(t *testing.T) {
	u := User{Roles: []string{"ROLE_cbioportal:study_a", "cbioportal:Group1", "other:study_b", "study_c"}}
	byApp := accessPolicy{appName: "cbioportal", filterByAppName: true, alwaysShowGroup: "public"}
	got := byApp.authorities(u)
	for _, want := range []string{"STUDY_A", "GROUP1", "PUBLIC"} {
		if !got[want] {
			t.Errorf("filter by app name: missing %s in %v", want, got)
		}
	}
	if got["STUDY_B"] || got["STUDY_C"] || len(got) != 3 {
		t.Errorf("filter by app name: unexpected %v", got)
	}
	all := accessPolicy{appName: "cbioportal"}.authorities(u)
	if !all["CBIOPORTAL:STUDY_A"] || !all["STUDY_C"] || !all["OTHER:STUDY_B"] {
		t.Errorf("no app name filter: %v", all)
	}
}

func TestCanRead(t *testing.T) {
	auth := func(a ...string) map[string]bool {
		m := map[string]bool{}
		for _, x := range a {
			m[x] = true
		}
		return m
	}
	cases := []struct {
		auth          map[string]bool
		study, groups string
		want          bool
	}{
		{auth("STUDY_A"), "study_a", "", true},
		{auth("STUDY_A"), "study_b", "", false},
		{auth("ALL"), "anything", "", true},
		{auth(), "all", "", true},
		{auth("ALL_TCGA"), "brca_tcga", "", true},
		{auth("ALL_TCGA"), "brca_tcga_pub", "", false},
		{auth("ALL_NCI_TARGET"), "aml_target", "", true},
		{auth("ALL_NCI_TARGET"), "all_target_phase2", "", true},
		{auth("PUBLIC"), "study_x", "PUBLIC;GDAC", true},
		{auth("GDAC"), "study_x", " public ; gdac ", true},
		{auth("SU2C"), "study_x", "PUBLIC;GDAC", false},
	}
	for _, c := range cases {
		if got := canRead(c.auth, c.study, c.groups); got != c.want {
			t.Errorf("canRead(%v, %q, %q) = %v, want %v", c.auth, c.study, c.groups, got, c.want)
		}
	}
}

func TestBodyTargets(t *testing.T) {
	s, p, l := bodyTargets("/api/samples/fetch", []byte(`{"sampleIdentifiers":[{"studyId":"s1","sampleId":"x"}],
		"studyViewFilter":{"studyIds":["s2"]},"molecularProfileIds":["p1"],"sampleListIds":["l1"],
		"molecularProfileCaseIdentifiers":[{"molecularProfileId":"p2","caseId":"c"}]}`))
	slices.Sort(s)
	slices.Sort(p)
	if strings.Join(s, ",") != "s1,s2" || strings.Join(p, ",") != "p1,p2" || strings.Join(l, ",") != "l1" {
		t.Errorf("got studies %v profiles %v lists %v", s, p, l)
	}
	s, _, _ = bodyTargets("/api/studies/fetch", []byte(`["s1","s2"]`))
	if strings.Join(s, ",") != "s1,s2" {
		t.Errorf("studies/fetch: %v", s)
	}
	if s, _, _ := bodyTargets("/api/genes/fetch", []byte(`["TP53"]`)); s != nil {
		t.Errorf("gene list read as studies: %v", s)
	}
}

func TestPathTargets(t *testing.T) {
	for path, want := range map[string]string{
		"GET /api/studies/s1":                      "s1||",
		"GET /api/studies/s1/samples":              "s1||",
		"POST /api/studies/fetch":                  "||",
		"GET /api/molecular-profiles/p1/mutations": "|p1|",
		"POST /api/molecular-profiles/fetch":       "||",
		"GET /api/sample-lists/l1/sample-ids":      "||l1",
		"GET /api/export/study/s9.zip":             "s9||",
		"GET /api/genes/TP53":                      "||",
	} {
		method, p, _ := strings.Cut(path, " ")
		s, pr, l := pathTargets(httptest.NewRequest(method, p, nil))
		if got := strings.Join(s, ",") + "|" + strings.Join(pr, ",") + "|" + strings.Join(l, ","); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}

func TestFilterByStudy(t *testing.T) {
	body := []byte(`[{"studyId":"a","readPermission":true,"z":1},{"studyId":"b","readPermission":true,"z":2},{"name":"no study"}]`)
	readable := func(s string) bool { return s == "a" }
	h := http.Header{"Total-Count": {"3"}}
	if got := string(filterByStudy(body, readable, false, true, h)); got != `[{"studyId":"a","readPermission":true,"z":1},{"name":"no study"}]` {
		t.Errorf("filtered: %s", got)
	}
	if h.Get("total-count") != "2" {
		t.Errorf("total-count %q", h.Get("total-count"))
	}
	got := string(filterByStudy(body, readable, true, true, http.Header{}))
	if got != `[{"studyId":"a","readPermission":true,"z":1},{"studyId":"b","readPermission":false,"z":2},{"name":"no study"}]` {
		t.Errorf("show unauthorized: %s", got)
	}
}

func TestStudyIndexReadable(t *testing.T) {
	idx := studyIndex{
		groups:   map[string]string{"a": "PUBLIC", "b": "PRIVATE"},
		profiles: map[string]string{"b_mutations": "b"},
		lists:    map[string]string{"b_all": "b"},
	}
	auth := map[string]bool{"PUBLIC": true}
	if !idx.readable(auth, []string{"a", "virtual_study_id"}, nil, nil) {
		t.Error("readable study (and an unknown id) refused")
	}
	if idx.readable(auth, []string{"b"}, nil, nil) || idx.readable(auth, nil, []string{"b_mutations"}, nil) ||
		idx.readable(auth, nil, nil, []string{"b_all"}) {
		t.Error("unreadable study allowed")
	}
}
