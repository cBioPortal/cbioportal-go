package apidocs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// TestAPIDocsOverlay pins the parameters laid over the served API
// descriptions: every operation a description lists gets its added paging
// (with its sortBy values) and added parameters once, and the description
// is otherwise unchanged.
func TestAPIDocsOverlay(t *testing.T) {
	type op struct {
		Parameters []map[string]any `json:"parameters"`
	}
	type doc struct {
		Paths map[string]map[string]op `json:"paths"`
	}
	for _, name := range []string{"v3.json", "v3-public.json", "v3-internal.json", "legacy-api.json"} {
		raw, err := apiDocsFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		served, err := overlayAPIDoc(name, raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var before, after doc
		if err := json.Unmarshal(raw, &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(served, &after); err != nil {
			t.Fatalf("%s: served description is no JSON: %v", name, err)
		}
		found := 0
		for _, a := range addedOperations() {
			key := a.Path
			if name == "legacy-api.json" {
				key = strings.TrimPrefix(key, "/api")
			}
			method := strings.ToLower(a.Method)
			old, ok := before.Paths[key][method]
			if !ok {
				continue
			}
			found++
			got := after.Paths[key][method].Parameters
			added := got[len(old.Parameters):]
			var want []string
			var sortValues []string
			for _, p := range a.Params {
				want = append(want, p.Name)
				if p.Name == "sortBy" {
					sortValues = p.Schema.Enum
				}
			}
			if len(got) != len(old.Parameters)+len(want) {
				t.Errorf("%s %s %s: %d parameters, want %d more than %d", name, a.Method, key, len(got), len(want), len(old.Parameters))
				continue
			}
			for i, n := range want {
				if added[i]["name"] != n || added[i]["in"] != "query" || added[i]["required"] != false {
					t.Errorf("%s %s %s: parameter %d is %v", name, a.Method, key, i, added[i])
				}
			}
			if sortValues != nil {
				enum := added[2]["enum"]
				if s, ok := added[2]["schema"].(map[string]any); ok {
					enum = s["enum"]
				}
				b, _ := json.Marshal(enum)
				w, _ := json.Marshal(sortValues)
				if string(b) != string(w) {
					t.Errorf("%s %s %s: sortBy enum %s, want %s", name, a.Method, key, b, w)
				}
			}
			// With the added parameters taken out, the operation is the
			// snapshot's.
			ob, _ := json.Marshal(old)
			trimmed := after.Paths[key][method]
			trimmed.Parameters = trimmed.Parameters[:len(old.Parameters)]
			if len(trimmed.Parameters) == 0 {
				trimmed.Parameters = nil
			}
			tb, _ := json.Marshal(trimmed)
			if string(ob) != string(tb) {
				t.Errorf("%s %s %s: other parameters changed", name, a.Method, key)
			}
		}
		if found < 10 {
			t.Errorf("%s: only %d added endpoints found", name, found)
		}
		// Everything outside the added parameters is unchanged.
		var b0, b1 map[string]any
		json.Unmarshal(raw, &b0)
		json.Unmarshal(served, &b1)
		delete(b0, "paths")
		delete(b1, "paths")
		x, _ := json.Marshal(b0)
		y, _ := json.Marshal(b1)
		if string(x) != string(y) {
			t.Errorf("%s: description outside its paths changed", name)
		}
	}
}

// TestAPIDocsOverlayYAML pins that the YAML descriptions get the
// parameters the JSON ones get, as lines in springdoc's layout.
func TestAPIDocsOverlayYAML(t *testing.T) {
	for _, name := range []string{"v3.yaml", "v3-public.yaml", "v3-internal.yaml"} {
		raw, err := apiDocsFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		jraw, _ := apiDocsFS.ReadFile(strings.TrimSuffix(name, ".yaml") + ".json")
		served, _ := overlayAPIDoc(name, raw)
		jserved, _ := overlayAPIDoc("v3.json", jraw)
		count := func(b []byte, s string) int { return strings.Count(string(b), s) }
		for _, p := range []string{"pageSize", "pageNumber", "sortBy", "direction", "entrezGeneId"} {
			yamlAdded := count(served, "      - name: "+p+"\n") - count(raw, "      - name: "+p+"\n")
			jsonAdded := count(jserved, `{"name":"`+p+`"`) - count(jraw, `{"name":"`+p+`"`)
			// The internal description lists none of the endpoints with an
			// added entrezGeneId.
			if yamlAdded != jsonAdded || (jsonAdded == 0 && p != "entrezGeneId") {
				t.Errorf("%s: %d %s parameters added, the JSON description %d", name, yamlAdded, p, jsonAdded)
			}
		}
		// Removing the added lines gives the snapshot back.
		var kept []string
		lines := strings.SplitAfter(string(served), "\n")
		for i := 0; i < len(lines); i++ {
			l := lines[i]
			if strings.HasPrefix(l, "      - name: ") && i+2 < len(lines) && strings.Contains(lines[i+2], "Added in go") {
				for i+1 < len(lines) && strings.HasPrefix(lines[i+1], "        ") {
					i++
				}
				continue
			}
			kept = append(kept, l)
		}
		got := strings.Join(kept, "")
		// Operations that had no parameters gained the parameters key.
		if got != string(raw) && strings.ReplaceAll(got, "      parameters:\n", "") != strings.ReplaceAll(string(raw), "      parameters:\n", "") {
			t.Errorf("%s: more than the added parameters changed", name)
		}
	}
}

// TestAPIDocsAddedParam pins that the copy-number GET is described with the
// entrezGeneId parameter go adds, as an optional int32 query parameter.
func TestAPIDocsAddedParam(t *testing.T) {
	raw, _ := apiDocsFS.ReadFile("v3.json")
	served, err := overlayAPIDoc("v3.json", raw)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Paths map[string]map[string]struct {
			Parameters []openAPIParam `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(served, &d); err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Paths[core.AddedParams[0].Path]["get"].Parameters {
		if p.Name == "entrezGeneId" {
			if p.Required || p.Schema.Type != "integer" || p.Schema.Format != "int32" || !strings.Contains(p.Description, "Added in go") {
				t.Errorf("entrezGeneId %+v", p)
			}
			return
		}
	}
	t.Error("no entrezGeneId on the copy-number GET")
}
