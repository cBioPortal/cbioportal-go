package parity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	issueTitle = "Parity with Java: 7 clean nights before switching api.cbioportal.org"
	issueLabel = "parity"
)

// runOptions configure one nightly comparison (the run subcommand).
type runOptions struct {
	root           string // repository root
	dir, state     string // PARITY_DIR, PARITY_STATE
	a, b, rate, ua string
	issueRepo      string
	issue          string // PARITY_ISSUE
	memoryMax      string
	memoryAS       string // address-space cap where no systemd scope can be had
	goMemLimit     string
	day            string // the night recorded, YYYY-MM-DD (default: today, UTC)
	dry            bool
	stdout, stderr io.Writer
}

// errRun marks a failure of the check itself (exit status 2).
type errRun struct{ msg string }

func (e errRun) Error() string { return e.msg }

func die(format string, args ...any) error { return errRun{fmt.Sprintf(format, args...)} }

// nightlyRun replays the corpus against A and B, writes the report and
// keeps the alert issue up to date. It returns whether the night was clean.
func nightlyRun(o runOptions) (clean bool, err error) {
	day := o.day
	if day == "" {
		day = time.Now().UTC().Format(time.DateOnly)
	}
	night, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return false, die("bad day %q: want YYYY-MM-DD", day)
	}
	run := filepath.Join(o.dir, "runs", day)
	corpusPath := filepath.Join(o.dir, "corpus.txt")
	if err := os.MkdirAll(filepath.Join(o.dir, "runs"), 0o755); err != nil {
		return false, die("cannot create %s: %v", o.dir, err)
	}
	if err := os.MkdirAll(o.state, 0o755); err != nil {
		return false, die("cannot create %s: %v", o.state, err)
	}

	if err := writeCorpus(o.root, corpusPath, night, o.stderr); err != nil {
		return false, die("corpus generation failed: %v", err)
	}
	bin := filepath.Join(o.dir, "bin")
	for _, tool := range []string{"loggedgen", "paritydiff"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(bin, tool), "./rewrite/java-v7-to-go/"+tool)
		cmd.Dir, cmd.Stdout, cmd.Stderr = o.root, o.stderr, o.stderr
		if err := cmd.Run(); err != nil {
			return false, die("building %s failed: %v", tool, err)
		}
	}
	// Expectations added since the corpus was written apply to it as well.
	if err := annotate(filepath.Join(bin, "loggedgen"), corpusPath, o.stderr); err != nil {
		return false, die("annotating the corpus failed: %v", err)
	}
	pruneRuns(filepath.Join(o.dir, "runs"), 15*24*time.Hour)
	os.RemoveAll(run)
	if err := os.MkdirAll(run, 0o755); err != nil {
		return false, die("cannot create %s: %v", run, err)
	}

	capPrefix, capHow, err := memoryCap(o.memoryMax, o.memoryAS)
	if err != nil {
		return false, die("refusing to run paritydiff without a hard memory cap: %v", err)
	}
	fmt.Fprintf(o.stderr, "paritydiff memory cap: %s\n", capHow)
	versionA, versionB := version(o.a, o.ua), version(o.b, o.ua)
	start := time.Now()
	runParitydiff(o, capPrefix, filepath.Join(bin, "paritydiff"), corpusPath, run)
	minutes := int((time.Since(start) + time.Minute - time.Second) / time.Minute)

	data, err := os.ReadFile(filepath.Join(run, "results.json"))
	if err != nil || len(data) == 0 {
		return false, die("paritydiff wrote no results, see %s/output.txt", run)
	}
	var results []Result
	if err := json.Unmarshal(data, &results); err != nil {
		return false, die("reading %s/results.json: %v", run, err)
	}
	c := count(results)
	bad := c.Bad()

	unexplained := unexplainedNames(results)
	if err := writeLines(filepath.Join(run, "unexplained.txt"), unexplained); err != nil {
		return false, die("%v", err)
	}
	lastPath := filepath.Join(o.dir, "last-unexplained.txt")
	newNames := newSince(readLines(lastPath), unexplained)
	if err := writeLines(filepath.Join(run, "new.txt"), newNames); err != nil {
		return false, die("%v", err)
	}
	nightLines, streak := nights(readLines(filepath.Join(o.state, "nights.txt")), day, bad == 0)
	if err := writeLines(filepath.Join(run, "nights.txt"), nightLines); err != nil {
		return false, die("%v", err)
	}

	corpus, _ := os.ReadFile(corpusPath)
	corpusDate := "?"
	if st, err := os.Stat(corpusPath); err == nil {
		corpusDate = st.ModTime().UTC().Format(time.DateOnly)
	}
	text := report(reportInput{
		Day: day, A: o.a, B: o.b, VersionA: versionA, VersionB: versionB,
		Rate: o.rate, Minutes: minutes, CorpusDate: corpusDate, Corpus: string(corpus),
		Results: results, New: newNames, Streak: streak, RunDir: run,
	})
	if err := os.WriteFile(filepath.Join(run, "report.md"), []byte(text), 0o644); err != nil {
		return false, die("%v", err)
	}
	fmt.Fprint(o.stdout, text)

	if err := trackIssue(o, run, text, day, c, nightLines, streak); err != nil {
		fmt.Fprintf(o.stderr, "nightly: issue: %v\n", err)
	}
	if !o.dry {
		copyFile(filepath.Join(run, "unexplained.txt"), lastPath)
		copyFile(filepath.Join(run, "nights.txt"), filepath.Join(o.state, "nights.txt"))
	}
	return bad == 0, nil
}

// trackIssue posts the night's report as a comment on the open tracking
// issue (labelled issueLabel, opened when there is none), rewrites the
// issue's description with the goal and status, and closes it once the
// goal of clean nights in a row is reached.
func trackIssue(o runOptions, run, text, day string, c Counts, nightLines []string, streak int) error {
	gh := func(args ...string) (string, error) {
		if o.dry {
			fmt.Fprintf(o.stdout, "[dry-run] gh %s\n", strings.Join(args, " "))
			return "", nil
		}
		var out strings.Builder
		cmd := exec.Command("gh", args...)
		cmd.Stdout, cmd.Stderr = io.MultiWriter(&out, o.stdout), o.stderr
		err := cmd.Run()
		return strings.TrimSpace(out.String()), err
	}
	linksPath := filepath.Join(o.state, "night-links.txt")
	links := readLinks(linksPath)
	status := filepath.Join(run, "status.md")
	writeStatus := func() error {
		return os.WriteFile(status, []byte(statusBody(o.issueRepo, nightLines, links, streak, day)), 0o644)
	}

	issue := o.issue
	if issue == "" {
		issue = openIssue(o.issueRepo)
	}
	if issue == "" {
		labels, _ := exec.Command("gh", "label", "list", "-R", o.issueRepo, "--search", issueLabel,
			"--json", "name", "-q", ".[].name").Output()
		if !hasLine(string(labels), issueLabel) {
			if _, err := gh("label", "create", issueLabel, "-R", o.issueRepo, "--color", "FBCA04",
				"--description", "API parity between go.cbioportal.org and production"); err != nil {
				return err
			}
		}
		if err := writeStatus(); err != nil {
			return err
		}
		url, err := gh("issue", "create", "-R", o.issueRepo, "--title", issueTitle, "--label", issueLabel, "--body-file", status)
		if err != nil {
			return err
		}
		issue = url[strings.LastIndex(url, "/")+1:]
		fmt.Fprintf(o.stdout, "opened issue #%s\n", issue)
	}

	body := filepath.Join(run, "issue.md")
	if len(text) > 60000 {
		text = text[:60000]
	}
	if err := os.WriteFile(body, []byte(text), 0o644); err != nil {
		return err
	}
	url, err := gh("issue", "comment", issue, "-R", o.issueRepo, "--body-file", body)
	if err != nil {
		return err
	}
	if url != "" {
		links[day] = url
		var lines []string
		for d, u := range links {
			lines = append(lines, d+" "+u)
		}
		slices.Sort(lines)
		if !o.dry {
			writeLines(linksPath, lines)
		}
	}
	if err := writeStatus(); err != nil {
		return err
	}
	if _, err := gh("issue", "edit", issue, "-R", o.issueRepo, "--title", issueTitle, "--body-file", status); err != nil {
		return err
	}
	if decideIssue(streak) == issueClose {
		closing := filepath.Join(run, "close.md")
		msg := fmt.Sprintf("Goal reached on %s: %d clean nights in a row (%d identical, %d known differences, %d Java nondeterministic of %d requests tonight).\n",
			day, streak, c.Identical, c.Known, c.Nondeterministic, c.All)
		if err := os.WriteFile(closing, []byte(msg), 0o644); err != nil {
			return err
		}
		if _, err := gh("issue", "comment", issue, "-R", o.issueRepo, "--body-file", closing); err != nil {
			return err
		}
		_, err := gh("issue", "close", issue, "-R", o.issueRepo)
		return err
	}
	return nil
}

// readLinks reads night-links.txt: one "<date> <report comment URL>" line
// per night.
func readLinks(path string) map[string]string {
	links := map[string]string{}
	for _, l := range readLines(path) {
		if d, u, ok := strings.Cut(l, " "); ok {
			links[d] = u
		}
	}
	return links
}

// openIssue is the number of the open tracking issue, or "" when there is
// none.
func openIssue(repo string) string {
	out, _ := exec.Command("gh", "issue", "list", "-R", repo, "--state", "open", "--label", issueLabel,
		"--json", "number", "-q", ".[].number").Output()
	issue, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return issue
}

// refreshStatus rewrites the tracking issue's description from nights.txt
// as it is now, for when a night's record was changed after its run.
func refreshStatus(o runOptions) error {
	nightLines := readLines(filepath.Join(o.state, "nights.txt"))
	if len(nightLines) == 0 {
		return die("no nights recorded in %s", o.state)
	}
	slices.Sort(nightLines)
	day, _, _ := strings.Cut(nightLines[len(nightLines)-1], " ")
	body := statusBody(o.issueRepo, nightLines, readLinks(filepath.Join(o.state, "night-links.txt")), streakTo(nightLines, day), day)
	if o.dry {
		fmt.Fprint(o.stdout, body)
		return nil
	}
	issue := o.issue
	if issue == "" {
		issue = openIssue(o.issueRepo)
	}
	if issue == "" {
		return die("no open issue labelled %s in %s", issueLabel, o.issueRepo)
	}
	cmd := exec.Command("gh", "issue", "edit", issue, "-R", o.issueRepo, "--body-file", "-")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(body), o.stdout, o.stderr
	return cmd.Run()
}

// runParitydiff replays the corpus, B twice, under the memory cap of
// capPrefix (memoryCap), so a runaway comparison ends only itself.
func runParitydiff(o runOptions, capPrefix []string, bin, corpus, run string) {
	args := append(append([]string{}, capPrefix...), bin, "-corpus", corpus, "-a", o.a, "-b", o.b, "-ua", o.ua, "-rate", o.rate,
		"-cache", "", "-retry", "2m", "-self-check", "-q", "-json", filepath.Join(run, "results.json"),
		"-dump", filepath.Join(run, "dump"), "-dump-unexplained", "-dump-max", "1000000",
		"-spool", filepath.Join(run, "spool"))
	if err := os.MkdirAll(filepath.Join(run, "spool"), 0o755); err != nil {
		return
	}
	defer os.RemoveAll(filepath.Join(run, "spool"))
	out, err := os.Create(filepath.Join(run, "output.txt"))
	if err != nil {
		return
	}
	defer out.Close()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "GOMEMLIMIT="+o.goMemLimit)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Run()
}

// annotate marks the corpus's requests with the known differences, in place.
func annotate(loggedgen, corpus string, stderr io.Writer) error {
	in, err := os.Open(corpus)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(corpus), filepath.Base(corpus)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	cmd := exec.Command(loggedgen, "-annotate")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, tmp, stderr
	err = cmd.Run()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), corpus)
}

// version describes a deployment from its /api/info.
func version(origin, ua string) string {
	req, err := http.NewRequest(http.MethodGet, origin+"/api/info", nil)
	if err != nil {
		return "unreachable"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return "unreachable"
	}
	defer resp.Body.Close()
	var info struct {
		PortalVersion       *string `json:"portalVersion"`
		GitCommitID         *string `json:"gitCommitId"`
		DBVersion           *string `json:"dbVersion"`
		DerivedTableVersion *string `json:"derivedTableVersion"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "unreachable"
	}
	return describeVersion(info.PortalVersion, info.GitCommitID, info.DBVersion, info.DerivedTableVersion)
}

func describeVersion(portal, commit, db, derived *string) string {
	parts := []string{deref(portal)}
	if c := deref(commit); c != "" && c != "not set" {
		parts = append(parts, "commit "+cut(c, 12))
	}
	if d := deref(db); d != "" {
		parts = append(parts, "db "+d)
	}
	if d := deref(derived); d != "" {
		parts = append(parts, "derived tables "+d)
	}
	return strings.Join(parts, ", ")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// pruneRuns removes run directories older than maxAge, and any response
// cache left in a run directory.
func pruneRuns(runs string, maxAge time.Duration) {
	entries, _ := os.ReadDir(runs)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(runs, e.Name())
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) >= maxAge {
			os.RemoveAll(p)
			continue
		}
		os.RemoveAll(filepath.Join(p, "cache"))
	}
}

func hasLine(text, line string) bool {
	for l := range strings.SplitSeq(text, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		if s.Text() != "" {
			out = append(out, s.Text())
		}
	}
	return out
}

func writeLines(path string, lines []string) error {
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644)
}

// repoRoot is the nearest directory from dir upwards with a go.mod.
func repoRoot(dir string) (string, error) {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", errors.New("no go.mod above " + dir + "; pass -root")
		}
		d = parent
	}
}
