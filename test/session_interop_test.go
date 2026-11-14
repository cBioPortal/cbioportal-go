package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Session interoperability: go (API) and the Java backend (JAVA_API) share
// one cBioPortal session service, as go.cbioportal.org and www.cbioportal.org
// do. Whatever one server saves, the other reads back the same, and both
// answer the same for requests that use it (docs/testing.md, "Session
// service"). Java runs anonymously; go may run with a user.

func javaBase(t *testing.T) string {
	t.Helper()
	requireAPI(t)
	b := strings.TrimRight(os.Getenv("JAVA_API"), "/")
	if b == "" {
		t.Skip("JAVA_API not set: no Java backend sharing go's session service")
	}
	return b
}

// call sends a request to base and returns its status and decoded JSON body.
func call(t *testing.T, base, method, path string, body any, header map[string]string) (int, any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s%s: %v", method, base, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			out = string(raw)
		}
	}
	return resp.StatusCode, out
}

// saveSession saves a session of type typ on base and returns its id.
func saveSession(t *testing.T, base, typ string, data any) string {
	t.Helper()
	st, out := call(t, base, "POST", "/api/session/"+typ, data, nil)
	m, _ := out.(map[string]any)
	id, _ := m["id"].(string)
	if st != http.StatusOK || id == "" {
		t.Fatalf("saving a %s on %s: %d %v", typ, base, st, out)
	}
	return id
}

func interopSessions(stamp string) map[string]any {
	samples := []map[string]any{{"id": "study_es_0", "samples": []string{"TCGA-A1-A0SB-01", "TCGA-A1-A0SD-01"}}}
	return map[string]any{
		"main_session": map[string]any{"cancer_study_list": "study_es_0", "Z_SCORE_THRESHOLD": "2.0", "RPPA_SCORE_THRESHOLD": "2.0",
			"profileFilter": "mutations", "case_set_id": "study_es_0_all", "gene_list": "TP53 " + stamp, "geneset_list": " ", "tab_index": "tab_visualize",
			"Action": "Submit"},
		"virtual_study": map[string]any{"name": "interop vs " + stamp, "description": "", "studies": samples, "origin": []string{"study_es_0"},
			"studyViewFilter": map[string]any{"studyIds": []string{"study_es_0"}}},
		"group":              map[string]any{"name": "interop group " + stamp, "description": "", "studies": samples, "origin": []string{"study_es_0"}},
		"comparison_session": map[string]any{"origin": []string{"study_es_0"}, "clinicalAttributeName": stamp, "groups": []any{map[string]any{"name": "a", "studies": samples}}},
		"custom_data": map[string]any{"displayName": "interop " + stamp, "datatype": "STRING", "patientAttribute": false, "origin": []string{"study_es_0"},
			"data": []map[string]any{
				{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SB-01", "patientId": "TCGA-A1-A0SB", "value": "red"},
				{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SD-01", "patientId": "TCGA-A1-A0SD", "value": "blue"},
				{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SE-01", "patientId": "TCGA-A1-A0SE", "value": "red"},
			}},
	}
}

// TestInteropSessionsReadBack saves each type of session on one server and
// reads it on both: the two answers are the same document.
func TestInteropSessionsReadBack(t *testing.T) {
	java, goAPI := javaBase(t), apiBase()
	stamp := fmt.Sprintf("interop-%d", time.Now().UnixNano())
	for typ, data := range interopSessions(stamp) {
		for _, writer := range []string{goAPI, java} {
			id := saveSession(t, writer, typ, data)
			path := "/api/session/" + typ + "/" + id
			stG, fromGo := call(t, goAPI, "GET", path, nil, nil)
			stJ, fromJava := call(t, java, "GET", path, nil, nil)
			if stG != http.StatusOK || stJ != http.StatusOK || !reflect.DeepEqual(fromGo, fromJava) {
				t.Errorf("%s saved on %s: go %d %v\njava %d %v", typ, writer, stG, fromGo, stJ, fromJava)
				continue
			}
			doc, _ := fromGo.(map[string]any)
			// Java's VirtualStudy and CustomDataSession leave out the type.
			if doc["id"] != id || (doc["type"] != nil && doc["type"] != typ) {
				t.Errorf("%s saved on %s: read back %v", typ, writer, fromGo)
			}
			if d, _ := doc["data"].(map[string]any); d == nil || !strings.Contains(fmt.Sprint(d), stamp) {
				t.Errorf("%s saved on %s: data lost: %v", typ, writer, doc["data"])
			}
		}
	}
	// A session neither server saved is missing on both.
	stG, _ := call(t, goAPI, "GET", "/api/session/virtual_study/000000000000000000000000", nil, nil)
	stJ, _ := call(t, java, "GET", "/api/session/virtual_study/000000000000000000000000", nil, nil)
	wantEq(t, []int{stG, stJ}, []int{http.StatusNotFound, http.StatusNotFound})
}

// TestInteropCustomData saves a custom data chart on one server and counts
// and filters by it on both.
func TestInteropCustomData(t *testing.T) {
	java, goAPI := javaBase(t), apiBase()
	data := interopSessions(fmt.Sprintf("interop-%d", time.Now().UnixNano()))["custom_data"]
	for _, writer := range []string{goAPI, java} {
		id := saveSession(t, writer, "custom_data", data)
		counts := map[string]any{"attributes": []map[string]any{{"attributeId": id}},
			"studyViewFilter": map[string]any{"studyIds": []string{"study_es_0"}}}
		filter := map[string]any{"studyIds": []string{"study_es_0"},
			"customDataFilters": []map[string]any{{"attributeId": id, "values": []map[string]any{{"value": "red"}}}}}
		for _, c := range []struct {
			path string
			body any
		}{
			{"/api/custom-data-counts/fetch", counts},
			{"/api/custom-data-bin-counts/fetch?dataBinMethod=STATIC", counts},
			{"/api/filtered-samples/fetch", filter},
		} {
			stG, g := call(t, goAPI, "POST", c.path, c.body, nil)
			stJ, j := call(t, java, "POST", c.path, c.body, nil)
			if stG != stJ || !sameUnordered(g, j) {
				t.Errorf("custom data saved on %s, %s: go %d %v\njava %d %v", writer, c.path, stG, g, stJ, j)
			}
		}
	}
}

// TestInteropPublicVirtualStudy publishes a virtual study on one server and
// un-publishes it on the other; both list it while it is published.
func TestInteropPublicVirtualStudy(t *testing.T) {
	java, goAPI := javaBase(t), apiBase()
	key := map[string]string{"X-PUBLISHER-API-KEY": "SECRETKEY"}
	listed := func(base, id string) bool {
		st, out := call(t, base, "GET", "/api/public_virtual_studies", nil, nil)
		list, _ := out.([]any)
		if st != http.StatusOK {
			t.Fatalf("%s public_virtual_studies: %d %v", base, st, out)
		}
		return slices.ContainsFunc(list, func(v any) bool { m, _ := v.(map[string]any); return m["id"] == id })
	}
	data := interopSessions(fmt.Sprintf("interop-%d", time.Now().UnixNano()))["virtual_study"]
	for _, pair := range [][2]string{{goAPI, java}, {java, goAPI}} {
		publisher, other := pair[0], pair[1]
		id := saveSession(t, publisher, "virtual_study", data)
		path := "/api/public_virtual_studies/" + id
		if st, out := call(t, publisher, "POST", path+"?typeOfCancerId=brca", nil, key); st != http.StatusOK {
			t.Fatalf("publishing on %s: %d %v", publisher, st, out)
		}
		if !listed(goAPI, id) || !listed(java, id) {
			t.Errorf("published on %s: go lists it %v, java %v", publisher, listed(goAPI, id), listed(java, id))
		}
		_, g := call(t, goAPI, "GET", "/api/session/virtual_study/"+id, nil, nil)
		_, j := call(t, java, "GET", "/api/session/virtual_study/"+id, nil, nil)
		if !reflect.DeepEqual(g, j) {
			t.Errorf("published on %s: go %v\njava %v", publisher, g, j)
		}
		if st, out := call(t, other, "DELETE", path, nil, key); st != http.StatusOK {
			t.Fatalf("un-publishing on %s: %d %v", other, st, out)
		}
		if listed(goAPI, id) || listed(java, id) {
			t.Errorf("un-published on %s: still listed", other)
		}
	}
}

// sameUnordered compares two JSON values, arrays as multisets.
func sameUnordered(a, b any) bool {
	norm := func(v any) string {
		if arr, ok := v.([]any); ok {
			parts := make([]string, len(arr))
			for i, x := range arr {
				s, _ := json.Marshal(x)
				parts[i] = string(s)
			}
			slices.Sort(parts)
			return strings.Join(parts, "\n")
		}
		s, _ := json.Marshal(v)
		return string(s)
	}
	return norm(a) == norm(b)
}
