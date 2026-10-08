package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// bulkRequests are requests of the bulk endpoints, by their "## name" or the
// start of their method and path in the fixture corpus
// (test/parity/fixtures.txt), whose first entry supplies the body.
var bulkRequests = []string{
	"GET /api/molecular-profiles/study_es_0_gistic/discrete-copy-number?",
	"POST /api/molecular-profiles/study_es_0_gistic/discrete-copy-number/fetch",
	"POST /api/molecular-profiles/study_es_0_mrna/molecular-data/fetch",
	"POST /api/molecular-data/fetch",
	"POST /api/generic_assay_data/study_es_0_mutational_signature/fetch",
	"POST /api/gene-panel-data/fetch",
	"POST /api/mutations/fetch",
	"POST /api/copy-number-segments/fetch",
	"POST /api/molecular-profiles/co-expressions/fetch",
	"## study_es_0:expression-enrichments:study_es_0_mrna",
	"POST /api/generic-assay-enrichments/fetch",
}

// fixtureRequest is a request of the fixture corpus.
type fixtureRequest struct{ name, method, path, body string }

// fixtureRequests returns the first corpus entry matching each of specs.
func fixtureRequests(t *testing.T, prefixes []string) []fixtureRequest {
	t.Helper()
	f, err := os.Open("../../test/parity/fixtures.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var all []fixtureRequest
	var cur *fixtureRequest
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "## "):
			all = append(all, fixtureRequest{name: strings.Fields(t)[1]})
			cur = &all[len(all)-1]
		case t == "" || strings.HasPrefix(t, "#") || cur == nil:
		case cur.method == "":
			cur.method, cur.path, _ = strings.Cut(t, " ")
		default:
			cur.body += line + "\n"
		}
	}
	var out []fixtureRequest
	for _, p := range prefixes {
		found := false
		for _, r := range all {
			if strings.HasPrefix(r.method+" "+r.path, p) || p == "## "+r.name {
				out = append(out, r)
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no corpus request %q", p)
		}
	}
	return out
}

// liveFixtureState returns the API state over the fixture database
// (CLICKHOUSE_TEST_URL, CLICKHOUSE_TEST_E2E_DB default cbioportal_e2e),
// its queries tagged with comment; it skips the test without them.
func liveFixtureState(t *testing.T, comment string) (*State, *ch.Client) {
	t.Helper()
	base := os.Getenv("CLICKHOUSE_TEST_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	db := os.Getenv("CLICKHOUSE_TEST_E2E_DB")
	if db == "" {
		db = "cbioportal_e2e"
	}
	admin := ch.New(base, "default")
	if out, err := admin.Execute("SELECT count() FROM system.databases WHERE name = '" + ch.SQLLit(db) + "'"); err != nil || strings.TrimSpace(out) != "1" {
		t.Skipf("fixture database %s not found (%v)", db, err)
	}
	t.Setenv("CLICKHOUSE_LOG_COMMENT", comment)
	c := ch.New(base, db)
	c.SetDatabaseAliases(db+"_raw", "")
	return NewState(c), admin
}

func serve(h http.Handler, r fixtureRequest) *httptest.ResponseRecorder {
	req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestLiveBulkLimits checks that the queries of the bulk endpoints run with
// the bulk limits: for each request, the query that reads the most rows
// carries the limits' settings.
func TestLiveBulkLimits(t *testing.T) {
	run := fmt.Sprintf("bulk-limits-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	limits := core.BulkLimits{MaxMemory: 10<<30 + 7, MaxTime: 9 * time.Minute, SortSpill: 1<<30 + 3}
	var admin *ch.Client
	comments := map[string]string{}
	for i, r := range fixtureRequests(t, bulkRequests) {
		comment := fmt.Sprintf("%s-%d", run, i)
		var s *State
		s, admin = liveFixtureState(t, comment)
		s.SetBulkLimits(limits)
		if w := serve(NewRouter(s), r); w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", r.method, r.path, w.Code, w.Body.String())
		}
		comments[comment] = r.method + " " + r.path
	}
	if _, err := admin.Execute("SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	type logged struct {
		Comment  string            `json:"log_comment"`
		ReadRows uint64            `json:"read_rows"`
		Settings map[string]string `json:"Settings"`
		Query    string            `json:"query"`
	}
	rows, err := ch.Query[logged](admin, "SELECT log_comment, read_rows, Settings, query FROM system.query_log "+
		"WHERE event_date >= yesterday() AND type = 'QueryFinish' AND query_kind = 'Select' AND startsWith(log_comment, '"+run+"-') "+
		"ORDER BY log_comment, read_rows DESC, event_time_microseconds")
	if err != nil {
		t.Fatal(err)
	}
	want := limits.Settings()
	checked := map[string]bool{}
	for _, row := range rows {
		if checked[row.Comment] {
			continue
		}
		checked[row.Comment] = true
		for k, v := range want {
			if row.Settings[k] != v {
				t.Errorf("%s: its largest query (%d rows) has %s = %q, want %q: %.200s",
					comments[row.Comment], row.ReadRows, k, row.Settings[k], v, row.Query)
			}
		}
	}
	for c, req := range comments {
		if !checked[c] {
			t.Errorf("%s: no query logged", req)
		}
	}
}

// TestLiveBulkLimitTrips checks a request stopped at its memory limit
// before any data is sent: a 400 with Java's error body, saying how to ask
// for less.
func TestLiveBulkLimitTrips(t *testing.T) {
	s, _ := liveFixtureState(t, fmt.Sprintf("bulk-limits-trip-%d", os.Getpid()))
	s.BulkCH = s.CH.WithSettings(map[string]string{"max_memory_usage": "1", "max_untracked_memory": "0"})
	h := NewRouter(s)
	for _, r := range fixtureRequests(t, []string{
		"GET /api/molecular-profiles/study_es_0_gistic/discrete-copy-number?",
		"POST /api/molecular-profiles/study_es_0_mrna/molecular-data/fetch",
		"POST /api/mutations/fetch",
		"POST /api/copy-number-segments/fetch",
	}) {
		w := serve(h, r)
		var body struct {
			Message string `json:"message"`
		}
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusBadRequest || body.Message != core.MsgQueryLimit("memory") ||
			!strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: %d %q %s", r.method, r.path, w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
	}
}
