package main

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/studyimport"
)

//go:embed wizard.html
var wizardPage []byte

// importStatus is what the last import of each study folder did, for the
// wizard page, with the page's actions.
type importStatus struct {
	mu      sync.Mutex
	studies map[string]*studyStatus // by folder

	// addFolder asks for a study folder with the system's folder picker
	// and imports it; addPublic downloads public studies and imports them;
	// quit stops the program.
	addFolder func() error
	addPublic func(ids []string) error
	quit      func()
	catalog   *publicCatalog
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

func newImportStatus() *importStatus {
	return &importStatus{studies: map[string]*studyStatus{}, catalog: &publicCatalog{}}
}

// add starts listing the folders of dirs it does not list yet, with
// status, and returns them.
func (s *importStatus) add(dirs []string, status string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var added []string
	for _, d := range dirs {
		if s.studies[d] == nil {
			id, _ := studyimport.StudyID(d)
			s.studies[d] = &studyStatus{Dir: d, StudyID: id, Status: status}
			added = append(added, d)
		}
	}
	return added
}

// set changes the status of one listed folder (or lists it), keeping the
// last import's details.
func (s *importStatus) set(dir, studyID, status, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.studies[dir]
	if st == nil {
		st = &studyStatus{Dir: dir}
		s.studies[dir] = st
	}
	if studyID != "" {
		st.StudyID = studyID
	}
	st.Status, st.Error = status, errText
}

// dirs are the study folders it lists.
func (s *importStatus) dirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.studies))
	for d := range s.studies {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func (s *importStatus) start(dirs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range dirs {
		if st := s.studies[d]; st != nil {
			st.Status = "importing"
		}
	}
}

func (s *importStatus) done(dirs []string, res *studyimport.Result, err error, took time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// handler serves the wizard at /local and its endpoints under /local/, and
// everything else from next. Only someone at this computer may change
// anything: open its folder picker, download studies, or stop it.
func (s *importStatus) handler(next http.Handler) http.Handler {
	local := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPost || !fromThisComputer(r) {
			http.Error(w, "this works only on the computer running the portal", http.StatusForbidden)
			return false
		}
		return true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/local", "/local/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(wizardPage)
		case "/local/status":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s.list())
		case "/local/public-studies":
			studies, err := s.catalog.list()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(studies)
		case "/local/add":
			if !local(w, r) {
				return
			}
			if err := s.addFolder(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/local/public":
			if !local(w, r) {
				return
			}
			var body struct {
				StudyIDs []string `json:"studyIds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := s.addPublic(body.StudyIDs); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "/local/quit":
			if !local(w, r) {
				return
			}
			w.WriteHeader(http.StatusNoContent)
			go s.quit()
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// fromThisComputer reports whether r came from the computer the portal
// runs on.
func fromThisComputer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
