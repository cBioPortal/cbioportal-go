package transform

import (
	"strings"
	"testing"
)

// The production-clone build covers every portal object the staging build
// does (the API reads them all) plus the staging-shaped objects the API
// reads from the raw database, each built once, with every database
// placeholder resolved.
func TestFromProdSteps(t *testing.T) {
	b := &prodBuilder{p: "`P`", t: "`T`", r: "`R`"}
	built := map[string]bool{}
	for _, o := range b.steps() {
		key := o.db + "." + o.name
		if built[key] {
			t.Errorf("%s is built twice", key)
		}
		built[key] = true
		if strings.Contains(o.sql, "{p}") || strings.Contains(o.sql, "{t}") || strings.Contains(o.sql, "{r}") {
			t.Errorf("%s: unresolved database placeholder", key)
		}
	}
	portal := []string{
		"cancer_study", "type_of_cancer", "cancer_study_tags", "clinical_attribute_meta", "molecular_profile",
		"patient", "sample", "sample_list", "gene_panel", "clinical_sample", "clinical_patient", "sample_profile",
		"mutation", "cna", "structural_variant", "seg", "gene_key_map",
		"sample_derived", "clinical_data_derived", "genomic_event_derived", "genetic_alteration_derived",
		"generic_assay_data_derived", "sample_to_gene_panel_derived", "gene_panel_to_gene_derived",
		"clinical_event_derived", "clinical_event_data_derived",
		"resource_definition", "resource_study", "resource_patient", "resource_sample",
		"gistic", "gistic_to_gene", "mutsig",
	}
	for _, name := range portal {
		if !built["`T`."+name] {
			t.Errorf("portal object %s is not built", name)
		}
	}
	raw := []string{
		"genetic_profile", "cna_matrix", "mrna_expression_matrix", "methylation_matrix", "protein_level_matrix",
		"generic_assay_matrix", "generic_assay_profile_entity", "generic_assay_entity_meta", "timeline", "profile_sample",
	}
	for _, name := range raw {
		if !built["`R`."+name] {
			t.Errorf("raw object %s is not built", name)
		}
	}
}

// The study license passes through from production's cancer_study when the
// clone has the column, and is empty when its schema predates it.
func TestFromProdStudyLicense(t *testing.T) {
	studySQL := func(b *prodBuilder) string {
		for _, o := range b.steps() {
			if o.db == "`T`" && o.name == "cancer_study" {
				return o.sql
			}
		}
		t.Fatal("cancer_study is not built")
		return ""
	}
	without := studySQL(&prodBuilder{p: "`P`", t: "`T`", r: "`R`", studyCols: map[string]bool{"citation": true}})
	if !strings.Contains(without, "'' AS license") || strings.Contains(without, "c.license") {
		t.Errorf("without the column: %s", without)
	}
	with := studySQL(&prodBuilder{p: "`P`", t: "`T`", r: "`R`", studyCols: map[string]bool{"license": true}})
	if !strings.Contains(with, "ifNull(c.license, '') AS license") {
		t.Errorf("with the column: %s", with)
	}
}
