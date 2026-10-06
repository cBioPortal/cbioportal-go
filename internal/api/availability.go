package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// Study availability, following the Java portal's
// org.cbioportal.application.rest.availability:
//
//   - A study is unavailable while its cancer_study.status is not 1
//     (AVAILABLE), i.e. while it is being (re)imported.
//   - With study_availability.enabled=true, GET /api/studies lists it with
//     readPermission false (Java master does so whatever the setting;
//     production v7.1.2 never does, see docs/configuration.md).
//   - With study_availability.enabled=true, an API request whose input (path
//     segment, query parameter or JSON body string, at any depth) names the
//     study, one of its molecular profiles or sample lists, or a unique
//     sample/patient key of it gets 423 Locked. The check runs after
//     authentication and study authorization, so a user without access to
//     the study still gets 401/403.

// studyAvailabilityProperty opts into answering 423 for unavailable studies.
const studyAvailabilityProperty = "study_availability.enabled"

// studyAvailabilityEnabled reports whether study_availability.enabled is on.
func (s *State) studyAvailabilityEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(s.Props[studyAvailabilityProperty]), "true")
}

// unavailableStudyIdentifiers maps every identifier owned by an unavailable
// study (study id, molecular profile and sample list ids) to the study id;
// empty when every study is available.
func (s *State) unavailableStudyIdentifiers() map[string]string {
	if !s.modelHasColumn("cancer_study", "status") {
		return nil
	}
	type row struct {
		Identifier string `json:"identifier"`
		StudyID    string `json:"studyId"`
	}
	const unavailable = "SELECT study_id FROM cancer_study WHERE status != 1"
	rows, err := cachedQuery[row](s,
		"SELECT study_id AS identifier, study_id AS studyId FROM ("+unavailable+") "+
			"UNION ALL SELECT DISTINCT molecular_profile_id AS identifier, study_id AS studyId FROM molecular_profile WHERE study_id IN ("+unavailable+") "+
			"UNION ALL SELECT DISTINCT list_id AS identifier, study_id AS studyId FROM sample_list WHERE study_id IN ("+unavailable+")")
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.Identifier != "" {
			out[r.Identifier] = r.StudyID
		}
	}
	return out
}

// msgStudyUnavailable is the 423 message (GlobalExceptionHandler
// .handleStudyUnavailable).
func msgStudyUnavailable(studyID string) string {
	return "Study " + studyID + " is being updated. Please check back later."
}

// studyAvailability answers 423 for requests that name an unavailable study.
func (s *State) studyAvailability(next http.Handler) http.Handler {
	if !s.studyAvailabilityEnabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAPIPath(r.URL.Path) && !strings.HasPrefix(r.URL.Path, "/export/") {
			next.ServeHTTP(w, r)
			return
		}
		unavailable := s.unavailableStudyIdentifiers()
		if len(unavailable) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		if study := requestNamesUnavailable(r, unavailable); study != "" {
			readBody(r, nil)
			writeError(w, http.StatusLocked, msgStudyUnavailable(study))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// searchKeywordPaths are the endpoints whose `keyword` parameter is a free-text
// search, not an identifier (@SearchKeyword): the study list always lists
// every study.
var searchKeywordPaths = map[string]bool{"/api/studies": true, "/api/studies/meta": true}

// requestNamesUnavailable returns the unavailable study a request names in
// its path, query parameters or JSON body, or "" if none.
func requestNamesUnavailable(r *http.Request, unavailable map[string]string) string {
	for _, seg := range strings.Split(r.URL.Path, "/") {
		if st := matchUnavailable(seg, unavailable); st != "" {
			return st
		}
		if base, ok := strings.CutSuffix(seg, ".zip"); ok {
			if st := matchUnavailable(base, unavailable); st != "" {
				return st
			}
		}
	}
	for k, vs := range r.URL.Query() {
		if k == "keyword" && searchKeywordPaths[r.URL.Path] {
			continue
		}
		for _, v := range vs {
			// A list parameter binds comma-separated values.
			for _, part := range append([]string{v}, strings.Split(v, ",")...) {
				if st := matchUnavailable(part, unavailable); st != "" {
					return st
				}
			}
		}
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return ""
	}
	var body any
	if json.Unmarshal(bufferedBody(r), &body) != nil {
		return ""
	}
	pending := []any{body}
	for len(pending) > 0 {
		n := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch n := n.(type) {
		case string:
			if st := matchUnavailable(n, unavailable); st != "" {
				return st
			}
		case []any:
			pending = append(pending, n...)
		case map[string]any:
			for _, v := range n {
				pending = append(pending, v)
			}
		}
	}
	return ""
}

var base64Key = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// matchUnavailable returns the unavailable study an identifier, or a unique
// sample/patient key base64("<id>:<studyId>") (padding optional), belongs to.
func matchUnavailable(text string, unavailable map[string]string) string {
	if text == "" {
		return ""
	}
	if st, ok := unavailable[text]; ok {
		return st
	}
	if !base64Key.MatchString(text) {
		return ""
	}
	// Like java.util.Base64's decoder: padding optional, but complete if given.
	enc := base64.RawStdEncoding
	if strings.HasSuffix(text, "=") {
		enc = base64.StdEncoding
	}
	decoded, err := enc.DecodeString(text)
	if err != nil {
		return ""
	}
	// String.split drops trailing empty strings.
	parts := strings.Split(string(decoded), ":")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) != 2 {
		return ""
	}
	return unavailable[parts[1]]
}
