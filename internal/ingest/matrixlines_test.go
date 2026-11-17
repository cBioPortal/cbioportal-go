package ingest

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/parser"
)

// TestMatrixLinesAndCells checks, against the server named by
// CLICKHOUSE_TEST_URL, that a matrix file is staged as lines in a new raw
// database and as cells in one whose matrix table already holds cells, and
// that both read the same through the matrix's name.
func TestMatrixLinesAndCells(t *testing.T) {
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"meta_study.txt": "type_of_cancer: brca\ncancer_study_identifier: lines_study\nname: L\ndescription: d\n",
		"meta_expression.txt": "cancer_study_identifier: lines_study\ngenetic_alteration_type: MRNA_EXPRESSION\n" +
			"datatype: CONTINUOUS\nstable_id: rna\nprofile_name: r\nprofile_description: r\nshow_profile_in_analysis_tab: false\n" +
			"data_filename: data_expression.txt\n",
		// A blank line, a short line, a repeated gene and empty cells.
		"data_expression.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS3\tS1\tS2\n" +
			"TP53\t7157\t1.5\t\t2\n\nKRAS\n" + "KRAS\t3845\t0.1\t0.2\t0.3\nTP53\t7157\t9\t9\t9\nEGFR\t1956\t\t\t\n",
	})
	want := []string{"1\tTP53\tS3\t1.5", "2\tTP53\tS2\t2", "3\tKRAS\tS3\t0.1", "4\tKRAS\tS1\t0.2", "5\tKRAS\tS2\t0.3"}

	for _, mode := range []string{"lines", "cells"} {
		t.Run(mode, func(t *testing.T) {
			db := fmt.Sprintf("ingest_lines_test_%s_%d", mode, os.Getpid())
			c := ch.New(base, db)
			if _, err := c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.ExecuteServerwide("DROP DATABASE IF EXISTS `" + db + "`") })
			if mode == "cells" {
				if err := c.CreateDatabase(); err != nil {
					t.Fatal(err)
				}
				if err := ensureSchema(c, "mrna_expression_matrix", []string{"hugo_symbol", "entrez_gene_id", "sample_id", "value"}); err != nil {
					t.Fatal(err)
				}
			}
			s, err := parser.Scan(dir)
			if err != nil {
				t.Fatal(err)
			}
			stats, err := IngestStudy(c, s)
			if err != nil {
				t.Fatal(err)
			}
			for _, st := range stats {
				if st.RowsLoaded != 5 || st.RowsExpected != 5 {
					t.Errorf("%s: %d rows loaded, %d expected, want 5", st.SourceFile, st.RowsLoaded, st.RowsExpected)
				}
			}
			out, err := c.Execute("SELECT row_num, hugo_symbol, sample_id, value FROM `" + db + "`.mrna_expression_matrix ORDER BY row_num FORMAT TSV")
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSpace(out), "\n"); !reflect.DeepEqual(got, want) {
				t.Errorf("cells %q, want %q", got, want)
			}
			engine, err := c.TableEngine("mrna_expression_matrix")
			if err != nil {
				t.Fatal(err)
			}
			if wantEngine := map[string]string{"lines": "View", "cells": "MergeTree"}[mode]; engine != wantEngine {
				t.Errorf("mrna_expression_matrix is a %s, want a %s", engine, wantEngine)
			}
			// Re-ingesting a changed file replaces its rows.
			writeFiles(t, dir, map[string]string{"data_expression.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS3\nTP53\t7157\t4\n"})
			defer writeFiles(t, dir, map[string]string{"data_expression.txt": "Hugo_Symbol\tEntrez_Gene_Id\tS3\tS1\tS2\n" +
				"TP53\t7157\t1.5\t\t2\n\nKRAS\n" + "KRAS\t3845\t0.1\t0.2\t0.3\nTP53\t7157\t9\t9\t9\nEGFR\t1956\t\t\t\n"})
			s, err = parser.Scan(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := IngestStudy(c, s); err != nil {
				t.Fatal(err)
			}
			out, err = c.Execute("SELECT row_num, hugo_symbol, sample_id, value FROM `" + db + "`.mrna_expression_matrix FORMAT TSV")
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(out); got != "1\tTP53\tS3\t4" {
				t.Errorf("after re-ingest: %q", got)
			}
		})
	}
}
