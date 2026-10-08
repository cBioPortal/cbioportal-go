package datahub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStudyDirs(t *testing.T) {
	got := StudyDirs([]string{
		"public/b_study/data_mutations.txt",
		"public/a_study/meta_study.txt",
		"public/a_study/case_lists/all.txt",
		"public/README.md",
		"crdc/gdc/gdc_x/meta_study.txt",
		"crdc/README.md",
		"reference_data/gene_panels/p.txt",
	})
	want := []string{"crdc/gdc/gdc_x", "public/a_study", "public/b_study"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if !TouchesGenePanels([]string{"x", "reference_data/gene_panels/p.txt"}) || TouchesGenePanels([]string{"reference_data/x"}) {
		t.Error("TouchesGenePanels")
	}
}

func TestSparsePatterns(t *testing.T) {
	got := SparsePatterns([]string{"public/a", "/public/b/", ""})
	want := []string{"/public/a/", "/public/b/", "/reference_data/gene_panels/", "/seedDB/seed-cbioportal_hg19_hg38_*.sql.gz"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNewestSeed(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, SeedDir), 0o755)
	for _, v := range []string{"v2.9.0", "v2.14.5", "v2.14.10", "v2.13.1"} {
		os.WriteFile(filepath.Join(repo, SeedDir, "seed-cbioportal_hg19_hg38_"+v+".sql.gz"), nil, 0o644)
	}
	if got := filepath.Base(NewestSeed(repo)); got != "seed-cbioportal_hg19_hg38_v2.14.10.sql.gz" {
		t.Errorf("got %s", got)
	}
	if NewestSeed(t.TempDir()) != "" {
		t.Error("seed in an empty checkout")
	}
}

func pointer(content string) (string, string) {
	sum := sha256.Sum256([]byte(content))
	oid := hex.EncodeToString(sum[:])
	return oid, fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, len(content))
}

func TestParsePointer(t *testing.T) {
	oid, text := pointer("hello")
	p, ok := ParsePointer([]byte(text))
	if !ok || p.OID != oid || p.Size != 5 {
		t.Errorf("got %+v %v", p, ok)
	}
	for _, bad := range []string{"hello", "version https://git-lfs.github.com/spec/v1\noid sha256:xyz\nsize 1\n", ""} {
		if _, ok := ParsePointer([]byte(bad)); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

// lfsServer serves objects (by oid) through the Git LFS batch API.
func lfsServer(t *testing.T, objects map[string]string, batches *int) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/objects/batch":
			*batches++
			var req struct {
				Operation string        `json:"operation"`
				Objects   []batchObject `json:"objects"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Operation != "download" || r.Header.Get("Accept") != "application/vnd.git-lfs+json" {
				http.Error(w, "bad request", 400)
				return
			}
			var out []map[string]any
			for _, o := range req.Objects {
				if _, ok := objects[o.OID]; !ok {
					out = append(out, map[string]any{"oid": o.OID, "error": map[string]any{"code": 404, "message": "missing"}})
					continue
				}
				out = append(out, map[string]any{"oid": o.OID, "size": o.Size, "actions": map[string]any{
					"download": map[string]any{"href": srv.URL + "/o/" + o.OID, "header": map[string]string{"X-Token": "t"}}}})
			}
			json.NewEncoder(w).Encode(map[string]any{"objects": out})
		case strings.HasPrefix(r.URL.Path, "/o/") && r.Header.Get("X-Token") == "t":
			fmt.Fprint(w, objects[strings.TrimPrefix(r.URL.Path, "/o/")])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPullLFS(t *testing.T) {
	repo := t.TempDir()
	study := filepath.Join(repo, "public", "s")
	os.MkdirAll(filepath.Join(study, "case_lists"), 0o755)
	oidA, ptrA := pointer("mutations data\n")
	oidB, ptrB := pointer("only in the fork\n")
	files := map[string]string{
		"data_mutations.txt":         ptrA,
		"copy_of_mutations.txt":      ptrA, // the same object twice
		"case_lists/all.txt":         ptrB,
		"meta_study.txt":             "cancer_study_identifier: s\n",
		"data_clinical_sample.txt":   "not a pointer\n",
		"../../outside_the_study.md": ptrA,
	}
	for name, content := range files {
		os.WriteFile(filepath.Join(study, name), []byte(content), 0o644)
	}

	var storeBatches, forkBatches int
	store := lfsServer(t, map[string]string{oidA: "mutations data\n"}, &storeBatches)
	fork := lfsServer(t, map[string]string{oidB: "only in the fork\n"}, &forkBatches)

	c := &Checkout{Dir: repo, LFSURL: store.URL + "/"}
	if _, err := c.PullLFS(context.Background(), "public/s"); err == nil || !strings.Contains(err.Error(), "case_lists/all.txt") {
		t.Fatalf("object missing from the store: %v", err)
	}
	c.FallbackLFSURL = fork.URL
	n, err := c.PullLFS(context.Background(), "public/s")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 { // the first call downloaded the store's object already
		t.Errorf("%d files downloaded", n)
	}
	for name, want := range map[string]string{
		"data_mutations.txt":       "mutations data\n",
		"copy_of_mutations.txt":    "mutations data\n",
		"case_lists/all.txt":       "only in the fork\n",
		"data_clinical_sample.txt": "not a pointer\n",
	} {
		if b, _ := os.ReadFile(filepath.Join(study, name)); string(b) != want {
			t.Errorf("%s: %q, want %q", name, b, want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "outside_the_study.md")); string(b) != ptrA {
		t.Error("a file outside the study was downloaded")
	}
	if storeBatches != 2 || forkBatches != 1 {
		t.Errorf("batch requests: store %d, fork %d", storeBatches, forkBatches)
	}
}

func TestPullLFSRejectsCorruptObject(t *testing.T) {
	repo := t.TempDir()
	_, ptr := pointer("right content")
	p, _ := ParsePointer([]byte(ptr))
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte(ptr), 0o644)
	var batches int
	srv := lfsServer(t, map[string]string{p.OID: "wrong content"}, &batches)
	c := &Checkout{Dir: repo, LFSURL: srv.URL}
	if _, err := c.PullLFS(context.Background(), "."); err == nil {
		t.Fatal("corrupt download accepted")
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "f.txt")); string(b) != ptr {
		t.Error("pointer file replaced by a corrupt download")
	}
	if _, err := os.Stat(filepath.Join(repo, "f.txt.lfs")); err == nil {
		t.Error("temporary file left behind")
	}
}

func init() { retryDelay = 0 }
