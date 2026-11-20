package hub

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParsePreviewRequest(t *testing.T) {
	const plus = ModePRStudiesPlusHub
	for _, tt := range []struct {
		body, mode string
		only       []string
		err        bool
	}{
		{"", ModePRStudies, nil, false},
		{"Adds a study.\n", ModePRStudies, nil, false},
		{"hub-preview: pr-studies-plus-hub\n", plus, nil, false},
		{"text\r\nhub-preview: pr-studies-plus-hub # every study\r\n", plus, nil, false},
		{"hub-preview: pr-studies\n", ModePRStudies, nil, false},
		{"hub-preview:\n", ModePRStudies, nil, false},
		{"  hub-preview: pr-studies-plus-hub\n", ModePRStudies, nil, false}, // only from the first column
		{"hub-preview: all\n", ModePRStudies, nil, true},
		{"hub-preview: pr-studies\r\n- public/b\r\n-\tpublic/a/\r\n\r\n- public/zzz\r\n", ModePRStudies, []string{"public/b", "public/a"}, false},
		{"hub-preview: pr-studies-plus-hub\n- `public/b`\ntext\n", plus, []string{"public/b"}, false},
		// The commented example of the pull request template does nothing.
		{"<!--\nhub-preview: pr-studies-plus-hub\n- public/brca_tcga\n-->\nAdds b.\n", ModePRStudies, nil, false},
	} {
		got, err := ParsePreviewRequest(tt.body)
		if got.Mode != tt.mode || !slices.Equal(got.Only, tt.only) || (err != nil) != tt.err {
			t.Errorf("%q: %+v, %v; want %q %q, error %v", tt.body, got, err, tt.mode, tt.only, tt.err)
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
