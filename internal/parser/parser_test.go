package parser

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestSanitizeIdent(t *testing.T) {
	cases := map[string]string{
		"Hugo_Symbol": "hugo_symbol",
		"HGVSp_Short": "hgvsp_short",
		"t_alt_count": "t_alt_count",
		"5'Flank":     "_5_flank",
		"":            "col",
	}
	for in, want := range cases {
		if got := SanitizeIdent(in); got != want {
			t.Errorf("SanitizeIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeColumnsDisambiguates(t *testing.T) {
	got := SanitizeColumns([]string{"A", "A", "b"})
	want := []string{"a", "a_2", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestMatrixShape(t *testing.T) {
	shape := Shape([]string{"Hugo_Symbol", "Entrez_Gene_Id", "TCGA-A1-A0SB-01", "TCGA-A1-A0SD-01"})
	if !reflect.DeepEqual(shape.IDColumns, []string{"hugo_symbol", "entrez_gene_id"}) {
		t.Errorf("id columns: %v", shape.IDColumns)
	}
	if !reflect.DeepEqual(shape.SampleColumns, []string{"TCGA-A1-A0SB-01", "TCGA-A1-A0SD-01"}) {
		t.Errorf("sample columns: %v", shape.SampleColumns)
	}

	ga := Shape([]string{"ENTITY_STABLE_ID", "name", "description", "S1", "S2"})
	if !reflect.DeepEqual(ga.IDColumns, []string{"entity_stable_id", "name", "description"}) {
		t.Errorf("generic-assay id columns: %v", ga.IDColumns)
	}
}

func TestMatrixUnpivot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.txt")
	os.WriteFile(path, []byte("Hugo_Symbol\tEntrez_Gene_Id\tS1\tS2\nTP53\t7157\t-2\t0\nEGFR\t1956\t\t1\n"), 0o644)
	h, _ := ReadHeader(path)
	shape := Shape(h.Columns)
	var emitted uint64
	r, _ := OpenMatrix(path, shape, &emitted)
	out, _ := io.ReadAll(r)
	lines := splitLines(string(out))
	sort.Strings(lines)
	want := []string{"EGFR\t1956\tS2\t1", "TP53\t7157\tS1\t-2", "TP53\t7157\tS2\t0"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("unpivot got %v want %v", lines, want)
	}
	if emitted != 3 {
		t.Errorf("emitted = %d want 3", emitted)
	}
}

func TestParseAttributeMeta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data_clinical_sample.txt")
	os.WriteFile(path, []byte(
		"#Patient Identifier\tSample Identifier\tCancer Type\n"+
			"#Patient id\tSample id\tType of cancer\n"+
			"#STRING\tSTRING\tSTRING\n"+
			"#1\t1\t9\n"+
			"PATIENT_ID\tSAMPLE_ID\tCANCER_TYPE\n"+
			"P1\tS1\tBreast\n"), 0o644)
	attrs, err := ParseAttributeMeta(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(attrs) != 1 || attrs[0].AttrID != "CANCER_TYPE" || attrs[0].DisplayName != "Cancer Type" || attrs[0].Priority != "9" {
		t.Errorf("got %+v", attrs)
	}
}

func splitLines(s string) []string {
	var out []string
	for _, l := range splitTrim(s, '\n') {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func splitTrim(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
