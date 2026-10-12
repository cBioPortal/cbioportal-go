package molecular

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/ingest"
	"github.com/cbioportal/cbioportal-go/internal/parser"
	"github.com/cbioportal/cbioportal-go/internal/transform"
)

// TestLiveMutationsUncalledProfile imports the golden_c fixture study (a
// MUTATION_EXTENDED and a MUTATION_UNCALLED profile) when
// CLICKHOUSE_TEST_URL is set, reading genes from CLICKHOUSE_TEST_REF_DB
// (default cbioportal_ref). Each profile answers its own mutations: the
// uncalled profile's records, and the extended profile's without the
// uncalled ones Java's MutationMapper adds for the same samples
// (docs/rewrite/java-quirks.md).
func TestLiveMutationsUncalledProfile(t *testing.T) {
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
	db := fmt.Sprintf("cbioportal_go_test_uncalled_%d", os.Getpid())
	rawDB := db + "_raw"
	for _, d := range []string{db, rawDB} {
		if _, err := admin.Execute("CREATE DATABASE " + d); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = admin.Execute("DROP DATABASE IF EXISTS " + d) })
	}
	study, err := parser.Scan("../../transform/testdata/golden/studies/golden_c")
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
	mux := http.NewServeMux()
	New(&core.State{CH: c, Props: map[string]string{}}).Routes(mux)

	type record struct {
		MolecularProfileID string `json:"molecularProfileId"`
		SampleID           string `json:"sampleId"`
		MutationStatus     string `json:"mutationStatus"`
		EntrezGeneID       int64  `json:"entrezGeneId"`
	}
	call := func(method, path, body string) (*httptest.ResponseRecorder, []record) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var out []record
		if w.Body.Len() > 0 {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
		}
		return w, out
	}
	profiles := func(rs []record) map[string]int {
		m := map[string]int{}
		for _, r := range rs {
			m[r.MolecularProfileID]++
		}
		return m
	}
	ident := func(profile string) string {
		return `{"sampleMolecularIdentifiers":[{"molecularProfileId":"` + profile + `","sampleId":"GC-P1-01"}]}`
	}

	_, got := call(http.MethodPost, "/api/mutations/fetch", ident("golden_c_mutations_uncalled"))
	if len(got) != 1 || got[0].MolecularProfileID != "golden_c_mutations_uncalled" || got[0].EntrezGeneID != 238 {
		t.Errorf("uncalled identifiers: %+v, want GC-P1-01's uncalled ALK record", got)
	}
	w, _ := call(http.MethodPost, "/api/mutations/fetch?projection=META", ident("golden_c_mutations_uncalled"))
	if n := w.Header().Get("total-count"); n != "1" {
		t.Errorf("uncalled identifiers META total-count %q, want 1", n)
	}
	_, got = call(http.MethodPost, "/api/mutations/fetch", ident("golden_c_mutations"))
	if p := profiles(got); len(got) == 0 || p["golden_c_mutations"] != len(got) {
		t.Errorf("extended identifiers: %v, want the extended profile's records only", p)
	}
	_, got = call(http.MethodPost, "/api/mutations/fetch", `{"molecularProfileIds":["golden_c_mutations_uncalled"]}`)
	if p := profiles(got); len(got) == 0 || p["golden_c_mutations_uncalled"] != len(got) {
		t.Errorf("uncalled profile: %v, want its uncalled records", p)
	}
	_, got = call(http.MethodGet, "/api/molecular-profiles/golden_c_mutations_uncalled/mutations?sampleListId=golden_c_all&entrezGeneId=238", "")
	if len(got) != 1 || got[0].MolecularProfileID != "golden_c_mutations_uncalled" || got[0].SampleID != "GC-P1-01" {
		t.Errorf("uncalled profile ALK: %+v, want GC-P1-01's uncalled record", got)
	}
	_, got = call(http.MethodPost, "/api/molecular-profiles/golden_c_mutations_uncalled/mutations/fetch", `{"sampleIds":["GC-P1-01"]}`)
	if len(got) != 1 || got[0].MolecularProfileID != "golden_c_mutations_uncalled" || got[0].MutationStatus != "UNCALLED" {
		t.Errorf("uncalled profile fetch: %+v, want GC-P1-01's uncalled record", got)
	}
}
