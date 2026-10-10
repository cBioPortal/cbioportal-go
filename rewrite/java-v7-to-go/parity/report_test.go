package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCountsAndResult(t *testing.T) {
	rs := []Result{
		{Name: "a", Outcome: "identical"},
		{Name: "b", Outcome: "known"},
		{Name: "c", Outcome: "nondeterministic", Reference: "differs"},
		{Name: "d", Outcome: "identical", Reference: "differs", Retried: true},
		{Name: "e", Outcome: "identical", Reference: "unverified"},
	}
	c := count(rs)
	if c.All != 5 || c.Identical != 3 || c.Known != 1 || c.Nondeterministic != 1 || c.JavaDiffers != 2 || c.JavaUnverified != 1 || c.Retried != 1 {
		t.Errorf("counts %+v", c)
	}
	if c.Bad() != 0 || c.Result() != "known" {
		t.Errorf("bad %d result %q", c.Bad(), c.Result())
	}
	for _, tt := range []struct {
		outcome, want string
	}{{"error", "error"}, {"status", "status mismatch"}, {"differs", "unexplained diff"}} {
		c := count(append(slices.Clone(rs), Result{Name: "x", Outcome: tt.outcome}))
		if c.Bad() != 1 || c.Result() != tt.want {
			t.Errorf("%s: bad %d result %q", tt.outcome, c.Bad(), c.Result())
		}
	}
	if got := count(nil).Result(); got != "identical" {
		t.Errorf("empty: %q", got)
	}
}

func TestUnexplainedAndNew(t *testing.T) {
	rs := []Result{{Name: "z", Outcome: "differs"}, {Name: "a", Outcome: "status"}, {Name: "m", Outcome: "known"}, {Name: "b", Outcome: "error"}}
	names := unexplainedNames(rs)
	if !slices.Equal(names, []string{"a", "b", "z"}) {
		t.Fatalf("unexplained %q", names)
	}
	if got := newSince([]string{"a", "q"}, names); !slices.Equal(got, []string{"b", "z"}) {
		t.Errorf("new %q", got)
	}
	if got := newSince(nil, names); !slices.Equal(got, names) {
		t.Errorf("new without a last run %q", got)
	}
}

func TestNights(t *testing.T) {
	prior := []string{"2026-10-05 clean", "2026-10-06 unclean", "2026-10-07 clean", "2026-10-08 unclean"}
	lines, streak := nights(prior, "2026-10-08", true)
	if want := []string{"2026-10-05 clean", "2026-10-06 unclean", "2026-10-07 clean", "2026-10-08 clean"}; !slices.Equal(lines, want) {
		t.Errorf("lines %q", lines)
	}
	if streak != 2 {
		t.Errorf("streak %d, want 2 (tonight replaces the earlier record)", streak)
	}
	if _, s := nights(prior, "2026-10-08", false); s != 0 {
		t.Errorf("unclean night: streak %d", s)
	}
	// A night without a record breaks the streak.
	if _, s := nights([]string{"2026-10-01 clean"}, "2026-10-03", true); s != 1 {
		t.Errorf("gap: streak %d", s)
	}
	// Month boundaries count back by calendar day.
	if _, s := nights([]string{"2026-09-30 clean"}, "2026-10-01", true); s != 2 {
		t.Errorf("month boundary: streak %d", s)
	}
}

func TestDecideIssue(t *testing.T) {
	for _, tt := range []struct {
		streak int
		want   issueAction
	}{{0, issueKeep}, {goal - 1, issueKeep}, {goal, issueClose}, {goal + 2, issueClose}} {
		if got := decideIssue(tt.streak); got != tt.want {
			t.Errorf("decideIssue(%d) = %d, want %d", tt.streak, got, tt.want)
		}
	}
}

func TestStatusBody(t *testing.T) {
	got := statusBody("org/repo", []string{"2026-10-08 unclean", "2026-10-09 clean"},
		map[string]string{"2026-10-09": "https://x/c/2"}, 1, "2026-10-09")
	for _, want := range []string{
		"**7 clean nights in a row**",
		"Clean nights in a row: **1 of 7**",
		"| 2026-10-09 | clean | [report](https://x/c/2) |\n| 2026-10-08 | unexplained differences | — |",
		"https://github.com/org/repo/blob/main/docs/rewrite/parity.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status body lacks %q:\n%s", want, got)
		}
	}
}

func TestDescribeVersion(t *testing.T) {
	s := func(v string) *string { return &v }
	if got := describeVersion(s("7.1.2"), s("0123456789abcdef"), s("2.14.5"), nil); got != "7.1.2, commit 0123456789ab, db 2.14.5" {
		t.Errorf("%q", got)
	}
	if got := describeVersion(s("go"), s("not set"), s(""), s("1.0")); got != "go, derived tables 1.0" {
		t.Errorf("%q", got)
	}
}

const corpusHead = `# Requests sampled from production
# since: 2026-10-07 22:30:00
# window: 2026-10-07 22:30 to 2026-10-08 10:00 UTC (first and last request seen)
# server www.cbioportal.org: 90 read-only requests over 10 endpoints
# server api.cbioportal.org: 10 read-only requests over 2 endpoints
# endpoints: 12 of the window's 40 read-only endpoints sampled
## first
GET /api/studies
`

func TestReport(t *testing.T) {
	rs := []Result{
		{Name: "api-studies", Request: "GET /api/studies", Outcome: "identical"},
		{Name: "old", Request: "GET /api/a", Outcome: "differs", Count: 2, Diffs: []string{"d1", "d2", "d3", "d4", "d5", "d6"}},
		{Name: "fresh", Request: "POST /api/b", Body: `{"x":1}`, Outcome: "status", StatusA: 500, StatusB: 200},
		{Name: "nd", Request: "GET /api/c", Outcome: "nondeterministic", Reference: "differs", ReferenceDiffs: []string{"r1"}},
		{Name: "kn", Request: "GET /api/d", Outcome: "known", Retried: true},
	}
	got := report(reportInput{
		Day: "2026-10-08", A: "https://go", B: "https://java", VersionA: "go", VersionB: "7.1.2",
		Rate: "4", Minutes: 3, CorpusDate: "2026-10-08", Corpus: corpusHead,
		Results: rs, New: []string{"fresh"}, Streak: 0, RunDir: "/runs/2026-10-08",
	})
	for _, want := range []string{
		"# Nightly parity 2026-10-08\n\nResult: **unexplained diff** (2 unexplained of 5 requests, 1 new since the last run)\n",
		"Clean nights in a row: **0** (go/no-go for api.cbioportal.org: 7)\n",
		"Java nondeterministic: 1 requests answered differently by B's two runs, 1 of them also differing from A (not counted as unexplained)\n",
		"- Corpus: 5 requests sampled from production traffic, generated 2026-10-08; 3 min at 4/s to B\n",
		"- Window: 2026-10-07 22:30 to 2026-10-08 10:00 UTC (first and last request seen)\n",
		"- Window traffic on www.cbioportal.org: 90 read-only requests over 10 endpoints\n- Window traffic on api.cbioportal.org: 10",
		"- Endpoints: 12 of the window's 40 read-only endpoints sampled\n",
		"| api.cbioportal.org | 1 | 1 | 0 | 0 | 0 |\n| www.cbioportal.org | 4 | 0 | 1 | 1 | 2 |\n",
		"| unexplained difference | 1 |\n| status mismatch | 1 |\n| error | 0 |\n",
		"1 requests failed in transport, with 502/503/504, or with a Java-only 500",
		"New since the last run: `fresh`\n",
		// New requests first.
		"\n### fresh (new): status a=500 b=200\n\n`POST /api/b`\n\nbody: `{\"x\":1}`\n\n```\n\n```\n\n### old: differs, 2 differences\n\n`GET /api/a`\n\n```\nd1\nd2\nd3\nd4\nd5\n```\n",
		"## Java nondeterministic\n\nB answered these differently on its two runs (b, b'); \"also A\" marks those where A differed from B as well.\n\n### nd (also A)\n\n`GET /api/c`\n\n```\nr1\n```\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q\n---\n%s", want, got)
		}
	}
	clean := report(reportInput{Day: "2026-10-08", Corpus: corpusHead, Results: rs[:1], Streak: 3})
	if strings.Contains(clean, "## Unexplained") || strings.Contains(clean, "## Java nondeterministic") || !strings.Contains(clean, "Result: **identical**") {
		t.Errorf("clean report:\n%s", clean)
	}
}

func TestReportTruncates(t *testing.T) {
	var rs []Result
	var fresh []string
	for i := range 70 {
		n := fmt.Sprintf("r%02d", i)
		rs = append(rs, Result{Name: n, Request: strings.Repeat("x", 400), Outcome: "error"})
		fresh = append(fresh, n)
	}
	got := report(reportInput{Day: "2026-10-08", Results: rs, New: fresh, RunDir: "/run"})
	if !strings.Contains(got, " and 10 more\n") || !strings.Contains(got, "… and 30 more, listed in /run/unexplained.txt on the parity host.") {
		t.Errorf("no truncation notes:\n%s", got[:500])
	}
	if strings.Count(got, "\n### ") != shown || !strings.Contains(got, "`"+strings.Repeat("x", 300)+"`") || strings.Contains(got, strings.Repeat("x", 301)) {
		t.Error("sections not cut to 40 requests of 300 characters")
	}
}

func TestParseEnvFile(t *testing.T) {
	vars, err := parseEnvFile(strings.NewReader("# creds\n\nCLICKHOUSE_URL=https://h:8443\nexport CLICKHOUSE_USER='u'\nCLICKHOUSE_PASSWORD=\"p=w\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if vars["CLICKHOUSE_URL"] != "https://h:8443" || vars["CLICKHOUSE_USER"] != "u" || vars["CLICKHOUSE_PASSWORD"] != "p=w" {
		t.Errorf("%q", vars)
	}
	if _, err := parseEnvFile(strings.NewReader("not a pair\n")); err == nil {
		t.Error("a line without = parsed")
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	old, recent := filepath.Join(dir, "2026-09-01"), filepath.Join(dir, "2026-10-07")
	for _, d := range []string{old, filepath.Join(recent, "cache")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-20 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	pruneRuns(dir, 15*24*time.Hour)
	if _, err := os.Stat(old); err == nil {
		t.Error("old run kept")
	}
	if _, err := os.Stat(filepath.Join(recent, "cache")); err == nil {
		t.Error("response cache kept")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("recent run removed")
	}
}

func TestMainUsage(t *testing.T) {
	var out, errOut strings.Builder
	if code := Main(nil, &out, &errOut); code != 2 {
		t.Errorf("no args: exit %d", code)
	}
	if code := Main([]string{"frobnicate"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), `unknown command "frobnicate"`) {
		t.Errorf("unknown: exit %d %q", code, errOut.String())
	}
	if code := Main([]string{"run", "extra"}, &out, &errOut); code != 2 {
		t.Errorf("run with an argument: exit %d", code)
	}
}
