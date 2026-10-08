package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// Study-level authorization under authenticate=oauth2 (and optional_oauth2
// with security.method_authorization_enabled=true), following the Java
// portal's CancerStudyPermissionEvaluator:
//
//   - A user's authorities are their roles, uppercased; with
//     filter_groups_by_appname only roles prefixed "<app.name>:" count (the
//     prefix removed). always_show_study_group is an authority of everyone.
//   - A study is readable with authority ALL; ALL_TCGA (ids ending _TCGA);
//     ALL_NCI_TARGET (ids ending _TARGET); any of the study's groups
//     (cancer_study.groups, ';'-separated); or the study id itself.
//   - Requests naming an unreadable study — in the path, or in a POST body
//     by study, molecular profile or sample list id — get 403; ids that are
//     not studies, profiles or lists (virtual studies) pass, as in Java.
//   - Study, profile, sample list, patient, sample and clinical attribute
//     lists leave unreadable studies out; GET /api/studies keeps them when
//     skin.home_page.show_unauthorized_studies is on, with readPermission
//     false.

type accessPolicy struct {
	appName          string
	filterByAppName  bool
	alwaysShowGroup  string
	showUnauthorized bool
	// anonymous applies the policy to requests without a user too, with
	// always_show_study_group as their only authority (optional_oauth2 with
	// security.method_authorization_enabled).
	anonymous bool
}

func newAccessPolicy(props map[string]string) accessPolicy {
	flag := func(k string, def bool) bool {
		v := strings.TrimSpace(props[k])
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		return err == nil && b
	}
	return accessPolicy{
		appName:          strings.TrimSpace(props["app.name"]),
		filterByAppName:  flag("filter_groups_by_appname", true),
		alwaysShowGroup:  strings.TrimSpace(props["always_show_study_group"]),
		showUnauthorized: flag("skin.home_page.show_unauthorized_studies", false),
	}
}

// authorities are the user's study-access groups, uppercased.
func (p accessPolicy) authorities(u core.User) map[string]bool {
	app := strings.ToUpper(p.appName)
	if app == "" {
		app = "PUBLIC_PORTAL"
	}
	out := map[string]bool{}
	for _, role := range u.Roles {
		role = strings.TrimPrefix(role, "ROLE_")
		up := strings.ToUpper(role)
		if p.filterByAppName {
			rest, ok := strings.CutPrefix(up, app+":")
			if !ok {
				continue
			}
			up = rest
		}
		if up != "" {
			out[up] = true
		}
	}
	if p.alwaysShowGroup != "" {
		out[strings.ToUpper(p.alwaysShowGroup)] = true
	}
	return out
}

// canRead reports whether authorities grant the study with the given groups.
func canRead(auth map[string]bool, studyID, groups string) bool {
	id := strings.ToUpper(studyID)
	switch {
	case id == "ALL", auth["ALL"]:
		return true
	case auth["ALL_TCGA"] && strings.HasSuffix(id, "_TCGA"):
		return true
	case auth["ALL_NCI_TARGET"] && (strings.HasSuffix(id, "_TARGET") || id == "ALL_TARGET_PHASE1" || id == "ALL_TARGET_PHASE2"):
		return true
	}
	for _, g := range strings.Split(groups, ";") {
		if g = strings.ToUpper(strings.TrimSpace(g)); g != "" && auth[g] {
			return true
		}
	}
	return auth[id]
}

// studyIndex maps studies, molecular profiles and sample lists to their
// study, with each study's groups.
type studyIndex struct {
	groups   map[string]string // study id -> groups
	profiles map[string]string // profile id -> study id
	lists    map[string]string // sample list id -> study id
}

func (s *State) studyIndex() (studyIndex, error) {
	type pair struct {
		K string `json:"k"`
		V string `json:"v"`
	}
	idx := studyIndex{map[string]string{}, map[string]string{}, map[string]string{}}
	for _, q := range []struct {
		dst map[string]string
		sql string
	}{
		{idx.groups, "SELECT study_id AS k, ifNull(groups, '') AS v FROM cancer_study"},
		{idx.profiles, "SELECT molecular_profile_id AS k, any(study_id) AS v FROM molecular_profile GROUP BY k"},
		{idx.lists, "SELECT list_id AS k, any(study_id) AS v FROM " + core.AcrossStudies.Rows(core.ParamsBinder{}, core.TSampleList) + " GROUP BY k"},
	} {
		rows, err := core.CachedQuery[pair](s.State, q.sql)
		if err != nil {
			return idx, err
		}
		for _, r := range rows {
			q.dst[r.K] = r.V
		}
	}
	return idx, nil
}

// readable reports whether the user may read every study the ids name.
func (idx studyIndex) readable(auth map[string]bool, studies, profiles, lists []string) bool {
	check := func(study string) bool {
		groups, ok := idx.groups[study]
		return !ok || canRead(auth, study, groups)
	}
	for _, id := range studies {
		if !check(id) {
			return false
		}
	}
	for _, id := range profiles {
		if st, ok := idx.profiles[id]; ok && !check(st) {
			return false
		}
	}
	for _, id := range lists {
		if st, ok := idx.lists[id]; ok && !check(st) {
			return false
		}
	}
	return true
}

// filteredLists are the GET list endpoints that leave out unreadable studies.
var filteredLists = map[string]bool{
	"/api/studies": true, "/api/molecular-profiles": true, "/api/sample-lists": true,
	"/api/patients": true, "/api/samples": true, "/api/clinical-attributes": true,
}

// studyAccess enforces the policy on API requests of logged-in users.
func (s *State) studyAccess(policy accessPolicy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := core.GetUser(r.Context())
		if (!ok && !policy.anonymous) || !core.IsAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		idx, err := s.studyIndex()
		if err != nil {
			core.WriteError(w, http.StatusInternalServerError, "could not check study permissions")
			return
		}
		auth := policy.authorities(user)
		studies, profiles, lists := pathTargets(r)
		if r.Method == http.MethodPost {
			bs, bp, bl := bodyTargets(r.URL.Path, core.BufferedBody(r))
			studies, profiles, lists = append(studies, bs...), append(profiles, bp...), append(lists, bl...)
		}
		if !idx.readable(auth, studies, profiles, lists) {
			core.ReadBody(r, nil)
			core.WriteError(w, http.StatusForbidden, "Access to the specified resource has been forbidden")
			return
		}
		if r.Method != http.MethodGet || !filteredLists[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		keepAll := r.URL.Path == "/api/studies" && policy.showUnauthorized
		f := studyFilter{readable: func(study string) bool {
			groups, ok := idx.groups[study]
			return !ok || canRead(auth, study, groups)
		}, keepAll: keepAll, studies: r.URL.Path == "/api/studies"}
		// A successful array is filtered element by element as it is
		// written, unless its total-count header must count the kept records.
		var bw *bufio.Writer
		rec := core.NewElementWriter(func(h http.Header) bool {
			if h.Get("total-count") != "" {
				return false
			}
			maps.Copy(w.Header(), h)
			w.Header().Del("Content-Length")
			w.WriteHeader(http.StatusOK)
			bw = bufio.NewWriterSize(w, 64<<10)
			bw.WriteByte('[')
			return true
		}, func(el []byte) error {
			f.record(bw, el)
			_, err := bw.Write(nil) // the error of an earlier write, if any
			return err
		})
		next.ServeHTTP(rec, r)
		if rec.Finish() {
			bw.WriteByte(']')
			bw.Flush()
			return
		}
		status := rec.Status
		body := rec.Raw.Bytes()
		if status == http.StatusOK {
			body = filterByStudy(body, f.readable, keepAll, f.studies, rec.Header())
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(status)
		w.Write(body)
	})
}

// pathTargets are the study, profile and sample list ids in an API path.
func pathTargets(r *http.Request) (studies, profiles, lists []string) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	if len(parts) < 2 || parts[1] == "" {
		return
	}
	id := parts[1]
	switch parts[0] {
	case "studies":
		if r.Method == http.MethodPost && (id == "fetch" || id == "tags") {
			return
		}
		studies = append(studies, id)
	case "molecular-profiles":
		if id != "fetch" {
			profiles = append(profiles, id)
		}
	case "sample-lists":
		if id != "fetch" {
			lists = append(lists, id)
		}
	case "export":
		if len(parts) > 2 && parts[1] == "study" {
			studies = append(studies, strings.TrimSuffix(parts[2], ".zip"))
		}
	}
	return
}

// bodyTargets collects the study, profile and sample list ids a POST body
// names, at any depth (filters nest them).
func bodyTargets(path string, body []byte) (studies, profiles, lists []string) {
	if len(bytes.TrimSpace(body)) == 0 {
		return
	}
	var v any
	if json.Unmarshal(body, &v) != nil {
		return
	}
	if arr, ok := v.([]any); ok && (path == "/api/studies/fetch" || path == "/api/studies/tags/fetch") {
		for _, x := range arr {
			if s, ok := x.(string); ok {
				studies = append(studies, s)
			}
		}
		return
	}
	var walk func(any)
	walk = func(n any) {
		switch n := n.(type) {
		case []any:
			for _, x := range n {
				walk(x)
			}
		case map[string]any:
			for k, x := range n {
				switch k {
				case "studyId", "studyIds":
					studies = appendStrings(studies, x)
				case "molecularProfileId", "molecularProfileIds":
					profiles = appendStrings(profiles, x)
				case "sampleListId", "sampleListIds":
					lists = appendStrings(lists, x)
				default:
					walk(x)
				}
			}
		}
	}
	walk(v)
	return
}

func appendStrings(dst []string, v any) []string {
	switch v := v.(type) {
	case string:
		return append(dst, v)
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				dst = append(dst, s)
			}
		}
	}
	return dst
}

// filterByStudy drops the records of unreadable studies from a JSON array,
// or with keepAll marks them readPermission false (study lists).
// Records keep their bytes, so field order is unchanged.
func filterByStudy(body []byte, readable func(string) bool, keepAll, studies bool, header http.Header) []byte {
	var records []json.RawMessage
	if json.Unmarshal(body, &records) != nil {
		return body
	}
	f := studyFilter{readable: readable, keepAll: keepAll, studies: studies}
	var out bytes.Buffer
	out.WriteByte('[')
	for _, rec := range records {
		f.record(&out, rec)
	}
	out.WriteByte(']')
	if header.Get("total-count") != "" {
		header.Set("total-count", strconv.Itoa(f.n))
	}
	return out.Bytes()
}

// studyFilter writes the records of a list that the user may read.
type studyFilter struct {
	readable func(string) bool
	// keepAll keeps unreadable records too; in study lists (studies) they
	// are marked readPermission false.
	keepAll, studies bool
	n                int // records written
}

// record writes rec to out, after a comma unless it is the first, when it
// is kept.
func (f *studyFilter) record(out core.ByteWriter, rec []byte) {
	var id struct {
		StudyID string `json:"studyId"`
	}
	json.Unmarshal(rec, &id)
	ok := id.StudyID == "" || f.readable(id.StudyID)
	if !ok && !f.keepAll {
		return
	}
	if f.studies && !ok {
		rec = bytes.Replace(rec, []byte(`"readPermission":true`), []byte(`"readPermission":false`), 1)
	}
	if f.n > 0 {
		out.WriteByte(',')
	}
	out.Write(rec)
	f.n++
}
