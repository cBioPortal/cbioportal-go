package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/studyimport"
)

// fakeValidator is a validateData.py stand-in run by /bin/sh: it prints
// what the study directory's name asks for and exits with its status.
const fakeValidator = `
case "$2" in
  */pass) echo "INFO: data_clinical_sample.txt: Validation of file complete"; exit 0 ;;
  */warn) echo "WARNING: meta_study.txt: Unrecognized field in meta file; value encountered: 'short_name'"
          echo "WARNING: data_cna.txt: lines [4, 38]: Gene symbol not known to the cBioPortal instance."; exit 3 ;;
  */fail) echo "ERROR: data_mutations.txt: line 7: Invalid value; value encountered: 'x'"
          echo "WARNING: meta_study.txt: Unrecognized field in meta file"
          echo "Validation of data failed." >&2; exit 1 ;;
  */broken) echo "Traceback (most recent call last):" >&2; exit 2 ;;
  */slow) exec sleep 5 ;;
esac
`

func newFakeValidator(t *testing.T) *Validator {
	t.Helper()
	script := filepath.Join(t.TempDir(), "validateData.py")
	if err := os.WriteFile(script, []byte(fakeValidator), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Validator{Python: "/bin/sh", Script: script, InfoDir: "/info", Logf: t.Logf}
}

func TestValidate(t *testing.T) {
	v := newFakeValidator(t)
	tests := []struct {
		study          string
		exit           int
		errors, warns  int
		failed, runErr bool
		note           string
	}{
		{"pass", 0, 0, 0, false, false, "validation passed"},
		{"warn", 3, 0, 2, false, false, "validation: 2 warnings"},
		{"fail", 1, 1, 1, true, false, ":x: validation: 1 error, 1 warning"},
		{"broken", 0, 0, 0, false, true, "validation not performed (see logs)"},
	}
	for _, tt := range tests {
		res := v.Validate(context.Background(), "/w/public/"+tt.study)
		if res.Exit != tt.exit || len(res.Errors) != tt.errors || len(res.Warnings) != tt.warns ||
			res.Failed() != tt.failed || (res.Err != nil) != tt.runErr || res.Note() != tt.note {
			t.Errorf("%s: %+v, note %q", tt.study, res, res.Note())
		}
	}
	v.Timeout = 100 * time.Millisecond
	if res := v.Validate(context.Background(), "/w/public/slow"); res.Err == nil || res.Failed() {
		t.Errorf("timeout: %+v", res)
	}
	v.Python = "/nonexistent/python3"
	if res := v.Validate(context.Background(), "/w/public/pass"); res.Err == nil {
		t.Errorf("missing interpreter: %+v", res)
	}
}

func TestWithValidation(t *testing.T) {
	tests := []struct {
		concl, title string
		invalid      int
		want, text   string
	}{
		{"success", "Imported 2 of 2", 0, "success", "Imported 2 of 2"},
		{"success", "Imported 2 of 2", 1, "failure", "Imported 2 of 2; 1 with validation errors"},
		{"neutral", "Imported 1 of 2", 1, "failure", "Imported 1 of 2; 1 with validation errors"},
		{"failure", "Imported 0 of 1", 1, "failure", "Imported 0 of 1; 1 with validation errors"},
		{"skipped", "Superseded by a newer commit", 1, "skipped", "Superseded by a newer commit"},
	}
	for _, tt := range tests {
		if c, text := WithValidation(tt.concl, tt.title, tt.invalid); c != tt.want || text != tt.text {
			t.Errorf("%s/%d: %s %q", tt.concl, tt.invalid, c, text)
		}
	}
}

// TestValidatedSummary runs the fake validator through the hub's run and
// checks the summary lines, the Check Run's text and conclusion.
func TestValidatedSummary(t *testing.T) {
	repo := t.TempDir()
	r := &run{Settings: &Settings{Base: "https://hub.example.org", LogURL: "https://logs"},
		validator: newFakeValidator(t), validations: map[string]Validation{}}
	for _, s := range []string{"public/pass", "public/warn", "public/fail", "reference_data/gene_panels"} {
		dir := filepath.Join(repo, s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(s, "public/") {
			os.WriteFile(filepath.Join(dir, "meta_study.txt"), nil, 0o644)
		}
		r.validate(context.Background(), s, dir)
		r.waitValidation()
	}
	if _, ok := r.validations["reference_data/gene_panels"]; ok || len(r.order) != 3 {
		t.Errorf("only studies are validated: %q", r.order)
	}
	res := &studyimport.Result{Studies: []studyimport.StudyResult{
		{Dir: repo + "/public/pass", StudyID: "pass", Status: studyimport.Imported, Duration: time.Second},
		{Dir: repo + "/public/warn", StudyID: "warn", Status: studyimport.Imported, Duration: time.Second},
		{Dir: repo + "/public/fail", StudyID: "fail", Status: studyimport.Imported, Duration: time.Second},
	}}
	r.addResults(res, repo, false)
	want := strings.Join([]string{
		"- :white_check_mark: [pass](https://hub.example.org/study/summary?id=pass) (1s) · validation passed",
		"- :white_check_mark: [warn](https://hub.example.org/study/summary?id=warn) (1s) · validation: 2 warnings",
		"- :white_check_mark: [fail](https://hub.example.org/study/summary?id=fail) (1s) · :x: validation: 1 error, 1 warning",
		"",
	}, "\n")
	if got := r.summary.String(); got != want {
		t.Errorf("summary:\n%s\nwant:\n%s", got, want)
	}
	if link := FirstStudyLink(r.summary.String()); link != "https://hub.example.org/study/summary?id=pass" {
		t.Errorf("first link %q", link)
	}
	concl, title := Conclusion(r.ok, r.fails)
	if concl, title = WithValidation(concl, title, r.invalid); concl != "failure" || title != "Imported 3 of 3; 1 with validation errors" {
		t.Errorf("conclusion %s %q", concl, title)
	}

	text := ValidationText(r.order, r.validations, r.LogURL)
	wantText := "## Validation\n\n" +
		"### `public/warn`: 2 warnings\n\n```\n" +
		"WARNING: meta_study.txt: Unrecognized field in meta file; value encountered: 'short_name'\n" +
		"WARNING: data_cna.txt: lines [4, 38]: Gene symbol not known to the cBioPortal instance.\n```\n\n" +
		"### `public/fail`: 1 error, 1 warning\n\n```\n" +
		"ERROR: data_mutations.txt: line 7: Invalid value; value encountered: 'x'\n" +
		"WARNING: meta_study.txt: Unrecognized field in meta file\n```\n\n"
	if text != wantText {
		t.Errorf("text:\n%s\nwant:\n%s", text, wantText)
	}

	// A long report is cut to fit the Check Run.
	many := Validation{Exit: 3}
	for i := range 5000 {
		many.Warnings = append(many.Warnings, fmt.Sprintf("WARNING: data_mutations.txt: line %d: something", i))
	}
	text = ValidationText([]string{"public/big"}, map[string]Validation{"public/big": many}, "https://logs")
	if len(text) > 65535 || !strings.HasSuffix(text, "the [import logs](https://logs) have every message.\n") {
		t.Errorf("long text: %d bytes, ends %q", len(text), text[len(text)-80:])
	}
}

func TestWritePortalInfo(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		switch r.URL.Path {
		case "/api/info":
			fmt.Fprint(w, `{"portalVersion":"go-0.1.0"}`)
		case "/api/cancer-types":
			fmt.Fprint(w, `[{"cancerTypeId":"acc","name":"Adrenocortical Carcinoma"}]`)
		case "/api/genes":
			fmt.Fprint(w, `[{"entrezGeneId":7157,"hugoGeneSymbol":"TP53","type":"protein-coding"},{"entrezGeneId":672,"hugoGeneSymbol":"BRCA1","type":"protein-coding"}]`)
		case "/api/genesets":
			fmt.Fprint(w, `[]`)
		case "/api/genesets/version":
			fmt.Fprint(w, `"msigdb_v2025.1.Hs"`)
		case "/api/gene-panels":
			fmt.Fprint(w, `[{"genePanelId":"IMPACT341"},{"genePanelId":"A B"}]`)
		case "/api/gene-panels/IMPACT341":
			fmt.Fprint(w, `{"genePanelId":"IMPACT341","description":"old","genes":[{"entrezGeneId":7157,"hugoGeneSymbol":"TP53"}]}`)
		case "/api/gene-panels/A B":
			fmt.Fprint(w, `{"genePanelId":"A B","description":"spaced","genes":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	panels := t.TempDir()
	os.WriteFile(filepath.Join(panels, "data_gene_panel_impact341.txt"),
		[]byte("stable_id: IMPACT341\ndescription: new\ngene_list:\tTP53\tbrca1\tNOTAGENE\t5728\n"), 0o644)
	os.WriteFile(filepath.Join(panels, "data_gene_panel_new.txt"),
		[]byte("stable_id: NEW_PANEL\ndescription: added\ngene_list: TP53\n"), 0o644)

	dir := filepath.Join(t.TempDir(), "info")
	changed := []string{
		filepath.Join(panels, "data_gene_panel_impact341.txt"),
		filepath.Join(panels, "data_gene_panel_new.txt"),
		filepath.Join(panels, "data_gene_panel_deleted.txt"),
	}
	if err := WritePortalInfo(context.Background(), srv.Client(), srv.URL+"/", dir, changed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(paths, " "), "/api/gene-panels/A%20B") {
		t.Errorf("panel ids are escaped: %q", paths)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read("genesets_version"); got != `"msigdb_v2025.1.Hs"` {
		t.Errorf("genesets_version %s", got)
	}
	if got := read("genesaliases"); got != "[]" {
		t.Errorf("genesaliases %s", got)
	}
	var got []portalPanel
	if err := json.Unmarshal([]byte(read("gene-panels")), &got); err != nil {
		t.Fatal(err)
	}
	want := `[{IMPACT341 new [{7157 TP53} {672 brca1} {5728 }]} {A B spaced []} {NEW_PANEL added [{7157 TP53}]}]`
	if s := fmt.Sprint(got); s != want {
		t.Errorf("panels %s\nwant %s", s, want)
	}

	// Without the pull request's panels, the API's are kept; a failing
	// API is an error.
	if err := WritePortalInfo(context.Background(), srv.Client(), srv.URL, dir, nil); err != nil {
		t.Fatal(err)
	}
	if s := read("gene-panels"); !strings.Contains(s, `"description":"old"`) {
		t.Errorf("API panels %s", s)
	}
	if err := WritePortalInfo(context.Background(), srv.Client(), srv.URL+"/missing", dir, nil); err == nil {
		t.Error("no error for a failing API")
	}
}
