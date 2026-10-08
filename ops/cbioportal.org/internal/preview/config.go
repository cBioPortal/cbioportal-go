// Package preview builds and removes the databases of cbioportal-go pull
// request previews (pr-<N>.gopreview.cbioportal.org).
package preview

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/job"
)

// Config is the "Preview Configuration" of a pull request description
// (.github/pull_request_template.md):
//
//	preview-data: prod             a clone of production (the default)
//	preview-data: datahub-pr 2372  the studies cBioPortal/datahub#2372 changes
//	preview-data: studies          the studies listed under preview-studies
//	preview-studies:               for datahub-pr: only these of its studies
//	- public/msk_impact_2017
type Config struct {
	Mode      string   // prod, datahub-pr or studies
	Arg       string   // the word after the mode, as written
	DatahubPR int      // for datahub-pr
	Studies   []string // preview-studies, sorted, without duplicates
	Err       string   // why the configuration is invalid ("" when valid)
}

// Modes of a Config.
const (
	ModeProd      = "prod"
	ModeDatahubPR = "datahub-pr"
	ModeStudies   = "studies"
)

var studyPath = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

// StripComments removes HTML comments (the template's explanations), which
// may span lines. The line breaks inside a comment stay, so text after a
// comment that ends on a later line starts that line; an unclosed comment
// hides the rest of the text.
func StripComments(body string) string {
	var out strings.Builder
	for {
		start := strings.Index(body, "<!--")
		if start < 0 {
			out.WriteString(body)
			break
		}
		out.WriteString(body[:start])
		rest := body[start+4:]
		end := strings.Index(rest, "-->")
		if end < 0 {
			out.WriteString(strings.Repeat("\n", strings.Count(rest, "\n")))
			break
		}
		out.WriteString(strings.Repeat("\n", strings.Count(rest[:end], "\n")))
		body = rest[end+3:]
	}
	return out.String()
}

// ParseConfig reads the configuration from a pull request description.
// Configuration lines start in the first column.
func ParseConfig(body string) Config {
	body = StripComments(strings.ReplaceAll(body, "\r", ""))
	data := ""
	for line := range strings.SplitSeq(body, "\n") {
		if v, ok := strings.CutPrefix(line, "preview-data:"); ok {
			if i := strings.IndexByte(v, '#'); i >= 0 {
				v = v[:i]
			}
			data = strings.TrimSpace(v)
			break
		}
	}
	c := Config{Mode: ModeProd}
	c.Studies = job.BodyList(body, "preview-studies")
	slices.Sort(c.Studies)
	c.Studies = slices.Compact(c.Studies)

	fields := strings.Fields(data)
	if len(fields) > 0 {
		c.Mode = fields[0]
	}
	if len(fields) > 1 {
		c.Arg = fields[1]
	}
	arg := c.Arg
	switch c.Mode {
	case ModeProd:
		if arg != "" {
			c.Err = "`preview-data: prod` takes no argument"
		}
	case ModeDatahubPR:
		n, err := strconv.Atoi(arg)
		if err != nil || n < 0 || strings.TrimLeft(arg, "0123456789") != "" {
			c.Err = "`preview-data: datahub-pr` needs a datahub PR number"
		}
		c.DatahubPR = n
	case ModeStudies:
		if len(c.Studies) == 0 {
			c.Err = "`preview-data: studies` needs a `preview-studies:` list"
		}
	default:
		c.Err = fmt.Sprintf("unknown `preview-data: %s` (prod, datahub-pr <N> or studies)", c.Mode)
	}
	if len(fields) > 2 {
		c.Err = fmt.Sprintf("unexpected `%s` in `preview-data: %s`", strings.Join(fields[2:], " "), data)
	}
	for _, p := range c.Studies {
		if !studyPath.MatchString(p) || strings.Contains(p, "..") {
			c.Err = fmt.Sprintf("invalid study path `%s`", p)
		}
	}
	return c
}

// Key identifies the data a build used, as recorded in the setup log: the
// mode, its argument, the study list, and for datahub-pr the datahub PR's
// head commit. A build is redone when the key changes.
func (c Config) Key(datahubHead string) string {
	k := c.Mode
	if c.Arg != "" {
		k += " " + c.Arg
	}
	if c.Mode != ModeProd && len(c.Studies) > 0 {
		k += " [" + strings.Join(c.Studies, ",") + "]"
	}
	if c.Mode == ModeDatahubPR && datahubHead != "" {
		k += " @" + datahubHead
	}
	return k
}

// Databases are the three databases of a preview.
type Databases struct{ DB, Raw, Ref string }

// DatabasesFor names the databases of pull request n's preview.
func DatabasesFor(prefix string, n int) Databases {
	db := prefix + strconv.Itoa(n)
	return Databases{db, db + "_raw", db + "_ref"}
}

// All lists the three names.
func (d Databases) All() []string { return []string{d.DB, d.Raw, d.Ref} }

// PRNumbers returns the pull request numbers that database names belong to
// (<prefix><N>, <prefix><N>_raw, <prefix><N>_ref), ascending.
func PRNumbers(prefix string, names []string) []int {
	re := regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + "([0-9]+)(?:_raw|_ref)?$")
	var out []int
	for _, name := range names {
		if m := re.FindStringSubmatch(name); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// GCDecision is whether garbage collection removes a preview.
type GCDecision struct {
	Remove bool
	Reason string
}

// DecideGC keeps the preview of an open pull request that has the preview
// label, and of one that cannot be read; it removes the others.
func DecideGC(state string, labelled bool, readErr error, label string) GCDecision {
	switch {
	case readErr != nil:
		return GCDecision{false, "cannot read the PR, left alone"}
	case state == "open" && labelled:
		return GCDecision{false, "open with label " + label + ", kept"}
	}
	return GCDecision{true, fmt.Sprintf("%s, label %s: %v; removing", state, label, labelled)}
}
