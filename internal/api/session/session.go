package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/sessionstore"
	"github.com/cbioportal/cbioportal-go/internal/api/studies"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// The Java SessionServiceController and PublicVirtualStudiesController, over
// a sessionstore.Store: cBioPortal's external session service when
// session.service.url is set (RemoteStore), else the built-in store, which
// keeps sessions in memory and, when SESSION_STORE_FILE is set, in a JSON
// file (MemoryStore).

// allUsers is the users-entry marking a virtual study as public.
const allUsers = "*"

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

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// sessionUser returns the logged-in user name (Java isAuthorized/userName).
func sessionUser(r *http.Request) (string, bool) {
	u, ok := core.GetUser(r.Context())
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
	if err := sessionstore.DecodeJSON(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("body must be a JSON object")
	}
	return m, nil
}

// Routes registers the session service and virtual study endpoints.
func (s Server) Routes(mux *http.ServeMux) {
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

func (s Server) sessionAdd(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	if !sessionTypes[typ] {
		core.ReadBody(r, nil)
		core.WriteError(w, http.StatusBadRequest, core.MsgEnumMismatch(sessionTypeClass, typ))
		return
	}
	s.addSession(w, r, typ, false)
}

func (s Server) sessionAddWith(typ string, save bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.addSession(w, r, typ, save) }
}

// addSession applies the per-type owner/users rules of the Java controller and
// stores the payload, answering {"id": ...}.
func (s Server) addSession(w http.ResponseWriter, r *http.Request, typ string, save bool) {
	body, err := readObject(r)
	if err != nil {
		core.WriteError(w, http.StatusBadRequest, core.MsgBadJSON)
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
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if p, _ := body["page"].(string); p != "study_view" && p != "results_view" {
			core.SpringError(w, r, http.StatusInternalServerError)
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
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		data = pickFields(body, customGeneListFields)
		setDefault(data, "geneList", []any{})
		data["users"] = []any{user}
	default:
		data = body
	}
	id, err := s.SessionStore().Create(typ, data)
	if err != nil {
		// RestTemplate's exception is not handled by the controller.
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	core.WriteJSON(w, map[string]any{"id": id})
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

func (s Server) sessionGet(w http.ResponseWriter, r *http.Request) {
	typ, id := r.PathValue("type"), r.PathValue("id")
	if !sessionTypes[typ] {
		core.WriteError(w, http.StatusBadRequest, core.MsgEnumMismatch(sessionTypeClass, typ))
		return
	}
	sess, err := s.SessionStore().Get(typ, id)
	if errors.Is(err, sessionstore.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		sessionstore.LogError(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if typ == "virtual_study" {
		s.populateDynamic(sess)
	}
	core.WriteJSON(w, sess)
}

func (s Server) sessionUserVirtualStudies(w http.ResponseWriter, r *http.Request) {
	user, ok := sessionUser(r)
	if !ok {
		core.WriteJSON(w, []any{})
		return
	}
	list, err := s.virtualStudiesFor(user)
	if err != nil {
		sessionstore.LogError(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	core.WriteJSON(w, list)
}

// virtualStudiesFor lists virtual studies whose users contain name, with
// dynamic ones resolved against the current data.
func (s Server) virtualStudiesFor(name string) ([]*sessionstore.Session, error) {
	list, err := s.SessionStore().Find("virtual_study", sessionstore.Filter{Users: name})
	if err != nil {
		return nil, err
	}
	for _, vs := range list {
		s.populateDynamic(vs)
	}
	return list, nil
}

func (s Server) sessionUserCustomGeneLists(w http.ResponseWriter, r *http.Request) {
	user, ok := sessionUser(r)
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	s.writeSessions(w, r, "custom_gene_list", sessionstore.Filter{Users: user})
}

// writeSessions answers the sessions of typ matching f; a store failure is
// the unhandled exception's Spring error.
func (s Server) writeSessions(w http.ResponseWriter, r *http.Request, typ string, f sessionstore.Filter) {
	list, err := s.SessionStore().Find(typ, f)
	if err != nil {
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	core.WriteJSON(w, list)
}

// sessionFetchByOrigin lists sessions of typ owned by the user whose origin
// is exactly the posted set of study ids (groups/fetch, custom_data/fetch).
func (s Server) sessionFetchByOrigin(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var studyIDs []string
		core.ReadBody(r, &studyIDs)
		user, ok := sessionUser(r)
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s.writeSessions(w, r, typ, sessionstore.Filter{Users: user, Origin: studyIDs, HasOrigin: true})
	}
}

// ---- users add/delete ----

func (s Server) sessionUpdateUsers(w http.ResponseWriter, r *http.Request) {
	typ, op, id := r.PathValue("type"), r.PathValue("operation"), r.PathValue("id")
	switch typ {
	case "virtual_study", "group", "custom_data", "custom_gene_list":
	default:
		// The route's {type} pattern admits only these types.
		core.SpringNotFound(w, r)
		return
	}
	if op != "add" && op != "delete" {
		core.WriteError(w, http.StatusBadRequest, core.MsgEnumMismatch("org.cbioportal.legacy.web.Operation", op))
		return
	}
	user, ok := sessionUser(r)
	if !ok {
		// HttpServletResponse.sendError: the Spring error body.
		core.SpringError(w, r, http.StatusServiceUnavailable)
		return
	}
	sess, err := s.SessionStore().Get(typ, id)
	if err != nil {
		// Java dereferences the missing session.
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	users := sessionstore.StringSet(sess.Data["users"])
	if op == "add" {
		if !sessionstore.ContainsUser(users, user) {
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
	if err := s.SessionStore().Update(typ, id, sess.Data); err != nil {
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---- page settings ----

// recentPageSettings returns the most recently updated settings session of
// the user for the given origin set and page.
func (s Server) recentPageSettings(user string, origin []string, page string) (*sessionstore.Session, error) {
	list, err := s.SessionStore().Find("settings", sessionstore.Filter{Owner: user, Origin: origin, HasOrigin: true, Page: page})
	if err != nil {
		return nil, err
	}
	var best *sessionstore.Session
	var bestTS int64
	for _, x := range list {
		ts := numberToInt64(x.Data["lastUpdated"])
		if best == nil || ts > bestTS {
			best, bestTS = x, ts
		}
	}
	return best, nil
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

func (s Server) sessionUpdateSettings(w http.ResponseWriter, r *http.Request) {
	body, err := readObject(r)
	page, _ := body["page"].(string)
	if err != nil || (body["page"] != nil && page != "study_view" && page != "results_view") {
		core.WriteError(w, http.StatusBadRequest, core.MsgBadJSON)
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
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	origin := sessionstore.StringSet(body["origin"])
	existing, err := s.recentPageSettings(user, origin, page)
	if err != nil {
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	if existing == nil {
		applyTimestampDefaults(body)
		body["owner"] = user
		if _, err := s.SessionStore().Create("settings", body); err != nil {
			sessionstore.LogError(err)
			core.SpringError(w, r, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	owner, _ := existing.Data["owner"].(string)
	if owner != user || !sessionstore.SameSet(sessionstore.StringSet(existing.Data["origin"]), origin) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body["created"] = existing.Data["created"]
	body["owner"] = owner
	body["origin"] = existing.Data["origin"]
	setDefault(body, "lastUpdated", nowMillis())
	if err := s.SessionStore().Update("settings", existing.ID, body); err != nil {
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s Server) sessionFetchSettings(w http.ResponseWriter, r *http.Request) {
	var id struct {
		Page   string   `json:"page"`
		Origin []string `json:"origin"`
	}
	core.ReadBody(r, &id)
	if id.Page != "" && id.Page != "study_view" && id.Page != "results_view" {
		core.WriteError(w, http.StatusBadRequest, core.MsgBadJSON)
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
	existing, err := s.recentPageSettings(user, id.Origin, id.Page)
	if err != nil {
		// The controller answers any exception with a bare 400.
		sessionstore.LogError(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if existing == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	core.WriteJSON(w, existing.Data)
}

// ---- dynamic virtual studies ----

// filterSamples resolves a studyViewFilter to its samples via the
// /api/filtered-samples/fetch handler.
func (s Server) filterSamples(filter any) ([]studies.Sample, error) {
	raw, err := json.Marshal(filter)
	if err != nil {
		return nil, err
	}
	req := httptest.NewRequest(http.MethodPost, "/api/filtered-samples/fetch", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.FilteredSamplesFetch(rec, req)
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("filtered-samples: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var samples []studies.Sample
	if err := json.Unmarshal(rec.Body.Bytes(), &samples); err != nil {
		return nil, err
	}
	return samples, nil
}

// populateDynamic recomputes data.studies of a dynamic virtual study from its
// studyViewFilter. On failure the stored studies are kept.
func (s Server) populateDynamic(vs *sessionstore.Session) {
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

func groupSamplesByStudy(samples []studies.Sample) []any {
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

func (s Server) publicVirtualStudies(w http.ResponseWriter, r *http.Request) {
	list, err := s.virtualStudiesFor(allUsers)
	if err != nil {
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	core.WriteJSON(w, list)
}

// publisherKey is the key publishing virtual studies requires: the
// session.endpoint.publisher-api-key property, else PUBLISHER_API_KEY.
func (s Server) publisherKey() string {
	if v := strings.TrimSpace(s.Props["session.endpoint.publisher-api-key"]); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("PUBLISHER_API_KEY"))
}

// checkPublisherKey enforces X-PUBLISHER-API-KEY == the publisher key: a
// missing header is a 400 (Spring required header), a wrong or unconfigured
// key 403.
func (s Server) checkPublisherKey(w http.ResponseWriter, r *http.Request) bool {
	provided, present := r.Header["X-Publisher-Api-Key"]
	if !present {
		core.SpringError(w, r, http.StatusBadRequest)
		return false
	}
	required := s.publisherKey()
	if required == "" || len(provided) == 0 || provided[0] != required {
		core.WriteError(w, http.StatusForbidden, "The access is forbidden.")
		return false
	}
	return true
}

func (s Server) cancerTypeExists(id string) (bool, error) {
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

func (s Server) studyExists(id string) (bool, error) {
	if s.CH == nil {
		return false, nil
	}
	type countRow struct {
		N uint64 `json:"n"`
	}
	rows, err := core.CachedQueryP[countRow](s.State,
		"SELECT count() AS n FROM cancer_study WHERE study_id = {study:String}", ch.Params{"study": id})
	if err != nil {
		return false, err
	}
	return len(rows) > 0 && rows[0].N > 0, nil
}

// applyPublishMetadata sets typeOfCancerId / pmid from the query string when
// given; false (after answering 400) on an unknown cancer type.
func (s Server) applyPublishMetadata(w http.ResponseWriter, r *http.Request, data map[string]any) bool {
	q := r.URL.Query()
	if q.Has("typeOfCancerId") {
		toc := q.Get("typeOfCancerId")
		if ok, err := s.cancerTypeExists(toc); err != nil || !ok {
			core.SpringError(w, r, http.StatusInternalServerError)
			return false
		}
		data["typeOfCancerId"] = toc
	}
	if q.Has("pmid") {
		data["pmid"] = q.Get("pmid")
	}
	return true
}

func (s Server) publishVirtualStudy(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !s.checkPublisherKey(w, r) {
		return
	}
	id := r.PathValue("id")
	if len(bytes.TrimSpace(raw)) == 0 {
		vs, err := s.SessionStore().Get("virtual_study", id)
		if err != nil {
			sessionstore.LogError(err)
			core.SpringError(w, r, http.StatusInternalServerError)
			return
		}
		if !s.applyPublishMetadata(w, r, vs.Data) {
			return
		}
		vs.Data["users"] = []any{allUsers}
		if err := s.SessionStore().Update("virtual_study", id, vs.Data); err != nil {
			sessionstore.LogError(err)
			core.SpringError(w, r, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	var body map[string]any
	if err := sessionstore.DecodeJSON(raw, &body); err != nil || body == nil {
		core.WriteError(w, http.StatusBadRequest, core.MsgBadJSON)
		return
	}
	data := pickFields(body, virtualStudyFields)
	if msg := virtualStudyDataViolation(data); msg != "" {
		core.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	// VirtualStudyService.validateResolvableVirtualStudyData: the filter must
	// select samples (a missing filter selects none).
	filter, ok := data["studyViewFilter"]
	if !ok || filter == nil || s.CH == nil {
		core.WriteError(w, http.StatusBadRequest, msgVSInvalidFilters)
		return
	}
	samples, err := s.filterSamples(filter)
	if err != nil || len(samples) == 0 {
		core.WriteError(w, http.StatusBadRequest, msgVSInvalidFilters)
		return
	}
	applyVirtualStudyDefaults(data)
	if !s.applyPublishMetadata(w, r, data) {
		return
	}
	data["users"] = []any{allUsers}
	exists, err := s.studyExists(id)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if exists {
		conflict(w, "The study with id="+id+" already exists. Use a different id for the virtual study.")
		return
	}
	if err := s.SessionStore().Insert("virtual_study", id, data); err != nil {
		if errors.Is(err, sessionstore.ErrConflict) {
			conflict(w, "A virtual study with the same ID or identical definition already exists: "+id)
			return
		}
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s Server) unpublishVirtualStudy(w http.ResponseWriter, r *http.Request) {
	core.ReadBody(r, nil)
	if !s.checkPublisherKey(w, r) {
		return
	}
	id := r.PathValue("id")
	vs, err := s.SessionStore().Get("virtual_study", id)
	if err != nil {
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	if !sessionstore.ContainsUser(sessionstore.StringSet(vs.Data["users"]), allUsers) {
		core.WriteError(w, http.StatusNotFound, "The virtual study with id="+id+" has not been found in the public list.")
		return
	}
	if r.URL.Query().Get("softDelete") == "false" {
		err = s.SessionStore().Remove("virtual_study", id)
	} else {
		owner, _ := vs.Data["owner"].(string)
		vs.Data["users"] = []any{owner}
		err = s.SessionStore().Update("virtual_study", id, vs.Data)
	}
	if err != nil {
		sessionstore.LogError(err)
		core.SpringError(w, r, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
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
