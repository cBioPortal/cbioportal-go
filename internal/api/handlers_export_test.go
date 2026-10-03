package api

import (
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
