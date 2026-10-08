package api

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJavaGroups(t *testing.T) {
	for in, want := range map[string]string{
		"PUBLIC;GDAC;SU2C-PI3K": "SU2C-PI3K;PUBLIC;GDAC",
		"public":                "PUBLIC",
		"":                      "",
		"a;A;b":                 "A;B",
	} {
		if got := javaGroups(in); got != want {
			t.Errorf("javaGroups(%q) = %q, want %q", in, got, want)
		}
	}
}

// meta_study.txt carries the license after pmid when the study has one, and
// leaves the key out otherwise (CancerStudyMetadataExporterTests).
func TestCancerStudyMetadataLicense(t *testing.T) {
	for license, want := range map[string]string{
		"CC-BY-NC-ND-4.0": "cancer_study_identifier: s1\ntype_of_cancer: brca\nname: S1\ndescription: d\n" +
			"citation: Cell 2018\npmid: 1\nlicense: CC-BY-NC-ND-4.0\ngroups: PUBLIC\nreference_genome: hg19\n",
		"": "cancer_study_identifier: s1\ntype_of_cancer: brca\nname: S1\ndescription: d\n" +
			"citation: Cell 2018\npmid: 1\ngroups: PUBLIC\nreference_genome: hg19\n",
	} {
		var buf bytes.Buffer
		e := &studyExport{zw: zip.NewWriter(&buf), study: &exportStudyMeta{
			StudyID: "s1", TypeOfCancer: "BRCA", Name: "S1", Description: "d", Citation: "Cell 2018",
			Pmid: "1", License: license, Groups: "public",
		}}
		if err := e.cancerStudyMetadata(); err != nil {
			t.Fatal(err)
		}
		e.zw.Close()
		zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}
		f, err := zr.File[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(f)
		if string(got) != want {
			t.Errorf("license %q: meta_study.txt\n%s\nwant\n%s", license, got, want)
		}
	}
}

func TestStableSampleID(t *testing.T) {
	for in, want := range map[string]string{
		"TCGA-A1-A0SB-01":         "TCGA-A1-A0SB-01",
		"TCGA-A1-A0SB-01A-11D-A1": "TCGA-A1-A0SB-01",
		"TCGA-A1-A0SB":            "TCGA-A1-A0SB-01",
		"TCGA-A1-A0SB-Tumor":      "TCGA-A1-A0SB-01",
		"P01_Pri":                 "P01_Pri",
	} {
		if got := stableSampleID(in); got != want {
			t.Errorf("stableSampleID(%q) = %q, want %q", in, got, want)
		}
	}
}

// The discrete-long sample order is the column order of a gene x sample
// table filled line by line: each gene's samples in turn, genes first seen
// first.
func TestLongMatrixSamples(t *testing.T) {
	known := map[string]bool{"S1": true, "S2": true, "S3": true}
	cells := []matrixCell{
		{Entrez: 1, SampleID: "S2"}, {Entrez: 2, SampleID: "S3"}, {Entrez: 1, SampleID: "S1"},
		{Entrez: 2, SampleID: "S2"}, {Entrez: 3, SampleID: "X"},
	}
	got := longMatrixSamples(cells, known)
	want := []string{"S2", "S1", "S3"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Without feature.study.export the path is not an export endpoint.
func TestExportDisabled(t *testing.T) {
	s := &State{}
	mux := http.NewServeMux()
	s.routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/export/study/study_es_0.zip", nil))
	if w.Header().Get("Content-Type") == "application/zip" || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("export served while disabled: %v", w.Header())
	}
}

// A matrix staged from a file without Entrez_Gene_Id (or Hugo_Symbol) reads
// the absent key column as an empty string.
func TestMissingGeneKeyColumns(t *testing.T) {
	for _, c := range []struct {
		have []string
		want string
	}{
		{[]string{"study_id", "hugo_symbol", "entrez_gene_id", "sample_id", "value"}, ""},
		{[]string{"study_id", "hugo_symbol", "sample_id", "value"}, ", '' AS entrez_gene_id"},
		{[]string{"study_id", "entrez_gene_id", "sample_id", "value"}, ", '' AS hugo_symbol"},
	} {
		if got := missingGeneKeyColumns(c.have); got != c.want {
			t.Errorf("missingGeneKeyColumns(%v) = %q, want %q", c.have, got, c.want)
		}
	}
}
