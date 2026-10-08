package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// The served API descriptions are the Java portal's (apidocs), with the
// paging parameters go adds (addedPagedEndpoints) laid over them as they
// are served, so the snapshots stay as scripts/refresh-api-docs.sh fetches
// them. Endpoints a description does not list are left out.

// Descriptions of the added parameters: Java's, with what go adds.
const (
	addedPageSizeDoc   = "Page size of the result list. Added in go: optional; without it, the whole list is returned."
	addedPageNumberDoc = "Page number of the result list, from 0. Added in go."
	addedSortByDoc     = "Name of the property that the result list is sorted by. Added in go; items without the property come last."
	addedDirectionDoc  = "Direction of the sort. Added in go."
)

// openAPIParam is an OpenAPI 3 query parameter, in springdoc's key order.
type openAPIParam struct {
	Name        string        `json:"name"`
	In          string        `json:"in"`
	Description string        `json:"description"`
	Required    bool          `json:"required"`
	Schema      openAPISchema `json:"schema"`
}

type openAPISchema struct {
	Type    string   `json:"type"`
	Format  string   `json:"format,omitempty"`
	Default any      `json:"default,omitempty"`
	Maximum *int     `json:"maximum,omitempty"`
	Minimum *int     `json:"minimum,omitempty"`
	Enum    []string `json:"enum,omitempty"`
}

// swaggerParam is a Swagger 2.0 query parameter, in the legacy
// description's key order.
type swaggerParam struct {
	Name        string   `json:"name"`
	In          string   `json:"in"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Type        string   `json:"type"`
	Default     any      `json:"default,omitempty"`
	Maximum     *int     `json:"maximum,omitempty"`
	Minimum     *int     `json:"minimum,omitempty"`
	Format      string   `json:"format,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// openAPIParams are the added paging parameters of a.
func openAPIParams(a core.AddedPaging) []openAPIParam {
	one, zero, maxSize := 1, 0, core.MaxPageSize
	ps := []openAPIParam{
		{Name: "pageSize", In: "query", Description: addedPageSizeDoc,
			Schema: openAPISchema{Type: "integer", Format: "int32", Maximum: &maxSize, Minimum: &one}},
		{Name: "pageNumber", In: "query", Description: addedPageNumberDoc,
			Schema: openAPISchema{Type: "integer", Format: "int32", Default: &zero, Minimum: &zero}},
	}
	if a.Sorted() {
		ps = append(ps,
			openAPIParam{Name: "sortBy", In: "query", Description: addedSortByDoc,
				Schema: openAPISchema{Type: "string", Enum: a.Sort.Values}},
			openAPIParam{Name: "direction", In: "query", Description: addedDirectionDoc,
				Schema: openAPISchema{Type: "string", Default: "ASC", Enum: []string{"ASC", "DESC"}}})
	}
	return ps
}

// swaggerParams are openAPIParams in Swagger 2.0 form.
func swaggerParams(a core.AddedPaging) []swaggerParam {
	var out []swaggerParam
	for _, p := range openAPIParams(a) {
		out = append(out, swaggerParam{Name: p.Name, In: p.In, Description: p.Description, Type: p.Schema.Type,
			Default: p.Schema.Default, Maximum: p.Schema.Maximum, Minimum: p.Schema.Minimum, Format: p.Schema.Format,
			Enum: p.Schema.Enum})
	}
	return out
}

// member is one member of a JSON object: the byte range of its value.
type member struct {
	key        string
	start, end int
}

// objectMembers lists the members of the JSON object at doc[at:].
func objectMembers(doc []byte, at int) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(doc[at:]))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("no object at %d", at)
	}
	var out []member
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := t.(string)
		start := at + int(dec.InputOffset())
		for start < len(doc) && (doc[start] == ':' || doc[start] == ' ' || doc[start] == '\n' || doc[start] == '\t' || doc[start] == '\r') {
			start++
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, member{key: key, start: start, end: at + int(dec.InputOffset())})
	}
	return out, nil
}

func memberOf(ms []member, key string) (member, bool) {
	for _, m := range ms {
		if m.key == key {
			return m, true
		}
	}
	return member{}, false
}

// overlayJSONDoc adds the added paging parameters to the operations of a
// JSON API description: OpenAPI 3 (paths as served), or Swagger 2.0 with
// paths below its basePath (swagger2).
func overlayJSONDoc(doc []byte, swagger2 bool) ([]byte, error) {
	top, err := objectMembers(doc, 0)
	if err != nil {
		return nil, err
	}
	paths, ok := memberOf(top, "paths")
	if !ok {
		return doc, nil
	}
	pathMembers, err := objectMembers(doc, paths.start)
	if err != nil {
		return nil, err
	}
	type insert struct {
		at   int
		text string
	}
	var inserts []insert
	for _, a := range core.AddedPagedEndpoints {
		key := a.Path
		if swagger2 {
			key = strings.TrimPrefix(key, "/api")
		}
		p, ok := memberOf(pathMembers, key)
		if !ok {
			continue
		}
		ops, err := objectMembers(doc, p.start)
		if err != nil {
			return nil, err
		}
		op, ok := memberOf(ops, strings.ToLower(a.Method))
		if !ok {
			continue
		}
		var items []any
		if swagger2 {
			for _, x := range swaggerParams(a) {
				items = append(items, x)
			}
		} else {
			for _, x := range openAPIParams(a) {
				items = append(items, x)
			}
		}
		var parts []string
		for _, it := range items {
			b, err := json.Marshal(it)
			if err != nil {
				return nil, err
			}
			parts = append(parts, string(b))
		}
		list := strings.Join(parts, ",")
		fields, err := objectMembers(doc, op.start)
		if err != nil {
			return nil, err
		}
		if params, ok := memberOf(fields, "parameters"); ok {
			if len(bytes.TrimSpace(doc[params.start+1:params.end-1])) > 0 {
				list = "," + list
			}
			inserts = append(inserts, insert{params.end - 1, list})
		} else {
			inserts = append(inserts, insert{op.start + 1, `"parameters":[` + list + "],"})
		}
	}
	slices.SortFunc(inserts, func(a, b insert) int { return b.at - a.at })
	out := slices.Clone(doc)
	for _, in := range inserts {
		out = slices.Insert(out, in.at, []byte(in.text)...)
	}
	return out, nil
}

// overlayYAMLDoc adds the added paging parameters to the operations of a
// springdoc YAML description: paths at indent 2, operations at 4, their
// fields at 6.
func overlayYAMLDoc(doc []byte) []byte {
	lines := strings.SplitAfter(string(doc), "\n")
	indent := func(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }
	type insert struct {
		at   int
		text string
	}
	var inserts []insert
	for _, a := range core.AddedPagedEndpoints {
		pathAt := slices.IndexFunc(lines, func(l string) bool {
			return l == "  "+a.Path+":\n" || l == `  "`+a.Path+`":`+"\n"
		})
		if pathAt < 0 {
			continue
		}
		opAt := -1
		for i := pathAt + 1; i < len(lines) && indent(lines[i]) > 2; i++ {
			if lines[i] == "    "+strings.ToLower(a.Method)+":\n" {
				opAt = i
				break
			}
		}
		if opAt < 0 {
			continue
		}
		end := opAt + 1
		for end < len(lines) && (indent(lines[end]) > 4 || strings.TrimSpace(lines[end]) == "") {
			end++
		}
		items := yamlParams(a)
		paramsAt := -1
		for i := opAt + 1; i < end; i++ {
			if lines[i] == "      parameters:\n" {
				paramsAt = i
				break
			}
		}
		if paramsAt >= 0 {
			at := paramsAt + 1
			for at < end && (indent(lines[at]) > 6 || strings.HasPrefix(lines[at], "      - ")) {
				at++
			}
			inserts = append(inserts, insert{at, items})
			continue
		}
		at := end
		for i := opAt + 1; i < end; i++ {
			if indent(lines[i]) == 6 && (strings.HasPrefix(lines[i], "      requestBody:") || strings.HasPrefix(lines[i], "      responses:")) {
				at = i
				break
			}
		}
		inserts = append(inserts, insert{at, "      parameters:\n" + items})
	}
	slices.SortFunc(inserts, func(a, b insert) int { return b.at - a.at })
	for _, in := range inserts {
		lines = slices.Insert(lines, in.at, in.text)
	}
	return []byte(strings.Join(lines, ""))
}

// yamlParams are a's added parameters as springdoc writes parameters in
// YAML.
func yamlParams(a core.AddedPaging) string {
	var b strings.Builder
	for _, p := range openAPIParams(a) {
		desc, _ := json.Marshal(p.Description)
		fmt.Fprintf(&b, "      - name: %s\n        in: query\n        description: %s\n        required: false\n        schema:\n          type: %s\n",
			p.Name, desc, p.Schema.Type)
		if p.Schema.Format != "" {
			fmt.Fprintf(&b, "          format: %s\n", p.Schema.Format)
		}
		switch d := p.Schema.Default.(type) {
		case *int:
			fmt.Fprintf(&b, "          default: %d\n", *d)
		case string:
			fmt.Fprintf(&b, "          default: %s\n", d)
		}
		if p.Schema.Maximum != nil {
			fmt.Fprintf(&b, "          maximum: %d\n", *p.Schema.Maximum)
		}
		if p.Schema.Minimum != nil {
			fmt.Fprintf(&b, "          minimum: %d\n", *p.Schema.Minimum)
		}
		if len(p.Schema.Enum) > 0 {
			b.WriteString("          enum:\n")
			for _, v := range p.Schema.Enum {
				fmt.Fprintf(&b, "          - %s\n", v)
			}
		}
	}
	return b.String()
}

// overlayAPIDoc lays the added paging over the description file name.
func overlayAPIDoc(name string, doc []byte) ([]byte, error) {
	switch {
	case strings.HasSuffix(name, ".yaml"):
		return overlayYAMLDoc(doc), nil
	case name == "legacy-api.json":
		return overlayJSONDoc(doc, true)
	}
	return overlayJSONDoc(doc, false)
}
