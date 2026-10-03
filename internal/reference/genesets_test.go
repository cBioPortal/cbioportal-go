package reference

import (
	"reflect"
	"testing"
)

// The study_es_0 gene set tree of cBioPortal's e2e fixtures.
const testTree = `Custom Gene sets:
  Gene sets:
    - GO_CARDIAC_CHAMBER_DEVELOPMENT
Different Gene sets from Random Institute:
  Gene sets:
    - GO_ACYLGLYCEROL_HOMEOSTASIS
    - GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS
  Institutes Subcategory 1:
    Gene sets:
      - GO_ATP_DEPENDENT_CHROMATIN_REMODELING
  Institutes Subcategory 2:
    Gene sets:
      - GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS
`

func TestParseGenesetTree(t *testing.T) {
	root, err := parseGenesetTree(testTree)
	if err != nil {
		t.Fatal(err)
	}
	want := &yamlNode{entries: []yamlEntry{
		{key: "Custom Gene sets", child: &yamlNode{entries: []yamlEntry{
			{key: "Gene sets", list: []string{"GO_CARDIAC_CHAMBER_DEVELOPMENT"}},
		}}},
		{key: "Different Gene sets from Random Institute", child: &yamlNode{entries: []yamlEntry{
			{key: "Gene sets", list: []string{"GO_ACYLGLYCEROL_HOMEOSTASIS", "GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS"}},
			{key: "Institutes Subcategory 1", child: &yamlNode{entries: []yamlEntry{
				{key: "Gene sets", list: []string{"GO_ATP_DEPENDENT_CHROMATIN_REMODELING"}},
			}}},
			{key: "Institutes Subcategory 2", child: &yamlNode{entries: []yamlEntry{
				{key: "Gene sets", list: []string{"GO_ANATOMICAL_STRUCTURE_FORMATION_INVOLVED_IN_MORPHOGENESIS"}},
			}}},
		}}},
	}}
	if !reflect.DeepEqual(root, want) {
		t.Errorf("got %+v", root)
	}
}

func TestParseGenesetTreeSequenceAtKeyIndent(t *testing.T) {
	// YAML allows a block sequence at the same indentation as its key.
	root, err := parseGenesetTree("A:\n  Gene sets:\n  - 'X'\n  - Y # comment\n  B:\n    Gene sets:\n      - Z\n")
	if err != nil {
		t.Fatal(err)
	}
	a := root.entries[0].child
	if !reflect.DeepEqual(a.entries[0].list, []string{"X", "Y"}) || a.entries[1].key != "B" {
		t.Errorf("got %+v", a)
	}
}

func TestParseGenesetTreeErrors(t *testing.T) {
	for _, src := range []string{"A: b\n", "- x\n", "A:\n    B:\n  C:\n"} {
		if _, err := parseGenesetTree(src); err == nil {
			t.Errorf("%q: want an error", src)
		}
	}
}
