package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// cbioportal-core's validateData.py checks each study before it is
// imported. It reads the portal's reference data (cancer types, genes, gene
// sets, gene panels) from a portal-info directory that WritePortalInfo
// fills from the hub's API once per job.

// Validation is the outcome of validating one study.
type Validation struct {
	// Exit is validateData.py's exit status: 0 valid, 1 errors,
	// 3 warnings only (2, "not performed", is reported as Err).
	Exit     int
	Errors   []string // the ERROR and CRITICAL lines
	Warnings []string // the WARNING lines
	Err      error    // the validator could not run
}

// Failed reports whether the study has validation errors.
func (v Validation) Failed() bool { return v.Err == nil && (v.Exit == 1 || len(v.Errors) > 0) }

// Note is the validation part of a study's summary line.
func (v Validation) Note() string {
	switch {
	case v.Err != nil:
		return "validation not performed (see logs)"
	case v.Failed():
		return fmt.Sprintf(":x: validation: %s", counts(len(v.Errors), len(v.Warnings)))
	case len(v.Warnings) > 0:
		return fmt.Sprintf("validation: %s", counts(0, len(v.Warnings)))
	}
	return "validation passed"
}

func counts(errs, warns int) string {
	var parts []string
	if errs > 0 {
		parts = append(parts, plural(errs, "error"))
	}
	if warns > 0 || errs == 0 {
		parts = append(parts, plural(warns, "warning"))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Validator runs validateData.py on study directories.
type Validator struct {
	Python  string // the interpreter, e.g. python3
	Script  string // validateData.py
	InfoDir string // the portal-info directory (validateData.py -p)
	Timeout time.Duration
	Logf    func(format string, args ...any)
}

// Validate validates one study directory. The validator's full output goes
// to the job's log.
func (v *Validator) Validate(ctx context.Context, dir string) Validation {
	if v.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, v.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, v.Python, v.Script, "-s", dir, "-p", v.InfoDir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 10 * time.Second
	start := time.Now()
	err := cmd.Run()
	res := ParseValidation(out.String())
	for line := range strings.SplitSeq(strings.TrimRight(out.String(), "\n"), "\n") {
		if line != "" {
			v.Logf("  validate: %s", line)
		}
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		res.Exit = 0
	case errors.As(err, &exit) && (exit.ExitCode() == 1 || exit.ExitCode() == 3):
		res.Exit = exit.ExitCode()
	case errors.As(err, &exit):
		res.Err = fmt.Errorf("validateData.py exited with status %d", exit.ExitCode())
	default:
		res.Err = err
	}
	if ctx.Err() != nil {
		res.Err = fmt.Errorf("validateData.py: %w", ctx.Err())
	}
	if res.Err != nil {
		v.Logf("  validation not performed: %v", res.Err)
	} else {
		v.Logf("  validated in %.0fs: %s", time.Since(start).Seconds(), counts(len(res.Errors), len(res.Warnings)))
	}
	return res
}

// ParseValidation collects the error and warning messages of
// validateData.py's output.
func ParseValidation(out string) Validation {
	var v Validation
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "ERROR: "), strings.HasPrefix(line, "CRITICAL: "):
			v.Errors = append(v.Errors, line)
		case strings.HasPrefix(line, "WARNING: "):
			v.Warnings = append(v.Warnings, line)
		}
	}
	return v
}

// maxCheckText keeps the Check Run's text under GitHub's 65535-character
// limit.
const maxCheckText = 60000

// ValidationText is the Check Run text with each study's validation
// messages, errors first, cut at maxCheckText.
func ValidationText(studies []string, results map[string]Validation, logURL string) string {
	var b strings.Builder
	for _, s := range studies {
		v, ok := results[s]
		if !ok || (v.Err == nil && len(v.Errors)+len(v.Warnings) == 0) {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("## Validation\n\n")
		}
		if v.Err != nil {
			fmt.Fprintf(&b, "### `%s`: not validated\n\n%s\n\n", s, v.Err)
			continue
		}
		fmt.Fprintf(&b, "### `%s`: %s\n\n```\n", s, counts(len(v.Errors), len(v.Warnings)))
		for _, l := range append(append([]string{}, v.Errors...), v.Warnings...) {
			b.WriteString(strings.ReplaceAll(l, "```", "'''"))
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
	}
	text := b.String()
	if len(text) <= maxCheckText {
		return text
	}
	cut := strings.LastIndexByte(text[:maxCheckText], '\n')
	return text[:cut+1] + fmt.Sprintf("```\n\n… cut short; the [import logs](%s) have every message.\n", logURL)
}

// WritePortalInfo fills dir with the portal information validateData.py
// reads with -p: what it requests from the portal's API with -u, plus
// genesaliases.json, empty as with -u (no API lists the aliases). The gene
// panel files panelFiles, a pull request's changed ones, replace or add to
// the API's panels: the hub loads them only during the import. Files that do
// not exist (deleted by the pull request) are left out.
func WritePortalInfo(ctx context.Context, hc *http.Client, api, dir string, panelFiles []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	get := func(path string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(api, "/")+path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
		}
		return body, err
	}
	write := func(name string, b []byte) error { return os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644) }
	for _, e := range []struct{ name, path string }{
		{"info", "/api/info"}, {"cancer-types", "/api/cancer-types"}, {"genes", "/api/genes"},
		{"genesets", "/api/genesets"}, {"genesets_version", "/api/genesets/version"},
	} {
		b, err := get(e.path)
		if err != nil {
			return err
		}
		if err := write(e.name, b); err != nil {
			return err
		}
	}
	if err := write("genesaliases", []byte("[]")); err != nil {
		return err
	}

	b, err := get("/api/gene-panels")
	if err != nil {
		return err
	}
	var list []struct {
		GenePanelID string `json:"genePanelId"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("/api/gene-panels: %w", err)
	}
	var panels []portalPanel
	for _, p := range list {
		b, err := get("/api/gene-panels/" + url.PathEscape(p.GenePanelID))
		if err != nil {
			return err
		}
		var panel portalPanel
		if err := json.Unmarshal(b, &panel); err != nil {
			return fmt.Errorf("/api/gene-panels/%s: %w", p.GenePanelID, err)
		}
		panels = append(panels, panel)
	}
	if len(panelFiles) > 0 {
		if panels, err = overlayPanels(panels, panelFiles, filepath.Join(dir, "genes.json")); err != nil {
			return err
		}
	}
	if b, err = json.Marshal(panels); err != nil {
		return err
	}
	return write("gene-panels", b)
}

type panelGene struct {
	EntrezGeneID   int    `json:"entrezGeneId"`
	HugoGeneSymbol string `json:"hugoGeneSymbol,omitempty"`
}

type portalPanel struct {
	GenePanelID string      `json:"genePanelId"`
	Description string      `json:"description"`
	Genes       []panelGene `json:"genes"`
}

// overlayPanels replaces or adds the gene panels of files (stable_id,
// description and gene_list lines). gene_list holds symbols or Entrez ids;
// symbols are resolved by their Hugo symbol in genesFile, the API's genes,
// not by alias as the import does, so a panel listing an alias can give
// extra off-panel warnings.
func overlayPanels(panels []portalPanel, files []string, genesFile string) ([]portalPanel, error) {
	b, err := os.ReadFile(genesFile)
	if err != nil {
		return nil, err
	}
	var genes []panelGene
	if err := json.Unmarshal(b, &genes); err != nil {
		return nil, fmt.Errorf("genes.json: %w", err)
	}
	entrez := map[string]int{}
	for _, g := range genes {
		if _, ok := entrez[strings.ToUpper(g.HugoGeneSymbol)]; !ok {
			entrez[strings.ToUpper(g.HugoGeneSymbol)] = g.EntrezGeneID
		}
	}
	index := map[string]int{}
	for i, p := range panels {
		index[p.GenePanelID] = i
	}
	for _, f := range files {
		p, err := readPanelFile(f, entrez)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if p.GenePanelID == "" {
			continue
		}
		if i, ok := index[p.GenePanelID]; ok {
			panels[i] = p
		} else {
			index[p.GenePanelID] = len(panels)
			panels = append(panels, p)
		}
	}
	return panels, nil
}

func readPanelFile(path string, entrez map[string]int) (portalPanel, error) {
	f, err := os.Open(path)
	if err != nil {
		return portalPanel{}, err
	}
	defer f.Close()
	p := portalPanel{Genes: []panelGene{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "stable_id":
			p.GenePanelID = value
		case "description":
			p.Description = value
		case "gene_list":
			for g := range strings.FieldsSeq(value) {
				if id, err := strconv.Atoi(g); err == nil {
					p.Genes = append(p.Genes, panelGene{EntrezGeneID: id})
				} else if id, ok := entrez[strings.ToUpper(g)]; ok {
					p.Genes = append(p.Genes, panelGene{EntrezGeneID: id, HugoGeneSymbol: g})
				}
			}
		}
	}
	return p, sc.Err()
}
