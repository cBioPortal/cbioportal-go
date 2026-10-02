package parser

import (
	"encoding/json"
	"testing"
)

func yamlJSON(t *testing.T, src string) string {
	t.Helper()
	v, err := ParseYAML(src)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestParseYAMLStudyTags(t *testing.T) {
	src := `Analyst:
  name: Jack
  email: jack@xyz.com
Loaded by: Jill
Load id: 34
git id: xxqaygqertqsg98qhpughqer
Analysis Id: 7asdlnagsd98gfaqsgf
Load Date: July 12, 2018
Data Source: Study XY123-456
Jira Ticket: Foo-1
Confluence URL: http://myserver/wxyz
Study sponsors:
  -
    name: john
    email: john@@xyz.com
  -
    name: jane
    email: jane@@xyz.com
`
	want := `{"Analyst":{"name":"Jack","email":"jack@xyz.com"},"Loaded by":"Jill","Load id":34,` +
		`"git id":"xxqaygqertqsg98qhpughqer","Analysis Id":"7asdlnagsd98gfaqsgf","Load Date":"July 12, 2018",` +
		`"Data Source":"Study XY123-456","Jira Ticket":"Foo-1","Confluence URL":"http://myserver/wxyz",` +
		`"Study sponsors":[{"name":"john","email":"john@@xyz.com"},{"name":"jane","email":"jane@@xyz.com"}]}`
	if got := yamlJSON(t, src); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestParseYAMLConstructs(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"scalars", "a: 1\nb: -2.5\nc: true\nd: ~\ne: 'it''s'\nf: \"x\\ty\"\ng: 012abc\nh:\n",
			`{"a":1,"b":-2.5,"c":true,"d":null,"e":"it's","f":"x\ty","g":"012abc","h":null}`},
		{"comments", "# header\na: b # trailing\nc: 'not # a comment'\n\n# between\nd: x#y\n",
			`{"a":"b","c":"not # a comment","d":"x#y"}`},
		{"inline seq items", "list:\n  - one\n  - two: 2\n    three: 3\n  - - nested\n",
			`{"list":["one",{"two":2,"three":3},["nested"]]}`},
		{"same-indent seq", "k:\n- a\n- b\nz: 1\n", `{"k":["a","b"],"z":1}`},
		{"flow", "a: [1, two, 'x, y']\nb: {k: v, n: 3}\n", `{"a":[1,"two","x, y"],"b":{"k":"v","n":3}}`},
		{"literal block", "a: |\n  line1\n  line2\nb: x\n", `{"a":"line1\nline2\n","b":"x"}`},
		{"folded block", "a: >-\n  one\n  two\n\n  three\n", `{"a":"one two\nthree"}`},
		{"url value", "u: http://h:8080/p\n", `{"u":"http://h:8080/p"}`},
		{"top-level seq", "- a\n- b\n", `["a","b"]`},
		{"document marker", "---\na: 1\n", `{"a":1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := yamlJSON(t, c.src); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestParseYAMLErrors(t *testing.T) {
	for _, src := range []string{"a: 'open\n", "a: 1\n   b: 2\n", "just text\n"} {
		if _, err := ParseYAML(src); err == nil {
			t.Errorf("expected error for %q", src)
		}
	}
}
