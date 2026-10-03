package contract

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Session-service contracts for a logged-in user (SessionServiceController and
// SessionServiceRequestHandler): saved sessions are owned by and shared with
// the user, the per-user listings query data.users, and the origin-scoped
// listings match the origin studies as a set ($all plus $size).

// sessionUser returns the logged-in user name, skipping when the server runs
// anonymously (the user endpoints then answer 503).
func sessionUser(t *testing.T) string {
	t.Helper()
	var cfg map[string]any
	get(t, "/config_service", &cfg)
	u, _ := cfg["user_email_address"].(string)
	if u == "" || u == "anonymousUser" {
		t.Skip("server runs without a logged-in user")
	}
	return u
}

type sessionDoc struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Owner  string   `json:"owner"`
		Users  []string `json:"users"`
		Origin []string `json:"origin"`
		Name   string   `json:"name"`
	} `json:"data"`
}

func sessionIDs(docs []sessionDoc) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.ID
	}
	return out
}

func TestContractSessionGroupsByOrigin(t *testing.T) {
	requireAPI(t)
	user := sessionUser(t)
	origin := []string{"study_es_0", fmt.Sprintf("contract_origin_%d", time.Now().UnixNano())}
	var created struct {
		ID string `json:"id"`
	}
	post(t, "/api/session/group", map[string]any{
		"name": "contract group", "description": "",
		"studies": []any{map[string]any{"id": "study_es_0", "samples": []string{"TCGA-A1-A0SB-01"}}},
		"origin":  origin,
	}, &created)

	var groups []sessionDoc
	post(t, "/api/session/groups/fetch", []string{origin[1], origin[0]}, &groups)
	if len(groups) != 1 || groups[0].ID != created.ID {
		t.Fatalf("groups for %v: %v", origin, sessionIDs(groups))
	}
	wantEq(t, []any{groups[0].Data.Owner, groups[0].Data.Users}, []any{user, []string{user}})

	// A subset of the origin does not match.
	post(t, "/api/session/groups/fetch", []string{origin[1]}, &groups)
	wantEq(t, len(groups), 0)
}

func TestContractSessionCustomDataByOrigin(t *testing.T) {
	requireAPI(t)
	user := sessionUser(t)
	origin := []string{fmt.Sprintf("contract_origin_%d", time.Now().UnixNano())}
	var created struct {
		ID string `json:"id"`
	}
	post(t, "/api/session/custom_data", map[string]any{
		"displayName": "contract chart", "datatype": "STRING", "origin": origin,
		"data": []any{map[string]any{"studyId": "study_es_0", "sampleId": "TCGA-A1-A0SB-01", "patientId": "TCGA-A1-A0SB", "value": "x"}},
	}, &created)
	var docs []sessionDoc
	post(t, "/api/session/custom_data/fetch", origin, &docs)
	if len(docs) != 1 || docs[0].ID != created.ID {
		t.Fatalf("custom data for %v: %v", origin, sessionIDs(docs))
	}
	wantEq(t, []any{docs[0].Data.Owner, docs[0].Data.Users}, []any{user, []string{user}})
}

// Saved virtual studies and gene lists are listed for their users; the
// add / delete operations put the user on or take them off the list.
func TestContractSessionSavedListsAndUserOperations(t *testing.T) {
	requireAPI(t)
	user := sessionUser(t)
	nonce := time.Now().UnixNano()
	cases := []struct {
		save, list, typ string
		body            map[string]any
	}{
		{"/api/session/virtual_study/save", "/api/session/virtual_study", "virtual_study", map[string]any{
			"name": fmt.Sprintf("contract saved vs %d", nonce), "description": "",
			"studies": []any{map[string]any{"id": "study_es_0", "samples": []string{"TCGA-A1-A0SB-01"}}},
			"origin":  []string{"study_es_0"},
		}},
		{"/api/session/custom_gene_list/save", "/api/session/custom_gene_list", "custom_gene_list", map[string]any{
			"name": fmt.Sprintf("contract genes %d", nonce), "geneList": []string{"TP53", "BRCA1"},
		}},
	}
	for _, c := range cases {
		var created struct {
			ID string `json:"id"`
		}
		post(t, c.save, c.body, &created)
		var listed []sessionDoc
		get(t, c.list, &listed)
		i := slices.IndexFunc(listed, func(d sessionDoc) bool { return d.ID == created.ID })
		if i < 0 {
			t.Fatalf("%s: saved %s not listed", c.list, created.ID)
		}
		wantEq(t, listed[i].Data.Users, []string{user})
		if c.typ == "virtual_study" {
			wantEq(t, listed[i].Data.Owner, user)
		}

		resp, _ := rawDo(t, http.MethodGet, "/api/session/"+c.typ+"/delete/"+created.ID, "")
		wantEq(t, resp.StatusCode, http.StatusOK)
		get(t, c.list, &listed)
		if slices.Contains(sessionIDs(listed), created.ID) {
			t.Errorf("%s: %s still listed after delete", c.list, created.ID)
		}
		resp, _ = rawDo(t, http.MethodGet, "/api/session/"+c.typ+"/add/"+created.ID, "")
		wantEq(t, resp.StatusCode, http.StatusOK)
		get(t, c.list, &listed)
		if !slices.Contains(sessionIDs(listed), created.ID) {
			t.Errorf("%s: %s not listed after add", c.list, created.ID)
		}
	}
}

// Page settings: the first POST creates the user's settings for the page and
// origin, later ones update them; fetch returns the most recent data.
func TestContractSessionPageSettings(t *testing.T) {
	requireAPI(t)
	user := sessionUser(t)
	origin := []string{fmt.Sprintf("contract_origin_%d", time.Now().UnixNano())}
	for _, chart := range []string{"first", "second"} {
		resp, _ := rawDo(t, http.MethodPost, "/api/session/settings", fmt.Sprintf(
			`{"page":"study_view","origin":["%s"],"chartSettings":[{"id":"%s"}]}`, origin[0], chart))
		wantEq(t, resp.StatusCode, http.StatusOK)
		var got map[string]any
		post(t, "/api/session/settings/fetch", map[string]any{"page": "study_view", "origin": origin}, &got)
		wantEq(t, []any{got["owner"], got["origin"], got["page"]}, []any{user, []any{origin[0]}, "study_view"})
		if cs, _ := got["chartSettings"].([]any); len(cs) != 1 || cs[0].(map[string]any)["id"] != chart {
			t.Errorf("chartSettings %v, want [%s]", got["chartSettings"], chart)
		}
	}
}

// PublicVirtualStudiesController: publishing needs the X-PUBLISHER-API-KEY
// header (Spring's 400 without it, 403 for a wrong key); a published study is
// listed publicly until un-published, which hands it back to its owner.
func TestContractPublicVirtualStudyPublishing(t *testing.T) {
	requireAPI(t)
	user := sessionUser(t)
	var created struct {
		ID string `json:"id"`
	}
	post(t, "/api/session/virtual_study", map[string]any{
		"name": fmt.Sprintf("contract public vs %d", time.Now().UnixNano()), "description": "",
		"studies": []any{map[string]any{"id": "study_es_0", "samples": []string{"TCGA-A1-A0SB-01"}}},
		"origin":  []string{"study_es_0"},
	}, &created)
	path := "/api/public_virtual_studies/" + created.ID

	resp, _ := rawDo(t, http.MethodPost, path, "")
	wantEq(t, resp.StatusCode, http.StatusBadRequest)
	publish := func(method, key, query string) int {
		req, _ := http.NewRequest(method, apiBase()+path+query, nil)
		req.Header.Set("X-PUBLISHER-API-KEY", key)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	wantEq(t, publish(http.MethodPost, "wrong", ""), http.StatusForbidden)
	wantEq(t, publish(http.MethodPost, "SECRETKEY", "?typeOfCancerId=brca&pmid=123"), http.StatusOK)

	var public []map[string]any
	get(t, "/api/public_virtual_studies", &public)
	i := slices.IndexFunc(public, func(v map[string]any) bool { return v["id"] == created.ID })
	if i < 0 {
		t.Fatalf("published %s not listed", created.ID)
	}
	data := public[i]["data"].(map[string]any)
	wantEq(t, []any{data["users"], data["typeOfCancerId"], data["pmid"]}, []any{[]any{"*"}, "brca", "123"})

	wantEq(t, publish(http.MethodDelete, "SECRETKEY", ""), http.StatusOK)
	get(t, "/api/public_virtual_studies", &public)
	if slices.ContainsFunc(public, func(v map[string]any) bool { return v["id"] == created.ID }) {
		t.Errorf("%s still public after un-publishing", created.ID)
	}
	var got sessionDoc
	get(t, "/api/session/virtual_study/"+created.ID, &got)
	wantEq(t, []any{got.Data.Owner, got.Data.Users}, []any{user, []string{user}})

	// Only a published study can be un-published.
	wantEq(t, publish(http.MethodDelete, "SECRETKEY", ""), http.StatusNotFound)
}

// Session error contract: an unknown {type} or {operation} fails Spring's
// enum conversion (400, GlobalExceptionHandler message); a missing session is
// a bare 404; types outside the add/delete route's pattern match no route.
func TestContractSessionErrors(t *testing.T) {
	requireAPI(t)
	var e map[string]any
	resp, body := rawDo(t, http.MethodGet, "/api/session/bogus/5f0000000000000000000000", "")
	wantEq(t, resp.StatusCode, http.StatusBadRequest)
	json.Unmarshal(body, &e)
	wantEq(t, e["message"], "Request parameter type mismatch: java.lang.IllegalArgumentException: "+
		"No enum constant org.cbioportal.legacy.utils.removeme.Session.SessionType.bogus")

	resp, _ = rawDo(t, http.MethodPost, "/api/session/bogus", "{}")
	wantEq(t, resp.StatusCode, http.StatusBadRequest)

	resp, body = rawDo(t, http.MethodGet, "/api/session/main_session/5f0000000000000000000000", "")
	wantEq(t, []any{resp.StatusCode, len(body)}, []any{http.StatusNotFound, 0})

	resp, body = rawDo(t, http.MethodGet, "/api/session/virtual_study/share/5f0000000000000000000000", "")
	wantEq(t, resp.StatusCode, http.StatusBadRequest)
	e = nil
	json.Unmarshal(body, &e)
	wantEq(t, e["message"], "Request parameter type mismatch: java.lang.IllegalArgumentException: "+
		"No enum constant org.cbioportal.legacy.web.Operation.share")

	resp, _ = rawDo(t, http.MethodGet, "/api/session/main_session/add/5f0000000000000000000000", "")
	wantEq(t, resp.StatusCode, http.StatusNotFound)

	resp, body = rawDo(t, http.MethodPost, "/api/session/main_session", "[1]")
	wantEq(t, resp.StatusCode, http.StatusBadRequest)
	e = nil
	json.Unmarshal(body, &e)
	wantEq(t, e["message"], "There is an error in the JSON format of the request payload")
}

// Publishing a virtual study from a body validates VirtualStudyData first
// (MethodArgumentNotValidException: "<field> <message>"), then requires its
// filter to select samples.
func TestContractPublishVirtualStudyValidation(t *testing.T) {
	requireAPI(t)
	publish := func(body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, apiBase()+"/api/public_virtual_studies/contract_vs_validation", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-PUBLISHER-API-KEY", "SECRETKEY")
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
		defer resp.Body.Close()
		var e map[string]any
		json.NewDecoder(resp.Body).Decode(&e)
		msg, _ := e["message"].(string)
		return resp.StatusCode, msg
	}
	hint := " See https://docs.cbioportal.org/virtual-study-data-schema/#virtual-study-json-schema"
	code, msg := publish(`{"name":" ","studies":[{"id":"study_es_0","samples":["TCGA-A1-A0SB-01"]}]}`)
	wantEq(t, []any{code, msg}, []any{400, "name Virtual study name cannot be null or blank. Check that you are passing " +
		"virtual study data only (data field content) and not the whole session object (with _id)." + hint})
	code, msg = publish(`{"name":"vs","studies":[{"id":"study_es_0","samples":[]}]}`)
	wantEq(t, []any{code, msg}, []any{400, "studies Static virtual study (dynamic=false) must contain at least one sample in each study." + hint})
	code, msg = publish(`{"name":"vs","studies":[{"id":"study_es_0","samples":["TCGA-A1-A0SB-01"]}]}`)
	wantEq(t, []any{code, msg}, []any{400, "The provided virtual study data is not valid. Check that the filters are correct and " +
		"that the studies and samples you are referring to in virtual study data exist in the system." + hint})
}
