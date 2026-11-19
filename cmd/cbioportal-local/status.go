package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/studyimport"
)

// importStatus is what the last import of each study folder did, for the
// status page.
type importStatus struct {
	mu      sync.Mutex
	studies map[string]*studyStatus // by folder
	running []string                // folders being imported now
}

type studyStatus struct {
	Dir      string    `json:"dir"`
	StudyID  string    `json:"studyId"`
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
	Imported time.Time `json:"imported"`
	Seconds  float64   `json:"seconds"`
}

func newImportStatus(dirs []string) *importStatus {
	s := &importStatus{studies: map[string]*studyStatus{}}
	for _, d := range dirs {
		s.studies[d] = &studyStatus{Dir: d, Status: "waiting"}
	}
	return s
}

func (s *importStatus) start(dirs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = dirs
	for _, d := range dirs {
		if st := s.studies[d]; st != nil {
			st.Status = "importing"
		}
	}
}

func (s *importStatus) done(dirs []string, res *studyimport.Result, err error, took time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = nil
	now := time.Now()
	for _, d := range dirs {
		st := s.studies[d]
		if st == nil {
			continue
		}
		st.Imported, st.Seconds = now, took.Seconds()
		if err != nil {
			st.Status, st.Error = "failed", err.Error()
		}
	}
	if res == nil {
		return
	}
	for _, r := range res.Studies {
		st := s.studies[r.Dir]
		if st == nil {
			continue
		}
		st.StudyID, st.Status, st.Error, st.Warnings = r.StudyID, string(r.Status), "", r.Warnings
		if r.Err != nil {
			st.Error = r.Err.Error()
		}
	}
}

func (s *importStatus) list() []studyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]studyStatus, 0, len(s.studies))
	for _, st := range s.studies {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

var statusPage = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="3"><title>Local portal</title>
<style>
:root { --bg: #fff; --fg: #1d2330; --muted: #5b6475; --line: #e3e6ec; --ok: #1a7f37; --bad: #b42318; --busy: #9a6700; }
@media (prefers-color-scheme: dark) { :root { --bg: #14171c; --fg: #e6e9ef; --muted: #9aa3b2; --line: #2a2f38; --ok: #4ac26b; --bad: #ff7b72; --busy: #d29922; } }
body { background: var(--bg); color: var(--fg); font: 15px/1.5 system-ui, sans-serif; margin: 0 auto; max-width: 960px; padding: 24px 16px; }
h1 { font-size: 22px; margin: 0 0 4px; } p { color: var(--muted); margin: 0 0 20px; }
table { border-collapse: collapse; width: 100%; } th, td { border-bottom: 1px solid var(--line); padding: 8px 6px; text-align: left; vertical-align: top; }
th { color: var(--muted); font-weight: 600; font-size: 13px; } code { font-size: 13px; word-break: break-all; }
.imported { color: var(--ok); } .failed, .removed { color: var(--bad); } .importing, .waiting { color: var(--busy); }
.err { color: var(--bad); white-space: pre-wrap; font-size: 13px; } .warn { color: var(--busy); font-size: 13px; } a { color: inherit; }
</style></head><body>
<h1>Local portal</h1>
<p>The study folders this portal imports and watches. A study is imported again when its files change. <a href="/">Open the portal</a></p>
<table><tr><th>Study</th><th>Status</th><th>Last import</th><th>Folder</th></tr>
{{range .}}<tr><td>{{if .StudyID}}<a href="/study/summary?id={{.StudyID}}">{{.StudyID}}</a>{{else}}–{{end}}</td>
<td class="{{.Status}}">{{.Status}}</td>
<td>{{if not .Imported.IsZero}}{{.Imported.Format "15:04:05"}} ({{printf "%.0f" .Seconds}} s){{end}}</td>
<td><code>{{.Dir}}</code>{{if .Error}}<div class="err">{{.Error}}</div>{{end}}{{range .Warnings}}<div class="warn">{{.}}</div>{{end}}</td></tr>
{{end}}</table></body></html>`))

// handler serves the status page at /local and its data at /local/status,
// and everything else from next.
func (s *importStatus) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/local", "/local/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			statusPage.Execute(w, s.list())
		case "/local/status":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s.list())
		default:
			next.ServeHTTP(w, r)
		}
	})
}
