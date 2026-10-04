package contract

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Data access tokens (dat.method=oauth2) end to end through Keycloak. Needs
// Keycloak started from test/keycloak (test/keycloak/start.sh, which
// creates datuser with the role of one study) and the API logging in
// through it with data access tokens on (DAT=true test/e2e_api.sh):
//
//	DAT_KEYCLOAK=1 API=http://localhost:18096 go test -run DATKeycloak ./test/
//
// DAT_USER, DAT_PASSWORD, DAT_STUDY (the study the user's role grants) and
// DAT_DENIED_STUDY override the defaults.

var kcLoginForm = regexp.MustCompile(`id="kc-form-login"[^>]*action="([^"]+)"`)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestDATKeycloak(t *testing.T) {
	if os.Getenv("DAT_KEYCLOAK") == "" {
		t.Skip("DAT_KEYCLOAK unset: needs Keycloak and an API with dat.method=oauth2")
	}
	requireAPI(t)
	base := apiBase()
	granted := envOr("DAT_STUDY", "ascn_test_study")
	denied := envOr("DAT_DENIED_STUDY", "study_es_0")

	// Log in through Keycloak's form.
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	resp, err := browser.Get(base + "/login")
	if err != nil {
		t.Fatal(err)
	}
	page := readAll(resp)
	m := kcLoginForm.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no Keycloak login form at %s (%d)", resp.Request.URL, resp.StatusCode)
	}
	resp, err = browser.PostForm(html.UnescapeString(m[1]), url.Values{
		"username": {envOr("DAT_USER", "datuser")}, "password": {envOr("DAT_PASSWORD", "P@ssword1")}, "credentialId": {""},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAll(resp)
	sessionStudies := studyIDs(t, browser, base, "")

	// Download a token: the portal sends the browser to Keycloak, which
	// returns the code to /api/data-access-token/oauth2.
	resp, err = browser.Get(base + "/api/data-access-token")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(resp)
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/api/data-access-token/oauth2" {
		t.Fatalf("token download ended at %s with %d: %.200q", resp.Request.URL, resp.StatusCode, body)
	}
	callback := resp.Request.URL.String()
	if resp.Request.URL.Query().Get("state") == "" {
		t.Errorf("callback %s carries no state", callback)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "attachment; filename=cbioportal_data_access_token.txt" {
		t.Errorf("Content-Disposition %q", cd)
	}
	tok, ok := strings.CutPrefix(body, "token: ")
	if !ok || !strings.HasSuffix(tok, "\n") {
		t.Fatalf("token file %.80q", body)
	}
	tok = "Bearer " + strings.TrimSuffix(tok, "\n")

	// The callback works once.
	resp, err = browser.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	if readAll(resp); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("replayed callback: %d, want 401", resp.StatusCode)
	}

	// The token alone, without the session, sees the studies the user's
	// roles grant, as the session does; the second call is served from the
	// verified-token cache.
	api := &http.Client{Timeout: 30 * time.Second}
	for range 2 {
		got := studyIDs(t, api, base, tok)
		if !slices.Equal(got, []string{granted}) || !slices.Equal(got, sessionStudies) {
			t.Fatalf("studies with token %v, with session %v, want [%s]", got, sessionStudies, granted)
		}
	}
	for _, c := range []struct {
		method, path, body, auth string
		want                     int
	}{
		{"GET", "/api/studies/" + granted, "", tok, http.StatusOK},
		{"GET", "/api/studies/" + denied, "", tok, http.StatusForbidden},
		{"POST", "/api/molecular-profiles/fetch", `{"studyIds":["` + denied + `"]}`, tok, http.StatusForbidden},
		{"GET", "/api/data-access-tokens", "", tok, http.StatusNotImplemented},
		{"GET", "/api/studies", "", "Bearer bogus", http.StatusUnauthorized},
		{"GET", "/api/studies", "", "", http.StatusUnauthorized},
	} {
		req, _ := http.NewRequest(c.method, base+c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		resp, err := api.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		readAll(resp)
		if resp.StatusCode != c.want {
			name := "token"
			if c.auth != tok {
				name = "Authorization " + strings.Fields(c.auth + " none")[0]
			}
			t.Errorf("%s %s (%s): %d, want %d", c.method, c.path, name, resp.StatusCode, c.want)
		}
	}
}

func readAll(resp *http.Response) string {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// studyIDs lists the study ids GET /api/studies returns.
func studyIDs(t *testing.T, c *http.Client, base, auth string) []string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/api/studies", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/studies: %d %.200q", resp.StatusCode, body)
	}
	var studies []struct {
		StudyID string `json:"studyId"`
	}
	if err := json.Unmarshal([]byte(body), &studies); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range studies {
		ids = append(ids, s.StudyID)
	}
	slices.Sort(ids)
	return ids
}
