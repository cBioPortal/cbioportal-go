package export

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/parser"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

// exportGeneKeyStudy has a gene x sample matrix of each exported type whose
// genes are identified by one key column only: Hugo_Symbol alone (discrete,
// continuous and log2 CNA, methylation, discrete long CNA) or Entrez_Gene_Id
// alone (mRNA). HIST1H3I is an alias of H3C11.
var exportGeneKeyStudy = map[string]string{
	"meta_study.txt": "type_of_cancer: mixed\ncancer_study_identifier: export_gene_keys\nname: Export gene keys\n" +
		"description: Matrices with a single gene key column\nreference_genome: hg19\n",
	"meta_clinical_sample.txt": "cancer_study_identifier: export_gene_keys\ngenetic_alteration_type: CLINICAL\n" +
		"datatype: SAMPLE_ATTRIBUTES\ndata_filename: data_clinical_sample.txt\n",
	"data_clinical_sample.txt": "#Patient Identifier\tSample Identifier\n#Patient Identifier\tSample Identifier\n" +
		"#STRING\tSTRING\n#1\t1\nPATIENT_ID\tSAMPLE_ID\nP1\tS1\nP2\tS2\n",
	"meta_cna.txt":         geneKeyMeta("COPY_NUMBER_ALTERATION", "DISCRETE", "cna", "data_cna.txt"),
	"data_cna.txt":         "Hugo_Symbol\tS1\tS2\nTP53\t-2\t0\nKRAS\t2\t1\nHIST1H3I\t0\t-1\n",
	"meta_linear_cna.txt":  geneKeyMeta("COPY_NUMBER_ALTERATION", "CONTINUOUS", "linear_CNA", "data_linear_cna.txt"),
	"data_linear_cna.txt":  "Hugo_Symbol\tS1\tS2\nKRAS\t0.5\t-0.25\nTP53\t1.5\tNA\n",
	"meta_log2_cna.txt":    geneKeyMeta("COPY_NUMBER_ALTERATION", "LOG2-VALUE", "log2CNA", "data_log2_cna.txt"),
	"data_log2_cna.txt":    "Hugo_Symbol\tS1\tS2\nTP53\t-0.7\t0.1\n",
	"meta_methylation.txt": geneKeyMeta("METHYLATION", "CONTINUOUS", "methylation_hm27", "data_methylation.txt"),
	"data_methylation.txt": "Hugo_Symbol\tS1\tS2\nBRCA1\t0.9\t0.1\n",
	"meta_mrna.txt":        geneKeyMeta("MRNA_EXPRESSION", "CONTINUOUS", "rna_seq_mrna", "data_mrna.txt"),
	"data_mrna.txt":        "Entrez_Gene_Id\tS1\tS2\n7157\t10.5\t3\n3845\t2\t4.25\n",
	"meta_cna_long.txt":    geneKeyMeta("COPY_NUMBER_ALTERATION", "DISCRETE_LONG", "cna_long", "data_cna_long.txt"),
	"data_cna_long.txt":    "Hugo_Symbol\tSample_Id\tValue\nKRAS\tS2\t2\nKRAS\tS1\t0\nTP53\tS1\t-2\nTP53\tS2\t-1\n",
}

func geneKeyMeta(alterationType, datatype, stableID, file string) string {
	return fmt.Sprintf("cancer_study_identifier: export_gene_keys\ngenetic_alteration_type: %s\ndatatype: %s\n"+
		"stable_id: %s\nshow_profile_in_analysis_tab: true\nprofile_name: %s\nprofile_description: %s\ndata_filename: %s\n",
		alterationType, datatype, stableID, stableID, stableID, file)
}

// TestLiveExportGeneKeyColumns ingests, transforms and exports
// exportGeneKeyStudy when CLICKHOUSE_TEST_URL (e.g. http://localhost:18123)
// is set, reading genes from CLICKHOUSE_TEST_REF_DB (default cbioportal_ref).
// Each matrix is written as GeneSampleWideTableDatatypeExporter does:
// Hugo_Symbol and Entrez_Gene_Id of the stored gene, whichever key the file
// had.
func TestLiveExportGeneKeyColumns(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	refDB := os.Getenv("CLICKHOUSE_TEST_REF_DB")
	if refDB == "" {
		refDB = "cbioportal_ref"
	}
	admin := ch.New(base, "default")
	type count struct {
		N uint64 `json:"n"`
	}
	if n, err := ch.Query[count](admin, "SELECT count() AS n FROM system.tables WHERE database = '"+refDB+"' AND name = 'gene'"); err != nil || len(n) != 1 || n[0].N == 0 {
		t.Skipf("reference database %s has no gene table (%v)", refDB, err)
	}
	db := fmt.Sprintf("cbioportal_go_test_export_%d", os.Getpid())
	rawDB := db + "_raw"
	for _, d := range []string{db, rawDB} {
		if _, err := admin.Execute("CREATE DATABASE " + d); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = admin.Execute("DROP DATABASE IF EXISTS " + d) })
	}

	dir := t.TempDir()
	for name, content := range exportGeneKeyStudy {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	study, err := parser.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.IngestStudy(ch.New(base, rawDB), study); err != nil {
		t.Fatal(err)
	}
	if _, err := transform.Apply(ch.New(base, rawDB), rawDB, db, refDB); err != nil {
		t.Fatal(err)
	}

	c := ch.New(base, db)
	c.SetDatabaseAliases(rawDB, refDB)
	s := New(&core.State{CH: c, Props: map[string]string{"feature.study.export": "true"}})
	mux := http.NewServeMux()
	s.Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/export/study/export_gene_keys.zip", nil))
	body := w.Body.Bytes()
	if w.Code != http.StatusOK || bytes.Contains(body, []byte("ERROR: study export failed")) {
		t.Fatalf("export failed: %d %q", w.Code, body[max(0, len(body)-100):])
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	for name, want := range map[string]string{
		"data_copy_number_alteration_discrete_cna.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS1\tS2\n" +
			"H3C11\t8354\t0\t-1\nKRAS\t3845\t2\t1\nTP53\t7157\t-2\t0\n",
		"data_copy_number_alteration_continuous_linear_cna.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS1\tS2\n" +
			"KRAS\t3845\t0.5\t-0.25\nTP53\t7157\t1.5\tNA\n",
		"data_copy_number_alteration_log2-value_log2cna.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS1\tS2\n" +
			"TP53\t7157\t-0.7\t0.1\n",
		"data_methylation_continuous_methylation_hm27.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS1\tS2\n" +
			"BRCA1\t672\t0.9\t0.1\n",
		"data_mrna_expression_continuous_rna_seq_mrna.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS1\tS2\n" +
			"KRAS\t3845\t2\t4.25\nTP53\t7157\t10.5\t3\n",
		"data_copy_number_alteration_discrete_cna_long.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS2\tS1\n" +
			"KRAS\t3845\t2\t0\nTP53\t7157\t-1\t-2\n",
	} {
		got, ok := files[name]
		if !ok {
			names := make([]string, 0, len(files))
			for n := range files {
				names = append(names, n)
			}
			t.Errorf("%s not exported; files: %s", name, strings.Join(names, " "))
			continue
		}
		if got != want {
			t.Errorf("%s =\n%s\nwant\n%s", name, got, want)
		}
	}
}
