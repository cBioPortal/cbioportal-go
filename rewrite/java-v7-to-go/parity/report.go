package parity

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// goal is the number of clean nights in a row that is the go/no-go for
// serving api.cbioportal.org from go.
const goal = 7

// shown is how many requests each report section details.
const shown = 40

// Result is one request of paritydiff's -json output.
type Result struct {
	Name           string   `json:"name"`
	Request        string   `json:"request"`
	Body           string   `json:"body,omitempty"`
	Outcome        string   `json:"outcome"`
	Count          int      `json:"differences,omitempty"`
	Diffs          []string `json:"diffs,omitempty"`
	Known          string   `json:"known,omitempty"`
	StatusA        int      `json:"statusA"`
	StatusB        int      `json:"statusB"`
	Retried        bool     `json:"retried,omitempty"`
	Reference      string   `json:"reference,omitempty"`
	ReferenceDiffs []string `json:"referenceDiffs,omitempty"`
}

// unexplained reports whether the request counts against a clean night.
func (r Result) unexplained() bool {
	return r.Outcome == "differs" || r.Outcome == "status" || r.Outcome == "error"
}

// Counts are the requests of a run by outcome.
type Counts struct {
	All, Identical, Known, Differs, Status, Error, Nondeterministic int
	JavaFailed, Retried, JavaDiffers, JavaUnverified                int
}

func count(results []Result) Counts {
	c := Counts{All: len(results)}
	for _, r := range results {
		switch r.Outcome {
		case "identical":
			c.Identical++
		case "known":
			c.Known++
		case "differs":
			c.Differs++
		case "status":
			c.Status++
		case "error":
			c.Error++
		case "nondeterministic":
			c.Nondeterministic++
		case "java-failed":
			c.JavaFailed++
		}
		if r.Retried {
			c.Retried++
		}
		switch r.Reference {
		case "differs":
			c.JavaDiffers++
		case "unverified":
			c.JavaUnverified++
		}
	}
	return c
}

// Bad is the number of unexplained requests.
func (c Counts) Bad() int { return c.Differs + c.Status + c.Error }

// Result names the night's worst outcome.
func (c Counts) Result() string {
	switch {
	case c.Differs > 0:
		return "unexplained diff"
	case c.Status > 0:
		return "status mismatch"
	case c.Error > 0:
		return "error"
	case c.JavaFailed > 0:
		return "Production failed, go worked"
	case c.Known > 0:
		return "known"
	}
	return "identical"
}

// unexplainedNames are the names of the unexplained requests, sorted.
func unexplainedNames(results []Result) []string {
	var names []string
	for _, r := range results {
		if r.unexplained() {
			names = append(names, r.Name)
		}
	}
	slices.Sort(names)
	return names
}

// newSince returns the names of current that last lacks, in current's order.
func newSince(last, current []string) []string {
	seen := map[string]bool{}
	for _, n := range last {
		seen[n] = true
	}
	var out []string
	for _, n := range current {
		if !seen[n] {
			out = append(out, n)
		}
	}
	return out
}

// nights records tonight in the "<date> clean|unclean" lines of nights.txt
// (replacing an earlier record of the same date) and returns the sorted
// lines and the clean nights in a row up to tonight.
func nights(lines []string, day string, clean bool) ([]string, int) {
	night := "unclean"
	if clean {
		night = "clean"
	}
	var out []string
	for _, l := range lines {
		if l != "" && !strings.HasPrefix(l, day+" ") {
			out = append(out, l)
		}
	}
	out = append(out, day+" "+night)
	slices.Sort(out)
	isClean := map[string]bool{}
	for _, l := range out {
		isClean[l] = true
	}
	streak := 0
	d, err := time.Parse(time.DateOnly, day)
	for err == nil && isClean[d.Format(time.DateOnly)+" clean"] {
		streak++
		d = d.AddDate(0, 0, -1)
	}
	return out, streak
}

// corpusHeader returns the values of the corpus header lines "# <key>…".
func corpusHeader(corpus, key string) []string {
	var out []string
	for l := range strings.SplitSeq(corpus, "\n") {
		if v, ok := strings.CutPrefix(l, "# "+key); ok {
			out = append(out, v)
		}
	}
	return out
}

// cut returns the first n characters of s.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// reportInput is what the nightly report describes.
type reportInput struct {
	Day                      string
	A, B, VersionA, VersionB string
	Rate                     string
	Minutes                  int
	CorpusDate               string
	Corpus                   string // the corpus text, for its header
	Results                  []Result
	New                      []string // unexplained requests new since the last run
	Streak                   int
	RunDir                   string
}

// report renders the nightly report in Markdown.
func report(in reportInput) string {
	c := count(in.Results)
	bad := c.Bad()
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	p("# Nightly parity %s", in.Day)
	p("")
	p("Result: **%s** (%d unexplained of %d requests, %d new since the last run)", c.Result(), bad, c.All, len(in.New))
	p("")
	p("Clean nights in a row: **%d** (go/no-go for api.cbioportal.org: %d)", in.Streak, goal)
	p("")
	unverified := ""
	if c.JavaUnverified > 0 {
		unverified = fmt.Sprintf("; %d not verified (B's second answer failed)", c.JavaUnverified)
	}
	p("Java nondeterministic: %d requests answered differently by B's two runs, %d of them also differing from A (not counted as unexplained)%s",
		c.JavaDiffers, c.Nondeterministic, unverified)
	p("")
	p("- A: %s (%s)", in.A, in.VersionA)
	p("- B: %s (%s), every request sent twice", in.B, in.VersionB)
	p("- Corpus: %d requests sampled from production traffic, written %s; %d min at %s/s to B", c.All, in.CorpusDate, in.Minutes, in.Rate)
	p("- Window: %s", strings.Join(corpusHeader(in.Corpus, "window: "), "\n"))
	for _, s := range corpusHeader(in.Corpus, "server ") {
		p("- Window traffic on %s", s)
	}
	p("- Endpoints: %s", strings.Join(corpusHeader(in.Corpus, "endpoints: "), "\n"))
	p("")
	p("| server | requests | identical | known | Java nondeterministic | production failed | unexplained |")
	p("|---|---:|---:|---:|---:|---:|---:|")
	for _, api := range []bool{true, false} {
		var rs []Result
		for _, r := range in.Results {
			if strings.HasPrefix(r.Name, "api-") == api {
				rs = append(rs, r)
			}
		}
		if len(rs) == 0 {
			continue
		}
		server := "www.cbioportal.org"
		if api {
			server = "api.cbioportal.org"
		}
		sc := count(rs)
		p("| %s | %d | %d | %d | %d | %d | %d |", server, sc.All, sc.Identical, sc.Known, sc.Nondeterministic, sc.JavaFailed, sc.Bad())
	}
	p("")
	p("| outcome | requests |")
	p("|---|---:|")
	p("| identical | %d |", c.Identical)
	p("| known difference | %d |", c.Known)
	p("| Java nondeterministic | %d |", c.Nondeterministic)
	p("| Production failed, go worked | %d |", c.JavaFailed)
	p("| unexplained difference | %d |", c.Differs)
	p("| status mismatch | %d |", c.Status)
	p("| error | %d |", c.Error)
	if c.Retried > 0 {
		p("")
		p("%d requests failed in transport, with 502/503/504, or with a Java-only 500 and were sent again after 2 min; the table counts the retry.", c.Retried)
	}
	if bad > 0 {
		p("")
		p("## Unexplained")
		if len(in.New) > 0 {
			var names []string
			for _, n := range in.New[:min(60, len(in.New))] {
				names = append(names, "`"+n+"`")
			}
			more := ""
			if len(in.New) > 60 {
				more = fmt.Sprintf(" and %d more", len(in.New)-60)
			}
			p("")
			p("New since the last run: %s%s", strings.Join(names, " "), more)
		}
		isNew := map[string]bool{}
		for _, n := range in.New {
			isNew[n] = true
		}
		var rs []Result
		for _, r := range in.Results {
			if r.unexplained() {
				rs = append(rs, r)
			}
		}
		slices.SortStableFunc(rs, func(x, y Result) int { return boolRank(!isNew[x.Name]) - boolRank(!isNew[y.Name]) })
		for _, r := range rs[:min(shown, len(rs))] {
			mark := ""
			if isNew[r.Name] {
				mark = " (new)"
			}
			detail := ""
			switch r.Outcome {
			case "status":
				detail = fmt.Sprintf(" a=%d b=%d", r.StatusA, r.StatusB)
			case "differs":
				detail = fmt.Sprintf(", %d differences", r.Count)
			}
			p("\n### %s%s: %s%s", r.Name, mark, r.Outcome, detail)
			p("\n`%s`", cut(r.Request, 300))
			if r.Body != "" {
				p("\nbody: `%s`", r.Body)
			}
			p("\n```\n%s\n```", firstDiffs(r.Diffs, 5))
		}
		if bad > shown {
			p("")
			p("… and %d more, listed in %s/unexplained.txt on the parity host.", bad-shown, in.RunDir)
		}
	}
	if c.JavaFailed > 0 {
		p("")
		p("## Production failed, go worked")
		p("")
		p("Java answered these with a 5xx, also when sent again; go answered them. They do not count against a clean night, but go's answers are not checked against anything.")
		var rs []Result
		for _, r := range in.Results {
			if r.Outcome == "java-failed" {
				rs = append(rs, r)
			}
		}
		for _, r := range rs[:min(shown, len(rs))] {
			p("\n### %s: a=%d b=%d", r.Name, r.StatusA, r.StatusB)
			p("\n`%s`", cut(r.Request, 300))
			if r.Body != "" {
				p("\nbody: `%s`", r.Body)
			}
		}
		if c.JavaFailed > shown {
			p("")
			p("… and %d more, in %s/results.json on the parity host.", c.JavaFailed-shown, in.RunDir)
		}
	}
	if c.JavaDiffers > 0 {
		p("")
		p("## Java nondeterministic")
		p("")
		p("B answered these differently on its two runs (b, b'); \"also A\" marks those where A differed from B as well.")
		var rs []Result
		for _, r := range in.Results {
			if r.Reference == "differs" {
				rs = append(rs, r)
			}
		}
		slices.SortStableFunc(rs, func(x, y Result) int {
			return boolRank(x.Outcome != "nondeterministic") - boolRank(y.Outcome != "nondeterministic")
		})
		for _, r := range rs[:min(shown, len(rs))] {
			mark := ""
			if r.Outcome == "nondeterministic" {
				mark = " (also A)"
			}
			p("\n### %s%s", r.Name, mark)
			p("\n`%s`", cut(r.Request, 300))
			p("\n```\n%s\n```", firstDiffs(r.ReferenceDiffs, 3))
		}
		if c.JavaDiffers > shown {
			p("")
			p("… and %d more, in %s/results.json on the parity host.", c.JavaDiffers-shown, in.RunDir)
		}
	}
	return b.String()
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// firstDiffs joins the first n differences, each cut to 300 characters.
func firstDiffs(diffs []string, n int) string {
	var out []string
	for _, d := range diffs[:min(n, len(diffs))] {
		out = append(out, cut(d, 300))
	}
	return strings.Join(out, "\n")
}

// statusBody is the tracking issue's description: the goal, where it
// stands, and the most recent nights, each linked to its report comment.
func statusBody(repo string, nightLines []string, links map[string]string, streak int, day string) string {
	var b strings.Builder
	b.WriteString("This issue tracks the go/no-go for serving api.cbioportal.org from go: ")
	fmt.Fprintf(&b, "**%d clean nights in a row** of the nightly parity check.\n\n", goal)
	b.WriteString("Each night the check sends a new sample of the previous day's production requests to ")
	b.WriteString("go.cbioportal.org and to api.cbioportal.org and compares the answers. A night is ")
	b.WriteString("clean when every difference is known and explained (a Java bug go fixes, ")
	b.WriteString("Java giving changing answers, or a documented choice). Each night's report is ")
	b.WriteString("a comment below; this description is rewritten after every run.\n\n")
	fmt.Fprintf(&b, "How it works: [docs/rewrite/parity.md](https://github.com/%s/blob/main/docs/rewrite/parity.md).\n\n", repo)
	fmt.Fprintf(&b, "## Status (%s)\n\nClean nights in a row: **%d of %d**\n\n", day, streak, goal)
	b.WriteString("| Night | Result | Report |\n|---|---|---|\n")
	recent := nightLines
	if len(recent) > 14 {
		recent = recent[len(recent)-14:]
	}
	for i := len(recent) - 1; i >= 0; i-- {
		d, result, _ := strings.Cut(recent[i], " ")
		mark := "unexplained differences"
		if result == "clean" {
			mark = "clean"
		}
		link := "—"
		if u := links[d]; u != "" {
			link = "[report](" + u + ")"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", d, mark, link)
	}
	return b.String()
}

// issueAction is what the night does with the tracking issue after
// posting its report.
type issueAction int

const (
	issueKeep  issueAction = iota // the goal is not reached: keep the issue open
	issueClose                    // the goal is reached: say so and close it
)

func decideIssue(streak int) issueAction {
	if streak >= goal {
		return issueClose
	}
	return issueKeep
}
