package hub

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/studyimport"
)

func TestPlan(t *testing.T) {
	files := []string{"public/a/data.txt", "public/b/meta_study.txt", "crdc/gdc/c/x.txt", "README.md"}
	studies, panels := Plan(files, "")
	if !slices.Equal(studies, []string{"crdc/gdc/c", "public/a", "public/b"}) || panels {
		t.Errorf("got %q %v", studies, panels)
	}
	body := "Adds b.\r\n\r\nhub-import-only:\r\n- public/b\r\n- public/zzz\r\n\r\n- public/a\r\n"
	studies, panels = Plan(append(files, "reference_data/gene_panels/p.txt"), body)
	if !slices.Equal(studies, []string{"public/b"}) || !panels {
		t.Errorf("narrowed: got %q %v", studies, panels)
	}
}

func TestConclusion(t *testing.T) {
	tests := []struct {
		ok, failed  int
		concl, text string
	}{
		{2, 0, "success", "Imported 2 of 2"},
		{2, 1, "neutral", "Imported 2 of 3"},
		{0, 0, "neutral", "Nothing imported"},
		{0, 3, "failure", "Imported 0 of 3"},
	}
	for _, tt := range tests {
		if c, text := Conclusion(tt.ok, tt.failed); c != tt.concl || text != tt.text {
			t.Errorf("%d/%d: %s %q", tt.ok, tt.failed, c, text)
		}
	}
}

func TestSummary(t *testing.T) {
	repo := "/w/datahub"
	r := &run{Settings: &Settings{Base: "https://hub.example.org"}, repo: repo}
	// Failed studies are collected as they fail (Progress) and listed
	// first; the rest after the transform.
	failed := studyimport.StudyResult{Dir: repo + "/public/bad", StudyID: "bad", Status: studyimport.Failed, Err: errors.New("x")}
	r.failed = append(r.failed, failed)
	res := &studyimport.Result{PanelsLoaded: true, Studies: []studyimport.StudyResult{
		{Dir: repo + "/public/a", StudyID: "a", Status: studyimport.Imported, Duration: 12 * time.Second},
		failed,
		{Dir: repo + "/public/gone", Status: studyimport.Skipped},
	}}
	if p := r.progressSummary(); p != "- :x: `bad` — import failed (see logs); removed from the hub\n" {
		t.Errorf("progress summary %q", p)
	}
	r.ok, r.fails = Tally(res, true)
	r.addFailed()
	r.addResults(res, repo, true)
	want := strings.Join([]string{
		"- :x: `bad` — import failed (see logs); removed from the hub",
		"- :white_check_mark: reference gene panels",
		"- :white_check_mark: [a](https://hub.example.org/study/summary?id=a) (12s)",
		"- :heavy_minus_sign: `public/gone` — no meta_study.txt in the PR, skipped",
		"",
	}, "\n")
	if got := r.summary.String(); got != want {
		t.Errorf("summary:\n%s\nwant:\n%s", got, want)
	}
	if r.ok != 2 || r.fails != 1 {
		t.Errorf("ok %d, failed %d", r.ok, r.fails)
	}
	// The details link of a successful import is the first study, not the
	// panels line.
	if link := FirstStudyLink(r.summary.String()); link != "" {
		t.Errorf("first link %q: the first checked line has no link", link)
	}
	if link := FirstStudyLink("- :x: y\n- :white_check_mark: [a](https://h/study/summary?id=a) (1s)\n"); link != "https://h/study/summary?id=a" {
		t.Errorf("first link %q", link)
	}

	// A failed transform removes the run's studies again.
	r = &run{Settings: &Settings{Base: "https://hub.example.org"}}
	res = &studyimport.Result{TransformErr: errors.New("t"), Studies: []studyimport.StudyResult{
		{Dir: repo + "/public/a", StudyID: "a", Status: studyimport.Removed},
	}}
	r.ok, r.fails = Tally(res, false)
	r.addResults(res, repo, false)
	if got := r.summary.String(); got != "- :x: `a` — building the hub failed (see logs); removed from the hub\n" || r.fails != 1 {
		t.Errorf("removed: %q, %d failed", got, r.fails)
	}
}
