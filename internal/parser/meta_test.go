package parser

import "testing"

func TestVariantClassificationFilter(t *testing.T) {
	for meta, want := range map[string]string{
		"stable_id: mutations":                                 "",
		"variant_classification_filter:":                       `[""]`,
		"variant_classification_filter: Silent, Intron,":       `["Silent","Intron"]`,
		"variant_classification_filter: ,":                     `[]`,
		"variant_classification_filter: , Silent":              `["","Silent"]`,
		"variant_classification_filter: Nonsense_Mutation , ,": `["Nonsense_Mutation",""]`,
	} {
		if got := variantClassificationFilter(ParseMeta(meta)); got != want {
			t.Errorf("%q: got %s, want %s", meta, got, want)
		}
	}
}

func TestIsMetaFile(t *testing.T) {
	for name, want := range map[string]bool{
		"meta_study.txt": true, "ascn_test_study_meta_cna_hg19_seg.txt": true, "META_CNA.txt": true,
		"meta2.txt": true, "data_metadata.txt": false, "metabolism.txt": false, "data_CNA.txt": false,
		".meta_study.txt": false, "meta_study.txt~": false,
	} {
		if got := IsMetaFile(name); got != want {
			t.Errorf("IsMetaFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestShapeWithFeatures(t *testing.T) {
	cols := []string{"ENTITY_STABLE_ID", "name", "description", "confidenceStatement", "S1", "S2"}
	got := ShapeWithFeatures(cols, []string{"name", "description", "confidenceStatement"})
	if len(got.IDColumns) != 4 || len(got.SampleColumns) != 2 || got.SampleColumns[0] != "S1" {
		t.Errorf("shape: %+v", got)
	}
	if got := Shape(cols); len(got.SampleColumns) != 3 {
		t.Errorf("without the property list confidenceStatement reads as a sample: %+v", got)
	}
}
