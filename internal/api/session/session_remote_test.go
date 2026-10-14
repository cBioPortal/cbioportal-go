package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/sessionstore"
)

// fakeSessionService mimics cBioPortal's session service for one portal
// instance: POST {type} creates (de-duplicating identical data), POST
// {type}/{id} inserts under a chosen id (409 when taken or identical),
// GET/PUT/DELETE {type}/{id}, and POST {type}/query/fetch evaluates the
// MongoDB filters the Java client sends.
type fakeSessionService struct {
	t      *testing.T
	mu     sync.Mutex
	docs   map[string][]*sessionstore.Session // by type, in insertion order
	nextID int
	// auth is the Authorization header of the last request; queries the
	// filters posted to query/fetch.
	auth    string
	queries []string
	// fail, when set, answers every request with this status.
	fail int
}

const fakePortal = "test_portal"

func newFakeSessionService(t *testing.T) (*fakeSessionService, *httptest.Server) {
	f := &fakeSessionService{t: t, docs: map[string][]*sessionstore.Session{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSessionService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	if f.fail != 0 {
		w.WriteHeader(f.fail)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/sessions/"+fakePortal+"/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	parts := strings.Split(rest, "/")
	raw, _ := io.ReadAll(r.Body)
	typ := parts[0]
	if !sessionTypes[typ] {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		core.WriteJSON(w, f.docs[typ])
	case len(parts) == 1 && r.Method == http.MethodPost:
		data, err := f.object(raw)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sum := sessionstore.Checksum(data)
		for _, d := range f.docs[typ] {
			if d.Checksum == sum {
				core.WriteJSON(w, map[string]string{"id": d.ID})
				return
			}
		}
		f.nextID++
		id := fmt.Sprintf("%024x", f.nextID)
		f.docs[typ] = append(f.docs[typ], &sessionstore.Session{ID: id, Checksum: sum, Data: data, Source: fakePortal, Type: typ})
		core.WriteJSON(w, map[string]string{"id": id})
	case len(parts) == 3 && parts[1] == "query" && parts[2] == "fetch" && r.Method == http.MethodPost:
		f.queries = append(f.queries, string(raw))
		var q map[string]any
		if err := sessionstore.DecodeJSON(raw, &q); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		out := []*sessionstore.Session{}
		for _, d := range f.docs[typ] {
			if mongoMatch(f.t, d, q) {
				out = append(out, f.served(d))
			}
		}
		core.WriteJSON(w, out)
	case len(parts) == 2:
		id := parts[1]
		i := f.index(typ, id)
		switch r.Method {
		case http.MethodGet:
			if i < 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			core.WriteJSON(w, f.served(f.docs[typ][i]))
		case http.MethodPost:
			data, err := f.object(raw)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sum := sessionstore.Checksum(data)
			if i >= 0 || f.hasChecksum(typ, sum) {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.docs[typ] = append(f.docs[typ], &sessionstore.Session{ID: id, Checksum: sum, Data: data, Source: fakePortal, Type: typ})
			core.WriteJSON(w, map[string]string{"id": id})
		case http.MethodPut:
			data, err := f.object(raw)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if i < 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.docs[typ][i].Data, f.docs[typ][i].Checksum = data, sessionstore.Checksum(data)
		case http.MethodDelete:
			if i < 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.docs[typ] = append(f.docs[typ][:i], f.docs[typ][i+1:]...)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeSessionService) object(raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := sessionstore.DecodeJSON(raw, &m); err != nil || m == nil {
		return nil, fmt.Errorf("not an object")
	}
	return m, nil
}

func (f *fakeSessionService) index(typ, id string) int {
	for i, d := range f.docs[typ] {
		if d.ID == id {
			return i
		}
	}
	return -1
}

func (f *fakeSessionService) hasChecksum(typ, sum string) bool {
	for _, d := range f.docs[typ] {
		if d.Checksum == sum {
			return true
		}
	}
	return false
}

// served is the document as the service answers it: without checksum.
func (f *fakeSessionService) served(d *sessionstore.Session) *sessionstore.Session {
	c := d.Clone()
	c.Checksum = ""
	return c
}

// mongoMatch evaluates the subset of MongoDB filters the Java client uses.
func mongoMatch(t *testing.T, d *sessionstore.Session, q map[string]any) bool {
	for k, cond := range q {
		if k == "$and" {
			for _, sub := range cond.([]any) {
				if !mongoMatch(t, d, sub.(map[string]any)) {
					return false
				}
			}
			continue
		}
		field, ok := strings.CutPrefix(k, "data.")
		if !ok {
			t.Fatalf("unexpected query field %q", k)
		}
		v := d.Data[field]
		arr, isArr := v.([]any)
		switch c := cond.(type) {
		case string:
			if v != c {
				return false
			}
		case map[string]any:
			if re, ok := c["$regex"].(string); ok {
				if c["$options"] != "i" {
					t.Fatalf("regex without i option: %v", c)
				}
				rx := regexp.MustCompile("(?i)" + re)
				vals := arr
				if !isArr {
					vals = []any{v}
				}
				hit := false
				for _, x := range vals {
					if s, ok := x.(string); ok && rx.MatchString(s) {
						hit = true
					}
				}
				if !hit {
					return false
				}
			} else if all, ok := c["$all"].([]any); ok {
				if !isArr || len(all) == 0 {
					return false
				}
				for _, want := range all {
					found := false
					for _, x := range arr {
						if x == want {
							found = true
						}
					}
					if !found {
						return false
					}
				}
			} else if n, ok := c["$size"].(json.Number); ok {
				if !isArr || fmt.Sprint(len(arr)) != n.String() {
					return false
				}
			} else {
				t.Fatalf("unexpected condition %v", c)
			}
		default:
			t.Fatalf("unexpected condition %v", cond)
		}
	}
	return true
}

func newRemoteSessionTestEnv(t *testing.T) (*sessionTestEnv, *fakeSessionService) {
	f, srv := newFakeSessionService(t)
	st, err := sessionstore.NewRemoteStore(srv.URL+"/api/sessions/"+fakePortal+"/", "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := New(&core.State{Sessions: st})
	mux := http.NewServeMux()
	s.Routes(mux)
	return &sessionTestEnv{t: t, s: s, mux: mux}, f
}

// forEachStore runs a session test against the built-in store and against
// the session service.
func forEachStore(t *testing.T, test func(t *testing.T, e *sessionTestEnv)) {
	t.Run("memory", func(t *testing.T) { test(t, newSessionTestEnv(t, "")) })
	t.Run("remote", func(t *testing.T) {
		e, _ := newRemoteSessionTestEnv(t)
		test(t, e)
	})
}

// The same stored data serves the same bodies from either store, apart from
// the envelope's source (the session service's portal instance) and the
// checksum, which the session service does not serve.
func TestRemoteStoreServesSameBodies(t *testing.T) {
	mem := newSessionTestEnv(t, "")
	rem, _ := newRemoteSessionTestEnv(t)
	vs := map[string]any{
		"name": "vs", "description": "d", "origin": []string{"a"},
		"studies":     []any{map[string]any{"id": "a", "samples": []string{"s2", "s1"}}},
		"created":     1727000000000,
		"lastUpdated": 1727000000001,
		"version":     1.5,
	}
	norm := regexp.MustCompile(`"(id|source)":"[^"]*"|"checksum":"[^"]*",`)
	for _, c := range []struct{ create, get string }{
		{"/api/session/virtual_study/save", "/api/session/virtual_study/"},
		{"/api/session/main_session", "/api/session/main_session/"},
	} {
		idM := mem.create(c.create, "alice", vs)
		idR := rem.create(c.create, "alice", vs)
		bm := mem.do("GET", c.get+idM, "", nil, nil).Body.String()
		br := rem.do("GET", c.get+idR, "", nil, nil).Body.String()
		if !strings.Contains(br, `"source":"`+fakePortal+`"`) || strings.Contains(br, "checksum") {
			t.Errorf("remote envelope: %s", br)
		}
		if a, b := norm.ReplaceAllString(bm, ""), norm.ReplaceAllString(br, ""); a != b {
			t.Errorf("%s bodies differ:\nmemory %s\nremote %s", c.create, a, b)
		}
	}
	lm := mem.do("GET", "/api/session/virtual_study", "alice", nil, nil).Body.String()
	lr := rem.do("GET", "/api/session/virtual_study", "alice", nil, nil).Body.String()
	if a, b := norm.ReplaceAllString(lm, ""), norm.ReplaceAllString(lr, ""); a != b {
		t.Errorf("listing differs:\nmemory %s\nremote %s", a, b)
	}
}

// The filters are the MongoDB queries SessionServiceRequestHandler posts.
func TestSessionFilterMongoQuery(t *testing.T) {
	cases := []struct {
		f    sessionstore.Filter
		want string
	}{
		{sessionstore.Filter{Users: "alice"}, `{"data.users":{"$options":"i","$regex":"\\Qalice\\E"}}`},
		{sessionstore.Filter{Users: "*"}, `{"data.users":{"$options":"i","$regex":"\\Q*\\E"}}`},
		{sessionstore.Filter{Users: "a", Origin: []string{"x", "y"}, HasOrigin: true},
			`{"$and":[{"data.users":{"$options":"i","$regex":"\\Qa\\E"}},{"data.origin":{"$all":["x","y"]}},{"data.origin":{"$size":2}}]}`},
		{sessionstore.Filter{Owner: "a", HasOrigin: true, Page: "study_view"},
			`{"$and":[{"data.owner":{"$options":"i","$regex":"\\Qa\\E"}},{"data.origin":{"$all":[]}},{"data.origin":{"$size":0}},{"data.page":"study_view"}]}`},
	}
	for _, c := range cases {
		raw, _ := json.Marshal(c.f.MongoQuery())
		if string(raw) != c.want {
			t.Errorf("%+v:\n got %s\nwant %s", c.f, raw, c.want)
		}
	}
	if got := sessionstore.JavaPatternQuote(`a\Eb`); got != `\Qa\E\\E\Qb\E` {
		t.Errorf("javaPatternQuote: %s", got)
	}
	if !regexp.MustCompile(`(?i)` + sessionstore.JavaPatternQuote(`x.y\E(`)).MatchString(`AX.Y\E(`) {
		t.Error("quoted pattern does not match its literal")
	}
}

// Users match as in Java: a case-insensitive substring of a users entry.
func TestRemoteStoreUserSubstring(t *testing.T) {
	e, f := newRemoteSessionTestEnv(t)
	id := e.create("/api/session/virtual_study/save", "Alice.Smith", map[string]any{"name": "vs"})
	for _, user := range []string{"alice.smith", "alice"} {
		got := e.list("/api/session/virtual_study", user, nil)
		if len(got) != 1 || got[0].ID != id {
			t.Errorf("%s: %v", user, got)
		}
	}
	if got := e.list("/api/session/virtual_study", "alicex", nil); len(got) != 0 {
		t.Errorf("alicex: %v", got)
	}
	if q := f.queries[len(f.queries)-1]; q != `{"data.users":{"$options":"i","$regex":"\\Qalicex\\E"}}` {
		t.Errorf("query sent: %s", q)
	}
}

func TestRemoteStoreBasicAuth(t *testing.T) {
	f, srv := newFakeSessionService(t)
	base := srv.URL + "/api/sessions/" + fakePortal
	st, _ := sessionstore.NewRemoteStore(base, "u", "p@ss")
	if _, err := st.Create("main_session", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if want := "Basic dTpwQHNz"; f.auth != want {
		t.Errorf("Authorization %q, want %q", f.auth, want)
	}
	// As in Java, a user without a password sends no credentials.
	st, _ = sessionstore.NewRemoteStore(base, "u", "")
	st.Create("main_session", map[string]any{"a": 2})
	if f.auth != "" {
		t.Errorf("Authorization %q without a password", f.auth)
	}
}

// Failures of the session service answer as the Java controllers do.
func TestRemoteStoreFailures(t *testing.T) {
	t.Setenv("PUBLISHER_API_KEY", "k")
	e, f := newRemoteSessionTestEnv(t)
	id := e.create("/api/session/virtual_study/save", "alice", map[string]any{"name": "vs", "origin": []string{"a"}})
	cd := e.create("/api/session/custom_data", "alice", map[string]any{"displayName": "x", "origin": []string{"a"}})
	key := map[string]string{"X-PUBLISHER-API-KEY": "k"}
	if rec := e.do("GET", "/api/session/virtual_study/ffffffffffffffffffffffff", "", nil, nil); rec.Code != 404 || rec.Body.Len() != 0 {
		t.Errorf("unknown id: %d %q", rec.Code, rec.Body)
	}
	if rec := e.do("DELETE", "/api/public_virtual_studies/ffffffffffffffffffffffff", "", nil, key); rec.Code != 500 {
		t.Errorf("unpublish unknown id: %d", rec.Code)
	}

	f.fail = http.StatusServiceUnavailable
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	ident := map[string]any{"page": "study_view", "origin": []string{"a"}}
	cases := []struct {
		method, path string
		body         any
		status       int
		springBody   bool // Spring's default error body, else empty
	}{
		{"POST", "/api/session/main_session", map[string]any{"a": 1}, 500, true},
		{"GET", "/api/session/virtual_study/" + id, nil, 500, false},
		{"GET", "/api/session/virtual_study", nil, 500, false},
		{"GET", "/api/session/custom_gene_list", nil, 500, true},
		{"GET", "/api/session/virtual_study/add/" + id, nil, 500, true},
		{"POST", "/api/session/groups/fetch", []string{"a"}, 500, true},
		{"POST", "/api/session/custom_data/fetch", []string{"a"}, 500, true},
		{"POST", "/api/session/settings", ident, 500, true},
		{"POST", "/api/session/settings/fetch", ident, 400, false},
		{"GET", "/api/public_virtual_studies", nil, 500, true},
		{"POST", "/api/public_virtual_studies/" + id, nil, 500, true},
		{"DELETE", "/api/public_virtual_studies/" + id, nil, 500, true},
	}
	for _, c := range cases {
		rec := e.do(c.method, c.path, "alice", c.body, key)
		if rec.Code != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, rec.Code, c.status)
		}
		if got := strings.Contains(rec.Body.String(), `"error":"Internal Server Error"`); got != c.springBody || (!c.springBody && rec.Body.Len() != 0) {
			t.Errorf("%s %s: body %q", c.method, c.path, rec.Body)
		}
	}
	if _, ok := e.s.SessionData("custom_data", cd); ok {
		t.Error("sessionData while the service fails")
	}
	if !strings.Contains(logs.String(), "status 503") {
		t.Errorf("failures not logged: %s", logs.String())
	}
}

func TestRemoteStorePublishConflict(t *testing.T) {
	e, f := newRemoteSessionTestEnv(t)
	data := map[string]any{"name": "vs"}
	if err := e.s.SessionStore().Insert("virtual_study", "my_vs", data); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SessionStore().Insert("virtual_study", "my_vs", map[string]any{"name": "other"}); err != sessionstore.ErrConflict {
		t.Errorf("taken id: %v", err)
	}
	if err := e.s.SessionStore().Insert("virtual_study", "my_vs2", data); err != sessionstore.ErrConflict {
		t.Errorf("identical data: %v", err)
	}
	if err := e.s.SessionStore().Update("virtual_study", "nope", data); err != sessionstore.ErrNotFound {
		t.Errorf("update unknown: %v", err)
	}
	if err := e.s.SessionStore().Remove("virtual_study", "nope"); err != sessionstore.ErrNotFound {
		t.Errorf("remove unknown: %v", err)
	}
	f.fail = http.StatusInternalServerError
	if err := e.s.SessionStore().Insert("virtual_study", "x", data); err == nil || err == sessionstore.ErrConflict {
		t.Errorf("failing service: %v", err)
	}
}

func TestRemoteStoreUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := strings.Replace(srv.URL, "http://", "http://u:secret@", 1) + "/api/sessions/p/"
	srv.Close()
	st, err := sessionstore.NewRemoteStore(base, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Get("main_session", "x")
	if err == nil || err == sessionstore.ErrNotFound {
		t.Fatalf("unreachable service: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaks the password: %v", err)
	}
}

func TestConfigureSessions(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	t.Setenv("SESSION_SERVICE_URL", "")
	t.Setenv("SESSION_SERVICE_PASSWORD", "")

	s := New(&core.State{Props: map[string]string{"session.service.url": ""}})
	if err := s.ConfigureSessions(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.SessionStore().(*sessionstore.MemoryStore); !ok {
		t.Errorf("no session service: %T", s.SessionStore())
	}

	s = New(&core.State{Props: map[string]string{
		"session.service.url":      "http://sess:5000/api/sessions/p/",
		"session.service.user":     "u",
		"session.service.password": "hunter2",
	}})
	if err := s.ConfigureSessions(); err != nil {
		t.Fatal(err)
	}
	st, ok := s.SessionStore().(*sessionstore.RemoteStore)
	if !ok || st.Base != "http://sess:5000/api/sessions/p" || st.User != "u" || st.Password != "hunter2" {
		t.Errorf("session service: %#v", s.SessionStore())
	}

	t.Setenv("SESSION_SERVICE_URL", "http://env:5000/api/sessions/p")
	t.Setenv("SESSION_SERVICE_PASSWORD", "envsecret")
	s = New(&core.State{})
	if err := s.ConfigureSessions(); err != nil {
		t.Fatal(err)
	}
	if st, ok := s.SessionStore().(*sessionstore.RemoteStore); !ok || st.Base != "http://env:5000/api/sessions/p" || st.Password != "envsecret" {
		t.Errorf("from the environment: %#v", s.SessionStore())
	}

	s = New(&core.State{Props: map[string]string{"session.service.url": "sess:5000"}})
	if err := s.ConfigureSessions(); err == nil {
		t.Error("invalid URL accepted")
	}
	for _, secret := range []string{"hunter2", "envsecret"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log leaks a password: %s", logs.String())
		}
	}
}

func TestPublisherKeyProperty(t *testing.T) {
	t.Setenv("PUBLISHER_API_KEY", "env")
	s := New(&core.State{})
	if s.publisherKey() != "env" {
		t.Errorf("env key: %q", s.publisherKey())
	}
	s.Props = map[string]string{"session.endpoint.publisher-api-key": "prop"}
	if s.publisherKey() != "prop" {
		t.Errorf("property key: %q", s.publisherKey())
	}
}
