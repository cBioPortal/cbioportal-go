package parser

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
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

// A gene's repeated rows are ignored, as ImportTabDelimData ignores them
// (acc_tcga_pan_can_atlas_2018's data_cna.txt lists RNU6-28P twice).
func TestMatrixUnpivotSkipsDuplicateRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.txt")
	os.WriteFile(path, []byte("Hugo_Symbol\tEntrez_Gene_Id\tS1\tS2\nRNU6-28P\t100873756\t0\t0\nTP53\t7157\t1\t2\nRNU6-28P\t100873756\t-2\t0\n"), 0o644)
	h, _ := ReadHeader(path)
	r, err := OpenMatrix(path, Shape(h.Columns), nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	want := []string{"1\tRNU6-28P\t100873756\tS1\t0", "2\tRNU6-28P\t100873756\tS2\t0", "3\tTP53\t7157\tS1\t1", "4\tTP53\t7157\tS2\t2"}
	if got := splitLines(string(out)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestMatrixUnpivot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.txt")
	os.WriteFile(path, []byte("#comment\nHugo_Symbol\tEntrez_Gene_Id\tS1\tS2\nTP53\t7157\t-2\t0\n\n#note\nEGFR\t1956\t\t1\nKRAS\t3845\t\t\nBRAF\t673\t5\t6"), 0o644)
	h, _ := ReadHeader(path)
	shape := Shape(h.Columns)
	var emitted uint64
	r, _ := OpenMatrix(path, shape, &emitted)
	out, _ := io.ReadAll(r)
	lines := splitLines(string(out))
	// Cells are numbered in file order: row by row, each row's cells in
	// column order, empty cells skipped.
	want := []string{"1\tTP53\t7157\tS1\t-2", "2\tTP53\t7157\tS2\t0", "3\tEGFR\t1956\tS2\t1",
		"4\tBRAF\t673\tS1\t5", "5\tBRAF\t673\tS2\t6"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("unpivot got %q want %q", lines, want)
	}
	if emitted != 5 {
		t.Errorf("emitted = %d want 5", emitted)
	}
}

func TestBodyReaderNumbersDataRows(t *testing.T) {
	body := "#Patient Identifier\tSample Identifier\n#STRING\tSTRING\n" +
		"PATIENT_ID\tSAMPLE_ID\n" +
		"P1\tS1\n\n\r\n#mid-file comment\nP2\tS2\r\nP3\tS3"
	path := filepath.Join(t.TempDir(), "data_clinical_sample.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(skipHeader bool) ([]string, uint64) {
		var emitted uint64
		r, err := OpenBody(path, skipHeader, &emitted)
		if err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return splitLines(string(out)), emitted
	}
	got, n := read(true)
	want := []string{"1\tP1\tS1", "2\tP2\tS2", "3\tP3\tS3"}
	if !reflect.DeepEqual(got, want) || n != 3 {
		t.Errorf("with header: got %q (%d) want %q (3)", got, n, want)
	}
	got, n = read(false)
	want = append([]string{"1\tPATIENT_ID\tSAMPLE_ID"}, "2\tP1\tS1", "3\tP2\tS2", "4\tP3\tS3")
	if !reflect.DeepEqual(got, want) || n != 4 {
		t.Errorf("headerless: got %q (%d) want %q (4)", got, n, want)
	}
}

// Files with CRLF line ends (datahub's brca_hta9_htan_2022 timeline) and
// with classic Mac CR line ends (chol_tcga_pan_can_atlas_2018's patient
// file) read as Java's readLine reads them: lines without carriage returns.
func TestReadersSplitCarriageReturnLines(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"crlf": "#Patient Identifier\tStart\r\nPATIENT_ID\tSTART_DATE\r\nP1\t1\r\n\r\nP2\t2\r\n",
		"cr":   "#Patient Identifier\tStart\rPATIENT_ID\tSTART_DATE\rP1\t1\r\rP2\t2\r",
	} {
		path := filepath.Join(dir, name+".txt")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		h, err := ReadHeader(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := []string{"PATIENT_ID", "START_DATE"}; !reflect.DeepEqual(h.Columns, want) {
			t.Errorf("%s: header %q want %q", name, h.Columns, want)
		}
		r, err := OpenBody(path, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if want := "1\tP1\t1\n2\tP2\t2\n"; string(out) != want {
			t.Errorf("%s: body %q want %q", name, out, want)
		}
	}
}

// ImportClinicalData stores display names and descriptions as written
// (pdac_msk_2024's "Ancestry " on www); a blank display name falls back to
// the attribute id.
func TestParseAttributeMetaKeepsSpaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data_clinical_patient.txt")
	os.WriteFile(path, []byte("#Patient Identifier\tAncestry \t \n#Patient id\tAncestry label \tx\n#STRING\tSTRING\tSTRING\n#1\t1\t1\n"+
		"PATIENT_ID\tANCESTRY\tOTHER\nP1\ta\tb\n"), 0o644)
	attrs, err := ParseAttributeMeta(path, true)
	if err != nil {
		t.Fatal(err)
	}
	got := [][2]string{}
	for _, a := range attrs {
		got = append(got, [2]string{a.DisplayName, a.Description})
	}
	if want := [][2]string{{"Ancestry ", "Ancestry label "}, {"OTHER", "x"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestLineEndsAcrossReads(t *testing.T) {
	// One byte per read: a CRLF split over two reads is still one line end.
	r := &lineEnds{r: iotest.OneByteReader(strings.NewReader("a\r\nb\rc\n\r\r\nd"))}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a\nb\nc\n\n\nd"; string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
	if got := ParseMeta(normalizeLineEnds("a: 1\rb: 2\r\n")).Get("b"); got != "2" {
		t.Errorf("meta with CR line ends: b = %q", got)
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

func TestReadSequencedSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data_mutations.txt")
	body := "#version 2.4\n#sequenced_samples: S1 S2\tS3\nHugo_Symbol\tTumor_Sample_Barcode\n#sequenced_samples: S9\nTP53\tS1\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSequencedSamples(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"S1", "S2", "S3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ReadSequencedSamples = %v, want %v", got, want)
	}
}

// A segment file is no molecular profile, even when its meta file has a
// stable_id (TCGA PanCancer studies' meta_cna_hg19_seg.txt).
func TestSegmentFileIsNoMolecularProfile(t *testing.T) {
	seg := DataFile{AlterationType: "COPY_NUMBER_ALTERATION", Datatype: "SEG", StableID: "s_segment"}
	cna := DataFile{AlterationType: "COPY_NUMBER_ALTERATION", Datatype: "DISCRETE", StableID: "gistic"}
	if seg.IsMolecularProfile() || !cna.IsMolecularProfile() {
		t.Errorf("IsMolecularProfile: segment %v, discrete CNA %v", seg.IsMolecularProfile(), cna.IsMolecularProfile())
	}
}

func TestSampleID(t *testing.T) {
	for in, want := range map[string]string{
		"TCGA-RZ-AB0B-01A":             "TCGA-RZ-AB0B-01",
		"TCGA-RZ-AB0B-01A-11D-A39W-08": "TCGA-RZ-AB0B-01",
		"TCGA-RZ-AB0B-01":              "TCGA-RZ-AB0B-01",
		"TCGA-RZ-AB0B":                 "TCGA-RZ-AB0B-01",
		"TCGA-13-1479-Tumor":           "TCGA-13-1479-01",
		"TCGA-13-1479-Normal":          "TCGA-13-1479-11",
		"TCGA-XY-1234-1A":              "TCGA-XY-1234-1A",
		"P-0001776-T02-IM6":            "P-0001776-T02-IM6",
	} {
		if got := SampleID(in); got != want {
			t.Errorf("SampleID(%q) = %q, want %q", in, got, want)
		}
	}
}
