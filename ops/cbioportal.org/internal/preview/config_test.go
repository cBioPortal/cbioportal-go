package preview

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestParseConfigTemplate(t *testing.T) {
	// The PR template as is: the explanation is a comment, the one
	// configuration line follows it.
	body, err := os.ReadFile("../../../../.github/pull_request_template.md")
	if err != nil {
		t.Fatal(err)
	}
	c := ParseConfig(string(body))
	if c.Mode != ModeProd || c.Err != "" || len(c.Studies) != 0 || c.Key("") != "prod" {
		t.Errorf("template: %+v key %q", c, c.Key(""))
	}
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name, body string
		mode       string
		key        string // with datahub head "abc"
		err        string
	}{
		{"empty description", "", "prod", "prod", ""},
		{"no value", "preview-data:\n", "prod", "prod", ""},
		{"prod", "## Summary\r\n\r\npreview-data: prod\r\n", "prod", "prod", ""},
		{"comment after the value", "preview-data: datahub-pr 2372   # the new study\n", "datahub-pr", "datahub-pr 2372 @abc", ""},
		{"first line wins", "preview-data: studies\npreview-data: prod\npreview-studies:\n- public/b\n- public/a\n- public/b\n", "studies", "studies [public/a,public/b]", ""},
		{"narrowed datahub-pr", "preview-data: datahub-pr 7\n\npreview-studies:\n- public/x\n", "datahub-pr", "datahub-pr 7 [public/x] @abc", ""},
		{"list ends at a blank line", "preview-data: studies\npreview-studies:\n- public/a\n\n- public/b\n", "studies", "studies [public/a]", ""},
		{"indented lines are not configuration", "  preview-data: studies\n", "prod", "prod", ""},
		{"explanations in comments", "<!--\npreview-data: studies\n-->\npreview-data: datahub-pr 3\n", "datahub-pr", "datahub-pr 3 @abc", ""},
		{"line after a comment", "x <!-- a\nb --> preview-data: studies\n<!-- c -->preview-data: datahub-pr 4\n", "datahub-pr", "datahub-pr 4 @abc", ""},
		{"unclosed comment hides the rest", "<!-- oops\npreview-data: studies\n", "prod", "prod", ""},
		{"prod with argument", "preview-data: prod 1\n", "prod", "", "takes no argument"},
		{"datahub-pr without number", "preview-data: datahub-pr\n", "datahub-pr", "", "needs a datahub PR number"},
		{"datahub-pr with sign", "preview-data: datahub-pr +5\n", "datahub-pr", "", "needs a datahub PR number"},
		{"studies without list", "preview-data: studies\n", "studies", "", "needs a `preview-studies:` list"},
		{"unknown mode", "preview-data: staging\n", "staging", "", "unknown `preview-data: staging`"},
		{"extra words", "preview-data: datahub-pr 5 now\n", "datahub-pr", "", "unexpected `now`"},
		{"path escaping the checkout", "preview-data: studies\npreview-studies:\n- public/../../etc\n", "studies", "", "invalid study path"},
		{"path with a space", "preview-data: studies\npreview-studies:\n- public/a b\n", "studies", "", "invalid study path"},
	}
	// An invalid configuration is recorded as written, so editing it
	// starts a new build.
	if k := ParseConfig("preview-data: datahub-pr x\n").Key(""); k != "datahub-pr x" {
		t.Errorf("invalid configuration's key %q", k)
	}
	for _, tt := range tests {
		c := ParseConfig(tt.body)
		if c.Mode != tt.mode {
			t.Errorf("%s: mode %q, want %q", tt.name, c.Mode, tt.mode)
		}
		if tt.err != "" {
			if !strings.Contains(c.Err, tt.err) {
				t.Errorf("%s: error %q, want one containing %q", tt.name, c.Err, tt.err)
			}
			continue
		}
		if c.Err != "" {
			t.Errorf("%s: unexpected error %q", tt.name, c.Err)
		}
		if k := c.Key("abc"); k != tt.key {
			t.Errorf("%s: key %q, want %q", tt.name, k, tt.key)
		}
	}
}

func TestDatabases(t *testing.T) {
	d := DatabasesFor("dev_cbioportal_go_pr", 42)
	if !slices.Equal(d.All(), []string{"dev_cbioportal_go_pr42", "dev_cbioportal_go_pr42_raw", "dev_cbioportal_go_pr42_ref"}) {
		t.Errorf("got %q", d.All())
	}
	got := PRNumbers("dev_cbioportal_go_pr", []string{
		"dev_cbioportal_go_pr42_ref", "dev_cbioportal_go_pr42", "dev_cbioportal_go_pr9_raw", "dev_cbioportal_go_pr100",
		"dev_cbioportal_go_pr", "dev_cbioportal_go_pr7_old", "dev_cbioportal_go_prx1", "xdev_cbioportal_go_pr5", "",
	})
	if !slices.Equal(got, []int{9, 42, 100}) {
		t.Errorf("PRNumbers: %v", got)
	}
}

func TestDecideGC(t *testing.T) {
	tests := []struct {
		state    string
		labelled bool
		err      error
		remove   bool
	}{
		{"open", true, nil, false},
		{"open", false, nil, true},
		{"closed", true, nil, true},
		{"closed", false, nil, true},
		{"", false, errors.New("HTTP 502"), false},
	}
	for _, tt := range tests {
		if d := DecideGC(tt.state, tt.labelled, tt.err, "preview"); d.Remove != tt.remove {
			t.Errorf("%s labelled=%v err=%v: %+v", tt.state, tt.labelled, tt.err, d)
		}
	}
}
