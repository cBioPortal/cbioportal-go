package hub

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseMode(t *testing.T) {
	for _, tt := range []struct {
		body, mode string
		err        bool
	}{
		{"", ModeChanged, false},
		{"Adds a study.\n", ModeChanged, false},
		{"hub-preview: all\n", ModeAll, false},
		{"text\r\nhub-preview: all # every study\r\n", ModeAll, false},
		{"hub-preview: changed\n", ModeChanged, false},
		{"hub-preview:\n", ModeChanged, false},
		{"<!-- hub-preview: all -->\n", ModeChanged, false},
		{"  hub-preview: all\n", ModeChanged, false}, // only from the first column
		{"hub-preview: everything\n", ModeChanged, true},
	} {
		mode, err := ParseMode(tt.body)
		if mode != tt.mode || (err != nil) != tt.err {
			t.Errorf("%q: %q, %v; want %q, error %v", tt.body, mode, err, tt.mode, tt.err)
		}
	}
}

func TestDecideSync(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	retry := 30 * time.Minute
	for _, tt := range []struct {
		name  string
		st    SyncState
		head  string
		run   bool
		about string
	}{
		{"first", SyncState{}, "aaaaaaa1", true, "first import"},
		{"up to date", SyncState{Imported: "aaaaaaa1", Last: "aaaaaaa1", At: now}, "aaaaaaa1", false, "up to date"},
		{"moved", SyncState{Imported: "aaaaaaa1", Last: "aaaaaaa1"}, "bbbbbbb2", true, "moved from aaaaaaa to bbbbbbb"},
		{"failed recently", SyncState{Imported: "aaaaaaa1", Last: "bbbbbbb2", Failed: true, At: now.Add(-10 * time.Minute)}, "bbbbbbb2", false, "trying again after"},
		{"failed long ago", SyncState{Imported: "aaaaaaa1", Last: "bbbbbbb2", Failed: true, At: now.Add(-time.Hour)}, "bbbbbbb2", true, "moved"},
		// A newer commit is imported at once, even after a failure.
		{"failed, then a new commit", SyncState{Imported: "aaaaaaa1", Last: "bbbbbbb2", Failed: true, At: now}, "ccccccc3", true, "moved"},
	} {
		d := DecideSync(tt.st, tt.head, now, retry)
		if d.Run != tt.run || !strings.Contains(d.Reason, tt.about) {
			t.Errorf("%s: %+v", tt.name, d)
		}
	}
}

func TestPlanSync(t *testing.T) {
	head := []string{"public/b", "public/c", "public/d"}
	p := PlanSync(head, nil, true)
	if !slices.Equal(p.Import, head) || len(p.Gone) != 0 || !p.Panels {
		t.Errorf("first: %+v", p)
	}
	changed := []string{"public/a/meta_study.txt", "public/b/data_mutations.txt", "public/c/meta_study.txt", "README.md"}
	p = PlanSync(head, changed, false)
	if !slices.Equal(p.Import, []string{"public/b", "public/c"}) || !slices.Equal(p.Gone, []string{"public/a"}) || p.Panels {
		t.Errorf("changed: %+v", p)
	}
	p = PlanSync(head, []string{"reference_data/gene_panels/impact.txt"}, false)
	if len(p.Import) != 0 || len(p.Gone) != 0 || !p.Panels {
		t.Errorf("panels: %+v", p)
	}
}
