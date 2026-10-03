package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// sessionTestEnv serves only the session routes; user "" means anonymous.
type sessionTestEnv struct {
	t   *testing.T
	s   *State
	mux *http.ServeMux
}

func newSessionTestEnv(t *testing.T, storePath string) *sessionTestEnv {
	s := &State{sessions: newSessionStore(storePath)}
	mux := http.NewServeMux()
	s.routeSession(mux)
	return &sessionTestEnv{t: t, s: s, mux: mux}
}

func (e *sessionTestEnv) do(method, path, user string, body any, header map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if user != "" {
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, User{Name: user}))
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func (e *sessionTestEnv) create(path, user string, body any) string {
	e.t.Helper()
	rec := e.do("POST", path, user, body, nil)
	if rec.Code != 200 {
		e.t.Fatalf("POST %s -> %d: %s", path, rec.Code, rec.Body)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	id, _ := out["id"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(id) {
		e.t.Fatalf("POST %s: bad id %q (body %s)", path, id, rec.Body)
	}
	if len(out) != 1 {
		e.t.Errorf("POST %s: want only id, got %v", path, out)
	}
	return id
}

func (e *sessionTestEnv) session(path, user string) Session {
	e.t.Helper()
	rec := e.do("GET", path, user, nil, nil)
	if rec.Code != 200 {
		e.t.Fatalf("GET %s -> %d: %s", path, rec.Code, rec.Body)
	}
	var s Session
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	return s
}

func (e *sessionTestEnv) list(path, user string, body any) []Session {
	e.t.Helper()
	method := "GET"
	if body != nil {
		method = "POST"
	}
	rec := e.do(method, path, user, body, nil)
	if rec.Code != 200 {
		e.t.Fatalf("%s %s -> %d: %s", method, path, rec.Code, rec.Body)
	}
	var out []Session
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("%s %s: %v (%s)", method, path, err, rec.Body)
	}
	return out
}

func TestSessionRoutesRegisterWithoutConflict(t *testing.T) {
	NewRouter(&State{sessions: newSessionStore("")})
}

func TestMainSessionRoundTrip(t *testing.T) {
	e := newSessionTestEnv(t, "")
	data := map[string]any{"cancer_study_list": "study_es_0", "gene_list": "TP53", "big": 1727000000000}
	id := e.create("/api/session/main_session", "", data)
	s := e.session("/api/session/main_session/"+id, "")
	if s.ID != id || s.Source != "cbioportal" || s.Type != "main_session" || s.Checksum == "" {
		t.Errorf("bad session envelope: %+v", s)
	}
	if s.Data["gene_list"] != "TP53" || s.Data["big"] != float64(1727000000000) {
		t.Errorf("data not round-tripped: %v", s.Data)
	}
	if id2 := e.create("/api/session/main_session", "", data); id2 != id {
		t.Errorf("identical data should dedupe: %s vs %s", id, id2)
	}
	if rec := e.do("GET", "/api/session/main_session/000000000000000000000000", "", nil, nil); rec.Code != 404 {
		t.Errorf("missing session: want 404, got %d", rec.Code)
	}
	if rec := e.do("GET", "/api/session/virtual_study/"+id, "", nil, nil); rec.Code != 404 {
		t.Errorf("session is keyed by type: want 404, got %d", rec.Code)
	}
	if rec := e.do("POST", "/api/session/bogus", "", data, nil); rec.Code != 400 {
		t.Errorf("unknown type: want 400, got %d", rec.Code)
	}
	if rec := e.do("POST", "/api/session/main_session", "", []string{"x"}, nil); rec.Code != 400 {
		t.Errorf("non-object body: want 400, got %d", rec.Code)
	}
}

func TestVirtualStudyAnonymous(t *testing.T) {
	e := newSessionTestEnv(t, "")
	id := e.create("/api/session/virtual_study", "", map[string]any{
		"name": "vs", "studies": []any{map[string]any{"id": "study_es_0", "samples": []string{"s1"}}},
		"origin": []string{"study_es_0"}, "owner": "mallory", "users": []string{"*"}, "junk": 1,
	})
	s := e.session("/api/session/virtual_study/"+id, "")
	d := s.Data
	if d["owner"] != "anonymous" || len(d["users"].([]any)) != 0 {
		t.Errorf("anonymous owner/users not enforced: %v", d)
	}
	if _, ok := d["junk"]; ok {
		t.Errorf("unknown field kept: %v", d)
	}
	if d["created"] == nil || d["lastUpdated"] == nil || d["version"] != float64(1) {
		t.Errorf("defaults missing: %v", d)
	}
	if got := e.list("/api/session/virtual_study", "", nil); len(got) != 0 {
		t.Errorf("anonymous list: want [], got %v", got)
	}
	if got := e.list("/api/public_virtual_studies", "", nil); len(got) != 0 {
		t.Errorf("payload users must not publish: %v", got)
	}
	if rec := e.do("GET", "/api/session/virtual_study/add/"+id, "", nil, nil); rec.Code != 503 {
		t.Errorf("anonymous add: want 503, got %d", rec.Code)
	}
}

func TestVirtualStudyAuthorized(t *testing.T) {
	e := newSessionTestEnv(t, "")
	vs := map[string]any{"name": "vs", "origin": []string{"a"}, "users": []string{"bob"}}
	saved := e.create("/api/session/virtual_study/save", "alice", vs)
	shared := e.create("/api/session/virtual_study", "alice", map[string]any{"name": "shared", "origin": []string{"a"}})

	s := e.session("/api/session/virtual_study/"+saved, "")
	if s.Data["owner"] != "alice" || len(s.Data["users"].([]any)) != 1 || s.Data["users"].([]any)[0] != "alice" {
		t.Errorf("save should set owner+users to alice: %v", s.Data)
	}
	if got := e.list("/api/session/virtual_study", "alice", nil); len(got) != 1 || got[0].ID != saved {
		t.Fatalf("alice's studies: %v", got)
	}

	if rec := e.do("GET", "/api/session/virtual_study/add/"+shared, "bob", nil, nil); rec.Code != 200 {
		t.Fatalf("add: %d", rec.Code)
	}
	if got := e.list("/api/session/virtual_study", "bob", nil); len(got) != 1 || got[0].ID != shared {
		t.Errorf("bob after add: %v", got)
	}
	if rec := e.do("GET", "/api/session/virtual_study/delete/"+shared, "bob", nil, nil); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if got := e.list("/api/session/virtual_study", "bob", nil); len(got) != 0 {
		t.Errorf("bob after delete: %v", got)
	}
	if rec := e.do("GET", "/api/session/virtual_study/add/ffffffffffffffffffffffff", "bob", nil, nil); rec.Code != 500 {
		t.Errorf("add missing: want 500 (Java dereferences the null session), got %d", rec.Code)
	}
}

func TestGroupsAndCustomDataFetch(t *testing.T) {
	e := newSessionTestEnv(t, "")
	g := e.create("/api/session/group", "alice", map[string]any{"name": "g", "origin": []string{"a", "b"}})
	e.create("/api/session/group", "alice", map[string]any{"name": "g2", "origin": []string{"a"}})
	cd := e.create("/api/session/custom_data", "alice", map[string]any{
		"displayName": "x", "patientAttribute": false, "origin": []string{"b", "a"}, "data": []any{},
	})

	if rec := e.do("POST", "/api/session/groups/fetch", "", []string{"a"}, nil); rec.Code != 503 {
		t.Errorf("anonymous groups/fetch: want 503, got %d", rec.Code)
	}
	if rec := e.do("POST", "/api/session/custom_data/fetch", "", []string{"a"}, nil); rec.Code != 503 {
		t.Errorf("anonymous custom_data/fetch: want 503, got %d", rec.Code)
	}
	if got := e.list("/api/session/groups/fetch", "alice", []string{"b", "a"}); len(got) != 1 || got[0].ID != g {
		t.Errorf("groups by exact origin set: %v", got)
	}
	if got := e.list("/api/session/groups/fetch", "bob", []string{"a", "b"}); len(got) != 0 {
		t.Errorf("groups for other user: %v", got)
	}
	if got := e.list("/api/session/custom_data/fetch", "alice", []string{"a", "b"}); len(got) != 1 || got[0].ID != cd {
		t.Errorf("custom data: %v", got)
	}
	s := e.session("/api/session/custom_data/"+cd, "")
	if s.Data["owner"] != "alice" || s.Data["displayName"] != "x" {
		t.Errorf("custom data payload: %v", s.Data)
	}
	if raw, ok := e.s.sessionData("custom_data", cd); !ok || !bytes.Contains(raw, []byte(`"displayName":"x"`)) {
		t.Errorf("sessionData: %s %v", raw, ok)
	}
	if _, ok := e.s.sessionData("custom_data", "nope"); ok {
		t.Error("sessionData of missing id")
	}
}

func TestPageSettings(t *testing.T) {
	e := newSessionTestEnv(t, "")
	ident := map[string]any{"page": "study_view", "origin": []string{"a", "b"}}
	if rec := e.do("POST", "/api/session/settings/fetch", "", ident, nil); rec.Code != 503 {
		t.Errorf("anonymous settings/fetch: want 503, got %d", rec.Code)
	}
	if rec := e.do("POST", "/api/session/settings", "", ident, nil); rec.Code != 503 {
		t.Errorf("anonymous settings: want 503, got %d", rec.Code)
	}
	if rec := e.do("POST", "/api/session/settings/fetch", "alice", ident, nil); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("no settings yet: want empty 200, got %d %q", rec.Code, rec.Body)
	}
	for _, v := range []string{"v1", "v2"} {
		body := map[string]any{"page": "study_view", "origin": []string{"b", "a"}, "chartSettings": v}
		if rec := e.do("POST", "/api/session/settings", "alice", body, nil); rec.Code != 200 {
			t.Fatalf("save settings: %d", rec.Code)
		}
	}
	if n := len(e.s.store().list("settings", nil)); n != 1 {
		t.Errorf("second save should update in place, have %d sessions", n)
	}
	rec := e.do("POST", "/api/session/settings/fetch", "alice", ident, nil)
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["chartSettings"] != "v2" || got["owner"] != "alice" {
		t.Errorf("fetched settings: %v", got)
	}
}

func TestCustomGeneList(t *testing.T) {
	e := newSessionTestEnv(t, "")
	body := map[string]any{"name": "l", "geneList": []string{"TP53"}}
	if rec := e.do("POST", "/api/session/custom_gene_list/save", "", body, nil); rec.Code != 503 {
		t.Errorf("anonymous save: want 503, got %d", rec.Code)
	}
	if rec := e.do("GET", "/api/session/custom_gene_list", "", nil, nil); rec.Code != 503 {
		t.Errorf("anonymous list: want 503, got %d", rec.Code)
	}
	id := e.create("/api/session/custom_gene_list/save", "alice", body)
	if got := e.list("/api/session/custom_gene_list", "alice", nil); len(got) != 1 || got[0].ID != id {
		t.Errorf("alice's gene lists: %v", got)
	}
}

func TestPublicVirtualStudies(t *testing.T) {
	t.Setenv("PUBLISHER_API_KEY", "k")
	e := newSessionTestEnv(t, "")
	id := e.create("/api/session/virtual_study/save", "alice", map[string]any{"name": "vs", "origin": []string{"a"}})
	key := map[string]string{"X-PUBLISHER-API-KEY": "k"}

	if rec := e.do("POST", "/api/public_virtual_studies/"+id, "", nil, nil); rec.Code != 400 {
		t.Errorf("missing key: want 400, got %d", rec.Code)
	}
	if rec := e.do("POST", "/api/public_virtual_studies/"+id, "", nil, map[string]string{"X-PUBLISHER-API-KEY": "x"}); rec.Code != 403 {
		t.Errorf("wrong key: want 403, got %d", rec.Code)
	}
	if rec := e.do("DELETE", "/api/public_virtual_studies/"+id, "", nil, key); rec.Code != 404 {
		t.Errorf("unpublish unpublished: want 404, got %d", rec.Code)
	}
	if rec := e.do("POST", "/api/public_virtual_studies/"+id+"?pmid=123", "", nil, key); rec.Code != 200 {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	got := e.list("/api/public_virtual_studies", "", nil)
	if len(got) != 1 || got[0].ID != id || got[0].Data["pmid"] != "123" {
		t.Fatalf("public list: %v", got)
	}
	if rec := e.do("DELETE", "/api/public_virtual_studies/"+id, "", nil, key); rec.Code != 200 {
		t.Fatalf("soft unpublish: %d", rec.Code)
	}
	s := e.session("/api/session/virtual_study/"+id, "")
	if u := s.Data["users"].([]any); len(u) != 1 || u[0] != "alice" {
		t.Errorf("soft unpublish should restore owner: %v", s.Data)
	}
	e.do("POST", "/api/public_virtual_studies/"+id, "", nil, key)
	if rec := e.do("DELETE", "/api/public_virtual_studies/"+id+"?softDelete=false", "", nil, key); rec.Code != 200 {
		t.Fatalf("hard delete: %d", rec.Code)
	}
	if rec := e.do("GET", "/api/session/virtual_study/"+id, "", nil, nil); rec.Code != 404 {
		t.Errorf("hard delete should drop: %d", rec.Code)
	}
}

func TestSessionStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	e := newSessionTestEnv(t, path)
	id := e.create("/api/session/main_session", "", map[string]any{"k": "v"})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file not written: %v", err)
	}
	e2 := newSessionTestEnv(t, path)
	if s := e2.session("/api/session/main_session/"+id, ""); s.Data["k"] != "v" {
		t.Errorf("reloaded data: %v", s.Data)
	}
	matches, _ := filepath.Glob(path + ".tmp-*")
	if len(matches) != 0 {
		t.Errorf("leftover temp files: %v", matches)
	}
}

func TestGroupSamplesByStudy(t *testing.T) {
	got := groupSamplesByStudy([]Sample{{StudyID: "b", SampleID: "1"}, {StudyID: "a", SampleID: "2"}, {StudyID: "b", SampleID: "3"}})
	raw, _ := json.Marshal(got)
	if want := `[{"id":"a","samples":["2"]},{"id":"b","samples":["1","3"]}]`; string(raw) != want {
		t.Errorf("got %s want %s", raw, want)
	}
}
