package api

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// Session service: an embedded replacement for the external cBioPortal
// session-service that the Java SessionServiceController and
// PublicVirtualStudiesController proxy to. Sessions are kept in memory and,
// when SESSION_STORE_FILE is set, persisted to a JSON file.

// allUsers is the users-entry marking a virtual study as public.
const allUsers = "*"

// sessionSource is the source every session is stored under.
const sessionSource = "cbioportal"

// sessionTypes are the valid {type} path values (Java Session.SessionType).
var sessionTypes = map[string]bool{
	"main_session": true, "virtual_study": true, "group": true, "comparison_session": true,
	"settings": true, "custom_data": true, "genomic_chart": true, "custom_gene_list": true,
}

// Field sets of the Java payload classes; Jackson ignores unknown properties,
// so anything else is dropped on create.
var (
	virtualStudyFields = []string{"name", "description", "studies", "studyViewFilter", "version", "owner",
		"origin", "created", "lastUpdated", "users", "typeOfCancerId", "pmid", "dynamic"}
	customDataFields = []string{"owner", "origin", "created", "lastUpdated", "users", "displayName",
		"description", "datatype", "patientAttribute", "priority", "data"}
	customGeneListFields = []string{"name", "description", "geneList", "users"}
)

// Session is the stored/served session document (Java Session).
type Session struct {
	ID       string         `json:"id"`
	Checksum string         `json:"checksum,omitempty"`
	Data     map[string]any `json:"data"`
	Source   string         `json:"source"`
	Type     string         `json:"type"`
}

func (s *Session) clone() *Session {
	c := *s
	c.Data = deepCopyMap(s.Data)
	return &c
}

// sessionStore is a thread-safe (type,id)-keyed session map, optionally
// mirrored to a JSON file that is rewritten atomically on every change.
type sessionStore struct {
	mu   sync.RWMutex
	path string
	m    map[string]*Session
}

func storeKey(typ, id string) string { return typ + "/" + id }

func newSessionStore(path string) *sessionStore {
	st := &sessionStore{path: path, m: map[string]*Session{}}
	if path == "" {
		return st
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("session store: reading %s: %v", path, err)
		}
		return st
	}
	var list []*Session
	if err := decodeJSON(raw, &list); err != nil {
		log.Printf("session store: parsing %s: %v", path, err)
		return st
	}
	for _, s := range list {
		if s != nil && s.ID != "" && s.Type != "" {
			if s.Data == nil {
				s.Data = map[string]any{}
			}
			st.m[storeKey(s.Type, s.ID)] = s
		}
	}
	return st
}

func newSessionStoreFromEnv() *sessionStore {
	return newSessionStore(os.Getenv("SESSION_STORE_FILE"))
}

// persistLocked writes the store to its file; the caller holds mu.
func (st *sessionStore) persistLocked() {
	if st.path == "" {
		return
	}
	list := make([]*Session, 0, len(st.m))
	for _, s := range st.m {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Type != list[j].Type {
			return list[i].Type < list[j].Type
		}
		return list[i].ID < list[j].ID
	})
	raw, err := json.Marshal(list)
	if err != nil {
		log.Printf("session store: encoding: %v", err)
		return
	}
	dir := filepath.Dir(st.path)
	tmp, err := os.CreateTemp(dir, filepath.Base(st.path)+".tmp-*")
	if err != nil {
		log.Printf("session store: %v", err)
		return
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		log.Printf("session store: writing %s: %v %v", tmp.Name(), werr, cerr)
		return
	}
	if err := os.Rename(tmp.Name(), st.path); err != nil {
		os.Remove(tmp.Name())
		log.Printf("session store: %v", err)
	}
}

func (st *sessionStore) get(typ, id string) (*Session, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	s, ok := st.m[storeKey(typ, id)]
	if !ok {
		return nil, false
	}
	return s.clone(), true
}

// create stores data under a new id, or returns the id of an existing session
// of the same type with identical data (session-service checksum dedup).
func (st *sessionStore) create(typ string, data map[string]any) string {
	sum := checksum(data)
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, s := range st.m {
		if s.Type == typ && s.Checksum == sum {
			return s.ID
		}
	}
	id := newObjectID()
	st.m[storeKey(typ, id)] = &Session{ID: id, Checksum: sum, Data: deepCopyMap(data), Source: sessionSource, Type: typ}
	st.persistLocked()
	return id
}

// insert stores data under the given id; it fails when the id is taken or a
// session of the same type with identical data exists.
func (st *sessionStore) insert(typ, id string, data map[string]any) bool {
	sum := checksum(data)
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.m[storeKey(typ, id)]; ok {
		return false
	}
	for _, s := range st.m {
		if s.Type == typ && s.Checksum == sum {
			return false
		}
	}
	st.m[storeKey(typ, id)] = &Session{ID: id, Checksum: sum, Data: deepCopyMap(data), Source: sessionSource, Type: typ}
	st.persistLocked()
	return true
}

// update replaces a session's data; false when it does not exist.
func (st *sessionStore) update(typ, id string, data map[string]any) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.m[storeKey(typ, id)]
	if !ok {
		return false
	}
	s.Data = deepCopyMap(data)
	s.Checksum = checksum(data)
	st.persistLocked()
	return true
}

func (st *sessionStore) remove(typ, id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.m[storeKey(typ, id)]; !ok {
		return false
	}
	delete(st.m, storeKey(typ, id))
	st.persistLocked()
	return true
}

// list returns copies of all sessions of a type matching keep, ordered by id
// (ObjectIds sort by creation time).
func (st *sessionStore) list(typ string, keep func(*Session) bool) []*Session {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := []*Session{}
	for _, s := range st.m {
		if s.Type == typ && (keep == nil || keep(s)) {
			out = append(out, s.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var objectIDCounter atomic.Uint32

func init() {
	var b [4]byte
	rand.Read(b[:])
	objectIDCounter.Store(binary.BigEndian.Uint32(b[:]))
}

// newObjectID returns a 24-hex-char Mongo-style ObjectId: 4-byte timestamp,
// 5 random bytes, 3-byte counter.
func newObjectID() string {
	var b [12]byte
	binary.BigEndian.PutUint32(b[0:4], uint32(time.Now().Unix()))
	rand.Read(b[4:9])
	c := objectIDCounter.Add(1)
	b[9], b[10], b[11] = byte(c>>16), byte(c>>8), byte(c)
	return hex.EncodeToString(b[:])
}

func checksum(data map[string]any) string {
	raw, _ := json.Marshal(data)
	sum := md5.Sum(raw)
	return hex.EncodeToString(sum[:])
}

// decodeJSON decodes keeping numbers as json.Number so ids and timestamps
// round-trip unchanged.
func decodeJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	raw, _ := json.Marshal(m)
	var out map[string]any
	decodeJSON(raw, &out)
	return out
}

func pickFields(m map[string]any, fields []string) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		if v, ok := m[f]; ok && v != nil {
			out[f] = v
		}
	}
	return out
}

func nowMillis() json.Number { return json.Number(fmt.Sprint(time.Now().UnixMilli())) }

// setDefault sets m[k] when absent or null (Java field initialisers).
func setDefault(m map[string]any, k string, v any) {
	if cur, ok := m[k]; !ok || cur == nil {
		m[k] = v
	}
}

// stringSet reads a JSON array of strings, de-duplicated in input order.
func stringSet(v any) []string {
	arr, _ := v.([]any)
	out := []string{}
	seen := map[string]bool{}
	for _, x := range arr {
		if s, ok := x.(string); ok && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// containsUser matches a users entry case-insensitively.
func containsUser(users []string, name string) bool {
	for _, u := range users {
		if strings.EqualFold(u, name) {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		if !set[x] {
			return false
		}
	}
	return true
}

// sessionUser returns the logged-in user name (Java isAuthorized/userName).
func sessionUser(r *http.Request) (string, bool) {
	u, ok := GetUser(r.Context())
	if !ok || u.Name == "" {
		return "", false
	}
	return u.Name, true
}

// readObject reads the request body as a JSON object.
func readObject(r *http.Request) (map[string]any, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := decodeJSON(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("body must be a JSON object")
	}
	return m, nil
}

func (s *State) store() *sessionStore {
	s.sessionsOnce.Do(func() {
		if s.sessions == nil {
			s.sessions = newSessionStoreFromEnv()
		}
	})
	return s.sessions
}

func (s *State) routeSession(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/session/{type}", s.sessionAdd)
	mux.HandleFunc("POST /api/session/virtual_study/save", s.sessionAddWith("virtual_study", true))
	mux.HandleFunc("POST /api/session/custom_gene_list/save", s.sessionAddWith("custom_gene_list", true))
	mux.HandleFunc("GET /api/session/{type}/{id}", s.sessionGet)
	mux.HandleFunc("GET /api/session/virtual_study", s.sessionUserVirtualStudies)
	mux.HandleFunc("GET /api/session/custom_gene_list", s.sessionUserCustomGeneLists)
	mux.HandleFunc("GET /api/session/{type}/{operation}/{id}", s.sessionUpdateUsers)
	mux.HandleFunc("POST /api/session/groups/fetch", s.sessionFetchByOrigin("group"))
	mux.HandleFunc("POST /api/session/custom_data/fetch", s.sessionFetchByOrigin("custom_data"))
	mux.HandleFunc("POST /api/session/settings", s.sessionUpdateSettings)
	mux.HandleFunc("POST /api/session/settings/fetch", s.sessionFetchSettings)

	mux.HandleFunc("GET /api/public_virtual_studies", s.publicVirtualStudies)
	mux.HandleFunc("POST /api/public_virtual_studies/{id}", s.publishVirtualStudy)
	mux.HandleFunc("DELETE /api/public_virtual_studies/{id}", s.unpublishVirtualStudy)
}

// ---- create ----

func (s *State) sessionAdd(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	if !sessionTypes[typ] {
		readBody(r, nil)
		writeError(w, http.StatusBadRequest, msgEnumMismatch(sessionTypeClass, typ))
		return
	}
	s.addSession(w, r, typ, false)
}

func (s *State) sessionAddWith(typ string, save bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.addSession(w, r, typ, save) }
}

// addSession applies the per-type owner/users rules of the Java controller and
// stores the payload, answering {"id": ...}.
func (s *State) addSession(w http.ResponseWriter, r *http.Request, typ string, save bool) {
	body, err := readObject(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, msgBadJSON)
		return
	}
	user, authed := sessionUser(r)
	var data map[string]any
	switch typ {
	case "virtual_study", "group":
		data = pickFields(body, virtualStudyFields)
		applyVirtualStudyDefaults(data)
		if authed {
			if user == allUsers {
				http.Error(w, "Illegal username "+allUsers+" for assigning virtual studies.", http.StatusInternalServerError)
				return
			}
			data["owner"] = user
			if save || typ == "group" {
				data["users"] = []any{user}
			}
		} else {
			data["owner"] = "anonymous"
			data["users"] = []any{}
		}
	case "settings":
		if !authed {
			http.Error(w, "", http.StatusServiceUnavailable)
			return
		}
		if p, _ := body["page"].(string); p != "study_view" && p != "results_view" {
			springError(w, r, http.StatusInternalServerError)
			return
		}
		data = body
		applyTimestampDefaults(data)
		data["owner"] = user
	case "custom_data":
		data = pickFields(body, customDataFields)
		applyTimestampDefaults(data)
		setDefault(data, "origin", []any{})
		if authed {
			data["owner"] = user
			data["users"] = []any{user}
		} else {
			data["owner"] = "anonymous"
			data["users"] = []any{}
		}
	case "custom_gene_list":
		if !authed {
			http.Error(w, "", http.StatusServiceUnavailable)
			return
		}
		data = pickFields(body, customGeneListFields)
		setDefault(data, "geneList", []any{})
		data["users"] = []any{user}
	default:
		data = body
	}
	writeJSON(w, map[string]any{"id": s.store().create(typ, data)})
}

func applyTimestampDefaults(data map[string]any) {
	now := nowMillis()
	setDefault(data, "created", now)
	setDefault(data, "lastUpdated", now)
}

// applyVirtualStudyDefaults fills the VirtualStudyData field initialisers.
func applyVirtualStudyDefaults(data map[string]any) {
	setDefault(data, "version", json.Number("1.0"))
	setDefault(data, "owner", "anonymous")
	setDefault(data, "origin", []any{})
	setDefault(data, "users", []any{})
	applyTimestampDefaults(data)
}

// ---- read ----

func (s *State) sessionGet(w http.ResponseWriter, r *http.Request) {
	typ, id := r.PathValue("type"), r.PathValue("id")
	if !sessionTypes[typ] {
		writeError(w, http.StatusBadRequest, msgEnumMismatch(sessionTypeClass, typ))
		return
	}
	sess, ok := s.store().get(typ, id)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if typ == "virtual_study" {
		s.populateDynamic(sess)
	}
	writeJSON(w, sess)
}

func (s *State) sessionUserVirtualStudies(w http.ResponseWriter, r *http.Request) {
	user, ok := sessionUser(r)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, s.virtualStudiesFor(user))
}

// virtualStudiesFor lists virtual studies whose users contain name, with
// dynamic ones resolved against the current data.
func (s *State) virtualStudiesFor(name string) []*Session {
	list := s.store().list("virtual_study", func(x *Session) bool {
		return containsUser(stringSet(x.Data["users"]), name)
	})
	for _, vs := range list {
		s.populateDynamic(vs)
	}
	return list
}

func (s *State) sessionUserCustomGeneLists(w http.ResponseWriter, r *http.Request) {
	user, ok := sessionUser(r)
	if !ok {
		http.Error(w, "", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.store().list("custom_gene_list", func(x *Session) bool {
		return containsUser(stringSet(x.Data["users"]), user)
	}))
}

// sessionFetchByOrigin lists sessions of typ owned by the user whose origin
// is exactly the posted set of study ids (groups/fetch, custom_data/fetch).
func (s *State) sessionFetchByOrigin(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var studyIDs []string
		readBody(r, &studyIDs)
		user, ok := sessionUser(r)
		if !ok {
			http.Error(w, "", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, s.store().list(typ, func(x *Session) bool {
			return containsUser(stringSet(x.Data["users"]), user) &&
				sameSet(stringSet(x.Data["origin"]), studyIDs)
		}))
	}
}

// ---- users add/delete ----

func (s *State) sessionUpdateUsers(w http.ResponseWriter, r *http.Request) {
	typ, op, id := r.PathValue("type"), r.PathValue("operation"), r.PathValue("id")
	switch typ {
	case "virtual_study", "group", "custom_data", "custom_gene_list":
	default:
		// The route's {type} pattern admits only these types.
		springNotFound(w, r)
		return
	}
	if op != "add" && op != "delete" {
		writeError(w, http.StatusBadRequest, msgEnumMismatch("org.cbioportal.legacy.web.Operation", op))
		return
	}
	user, ok := sessionUser(r)
	if !ok {
		http.Error(w, "", http.StatusServiceUnavailable)
		return
	}
	sess, found := s.store().get(typ, id)
	if !found {
		springError(w, r, http.StatusInternalServerError)
		return
	}
	users := stringSet(sess.Data["users"])
	if op == "add" {
		if !containsUser(users, user) {
			users = append(users, user)
		}
	} else {
		kept := users[:0]
		for _, u := range users {
			if u != user {
				kept = append(kept, u)
			}
		}
		users = kept
	}
	sess.Data["users"] = toAnySlice(users)
	s.store().update(typ, id, sess.Data)
	w.WriteHeader(http.StatusOK)
}

// ---- page settings ----

// recentPageSettings returns the most recently updated settings session of
// the user for the given origin set and page.
func (s *State) recentPageSettings(user string, origin []string, page string) *Session {
	list := s.store().list("settings", func(x *Session) bool {
		owner, _ := x.Data["owner"].(string)
		p, _ := x.Data["page"].(string)
		return strings.EqualFold(owner, user) && p == page && sameSet(stringSet(x.Data["origin"]), origin)
	})
	var best *Session
	var bestTS int64
	for _, x := range list {
		ts := numberToInt64(x.Data["lastUpdated"])
		if best == nil || ts > bestTS {
			best, bestTS = x, ts
		}
	}
	return best
}

func numberToInt64(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		f, _ := n.Float64()
		return int64(f)
	case float64:
		return int64(n)
	}
	return 0
}

func (s *State) sessionUpdateSettings(w http.ResponseWriter, r *http.Request) {
	body, err := readObject(r)
	page, _ := body["page"].(string)
	if err != nil || (body["page"] != nil && page != "study_view" && page != "results_view") {
		writeError(w, http.StatusBadRequest, msgBadJSON)
		return
	}
	user, ok := sessionUser(r)
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if page == "" {
		// settingsData.getPage().name() on a missing page: the IOException
		// handler does not catch it.
		springError(w, r, http.StatusInternalServerError)
		return
	}
	origin := stringSet(body["origin"])
	existing := s.recentPageSettings(user, origin, page)
	if existing == nil {
		applyTimestampDefaults(body)
		body["owner"] = user
		s.store().create("settings", body)
		w.WriteHeader(http.StatusOK)
		return
	}
	owner, _ := existing.Data["owner"].(string)
	if owner != user || !sameSet(stringSet(existing.Data["origin"]), origin) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body["created"] = existing.Data["created"]
	body["owner"] = owner
	body["origin"] = existing.Data["origin"]
	setDefault(body, "lastUpdated", nowMillis())
	s.store().update("settings", existing.ID, body)
	w.WriteHeader(http.StatusOK)
}

func (s *State) sessionFetchSettings(w http.ResponseWriter, r *http.Request) {
	var id struct {
		Page   string   `json:"page"`
		Origin []string `json:"origin"`
	}
	readBody(r, &id)
	if id.Page != "" && id.Page != "study_view" && id.Page != "results_view" {
		writeError(w, http.StatusBadRequest, msgBadJSON)
		return
	}
	user, ok := sessionUser(r)
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if id.Page == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	existing := s.recentPageSettings(user, id.Origin, id.Page)
	if existing == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, existing.Data)
}

// ---- dynamic virtual studies ----

// filterSamples resolves a studyViewFilter to its samples via the
// /api/filtered-samples/fetch handler.
func (s *State) filterSamples(filter any) ([]Sample, error) {
	raw, err := json.Marshal(filter)
	if err != nil {
		return nil, err
	}
	req := httptest.NewRequest(http.MethodPost, "/api/filtered-samples/fetch", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.filteredSamplesFetch(rec, req)
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("filtered-samples: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var samples []Sample
	if err := json.Unmarshal(rec.Body.Bytes(), &samples); err != nil {
		return nil, err
	}
	return samples, nil
}

// populateDynamic recomputes data.studies of a dynamic virtual study from its
// studyViewFilter. On failure the stored studies are kept.
func (s *State) populateDynamic(vs *Session) {
	if dyn, _ := vs.Data["dynamic"].(bool); !dyn || s.CH == nil {
		return
	}
	filter, ok := vs.Data["studyViewFilter"]
	if !ok || filter == nil {
		return
	}
	samples, err := s.filterSamples(filter)
	if err != nil {
		log.Printf("virtual study %s: resolving dynamic samples: %v", vs.ID, err)
		return
	}
	vs.Data["studies"] = groupSamplesByStudy(samples)
}

func groupSamplesByStudy(samples []Sample) []any {
	byStudy := map[string][]any{}
	order := []string{}
	for _, sm := range samples {
		if _, ok := byStudy[sm.StudyID]; !ok {
			order = append(order, sm.StudyID)
			byStudy[sm.StudyID] = []any{}
		}
		byStudy[sm.StudyID] = append(byStudy[sm.StudyID], sm.SampleID)
	}
	sort.Strings(order)
	out := make([]any, len(order))
	for i, st := range order {
		out[i] = map[string]any{"id": st, "samples": byStudy[st]}
	}
	return out
}

// ---- public virtual studies ----

func (s *State) publicVirtualStudies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.virtualStudiesFor(allUsers))
}

// checkPublisherKey enforces X-PUBLISHER-API-KEY == PUBLISHER_API_KEY: a missing
// header is a 400 (Spring required header), a wrong or unconfigured key 403.
func checkPublisherKey(w http.ResponseWriter, r *http.Request) bool {
	provided, present := r.Header["X-Publisher-Api-Key"]
	if !present {
		springError(w, r, http.StatusBadRequest)
		return false
	}
	required := strings.TrimSpace(os.Getenv("PUBLISHER_API_KEY"))
	if required == "" || len(provided) == 0 || provided[0] != required {
		writeError(w, http.StatusForbidden, "The access is forbidden.")
		return false
	}
	return true
}

func (s *State) cancerTypeExists(id string) (bool, error) {
	if s.CH == nil {
		return false, errors.New("no database")
	}
	type countRow struct {
		N uint64 `json:"n"`
	}
	rows, err := ch.QueryP[countRow](s.CH,
		"SELECT count() AS n FROM (SELECT type_of_cancer_id FROM cbioportal_ref.type_of_cancer "+
			"UNION DISTINCT SELECT type_of_cancer_id FROM type_of_cancer) WHERE type_of_cancer_id = {id:String}", ch.Params{"id": id})
	if err != nil {
		return false, err
	}
	return len(rows) > 0 && rows[0].N > 0, nil
}

func (s *State) studyExists(id string) (bool, error) {
	if s.CH == nil {
		return false, nil
	}
	type countRow struct {
		N uint64 `json:"n"`
	}
	rows, err := cachedQuery[countRow](s, fmt.Sprintf(
		"SELECT count() AS n FROM cancer_study WHERE study_id = '%s'", sqlLit(id)))
	if err != nil {
		return false, err
	}
	return len(rows) > 0 && rows[0].N > 0, nil
}

// applyPublishMetadata sets typeOfCancerId / pmid from the query string when
// given; false (after answering 400) on an unknown cancer type.
func (s *State) applyPublishMetadata(w http.ResponseWriter, r *http.Request, data map[string]any) bool {
	q := r.URL.Query()
	if q.Has("typeOfCancerId") {
		toc := q.Get("typeOfCancerId")
		if ok, err := s.cancerTypeExists(toc); err != nil || !ok {
			springError(w, r, http.StatusInternalServerError)
			return false
		}
		data["typeOfCancerId"] = toc
	}
	if q.Has("pmid") {
		data["pmid"] = q.Get("pmid")
	}
	return true
}

func (s *State) publishVirtualStudy(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !checkPublisherKey(w, r) {
		return
	}
	id := r.PathValue("id")
	if len(bytes.TrimSpace(raw)) == 0 {
		vs, ok := s.store().get("virtual_study", id)
		if !ok {
			springError(w, r, http.StatusInternalServerError)
			return
		}
		if !s.applyPublishMetadata(w, r, vs.Data) {
			return
		}
		vs.Data["users"] = []any{allUsers}
		s.store().update("virtual_study", id, vs.Data)
		w.WriteHeader(http.StatusOK)
		return
	}

	var body map[string]any
	if err := decodeJSON(raw, &body); err != nil || body == nil {
		writeError(w, http.StatusBadRequest, msgBadJSON)
		return
	}
	data := pickFields(body, virtualStudyFields)
	if msg := virtualStudyDataViolation(data); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	// VirtualStudyService.validateResolvableVirtualStudyData: the filter must
	// select samples (a missing filter selects none).
	filter, ok := data["studyViewFilter"]
	if !ok || filter == nil || s.CH == nil {
		writeError(w, http.StatusBadRequest, msgVSInvalidFilters)
		return
	}
	samples, err := s.filterSamples(filter)
	if err != nil || len(samples) == 0 {
		writeError(w, http.StatusBadRequest, msgVSInvalidFilters)
		return
	}
	applyVirtualStudyDefaults(data)
	if !s.applyPublishMetadata(w, r, data) {
		return
	}
	data["users"] = []any{allUsers}
	exists, err := s.studyExists(id)
	if err != nil {
		fail(w, err)
		return
	}
	if exists {
		conflict(w, "The study with id="+id+" already exists. Use a different id for the virtual study.")
		return
	}
	if !s.store().insert("virtual_study", id, data) {
		conflict(w, "A virtual study with the same ID or identical definition already exists: "+id)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *State) unpublishVirtualStudy(w http.ResponseWriter, r *http.Request) {
	readBody(r, nil)
	if !checkPublisherKey(w, r) {
		return
	}
	id := r.PathValue("id")
	vs, ok := s.store().get("virtual_study", id)
	if !ok {
		springError(w, r, http.StatusInternalServerError)
		return
	}
	if !containsUser(stringSet(vs.Data["users"]), allUsers) {
		writeError(w, http.StatusNotFound, "The virtual study with id="+id+" has not been found in the public list.")
		return
	}
	if r.URL.Query().Get("softDelete") == "false" {
		s.store().remove("virtual_study", id)
	} else {
		owner, _ := vs.Data["owner"].(string)
		vs.Data["users"] = []any{owner}
		s.store().update("virtual_study", id, vs.Data)
	}
	w.WriteHeader(http.StatusOK)
}

// sessionData returns the stored `data` JSON of session (typ, id).
func (s *State) sessionData(typ, id string) (json.RawMessage, bool) {
	sess, ok := s.store().get(typ, id)
	if !ok {
		return nil, false
	}
	raw, err := json.Marshal(sess.Data)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// sessionTypeClass is the enum Spring converts the {type} path variable to.
const sessionTypeClass = "org.cbioportal.legacy.utils.removeme.Session.SessionType"

// VirtualStudyValidationMessages.
const (
	vsSchemaHint        = "See https://docs.cbioportal.org/virtual-study-data-schema/#virtual-study-json-schema"
	msgVSNameRequired   = "Virtual study name cannot be null or blank. Check that you are passing virtual study data only (data field content) and not the whole session object (with _id). " + vsSchemaHint
	msgVSStudies        = "Virtual study must contain at least one study with samples. " + vsSchemaHint
	msgVSStudyID        = "Each study in virtual study must have a non-null and non-blank id. " + vsSchemaHint
	msgVSDynamicFilter  = "Virtual study with dynamic=true must have a defined study view filter. " + vsSchemaHint
	msgVSStaticSamples  = "Static virtual study (dynamic=false) must contain at least one sample in each study. " + vsSchemaHint
	msgVSInvalidFilters = "The provided virtual study data is not valid. Check that the filters are correct and that the studies and samples you are referring to in virtual study data exist in the system. " + vsSchemaHint
)

// virtualStudyDataViolation validates a published VirtualStudyData body
// (@NotBlank name, @NotEmpty studies, @NotBlank study ids and
// VirtualStudyDataValidator) and returns the first violation as the
// MethodArgumentNotValidException answer, "<field> <message>", or "".
func virtualStudyDataViolation(data map[string]any) string {
	if name, _ := data["name"].(string); strings.TrimSpace(name) == "" {
		return "name " + msgVSNameRequired
	}
	studies, _ := data["studies"].([]any)
	if len(studies) == 0 {
		return "studies " + msgVSStudies
	}
	for _, st := range studies {
		m, _ := st.(map[string]any)
		if id, _ := m["id"].(string); strings.TrimSpace(id) == "" {
			return "studies[].id " + msgVSStudyID
		}
	}
	if dyn, _ := data["dynamic"].(bool); dyn {
		if data["studyViewFilter"] == nil {
			return "studyViewFilter " + msgVSDynamicFilter
		}
		return ""
	}
	for _, st := range studies {
		m, _ := st.(map[string]any)
		if samples, _ := m["samples"].([]any); len(samples) == 0 {
			return "studies " + msgVSStaticSamples
		}
	}
	return ""
}

// conflict is DuplicateVirtualStudyException's answer: 409 with the message
// as a plain body.
func conflict(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	w.WriteHeader(http.StatusConflict)
	w.Write([]byte(msg))
}
