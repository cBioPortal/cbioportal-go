package contract

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// authenticate=optional_oauth2 end to end through Keycloak: anonymous
// browsing stays open and signing in makes the user known, without changing
// which studies are readable. Needs Keycloak (test/keycloak/start.sh) and the
// API in optional mode (OPTIONAL=true test/e2e_api.sh):
//
//	OPTIONAL_KEYCLOAK=1 API=http://localhost:18096 go test -run OptionalOAuth2Keycloak ./test/
func TestOptionalOAuth2Keycloak(t *testing.T) {
	if os.Getenv("OPTIONAL_KEYCLOAK") == "" {
		t.Skip("OPTIONAL_KEYCLOAK unset: needs Keycloak and an API with authenticate=optional_oauth2")
	}
	requireAPI(t)
	base := apiBase()
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	config := func() map[string]any {
		t.Helper()
		resp, err := browser.Get(base + "/config_service")
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(readAll(resp)), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	// Anonymous: pages and the API are served, without a redirect to the
	// provider.
	resp, err := browser.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	readAll(resp)
	if resp.StatusCode != http.StatusOK || resp.Request.URL.String() != base+"/" {
		t.Fatalf("anonymous home page: %d at %s", resp.StatusCode, resp.Request.URL)
	}
	anonymous := studyIDs(t, browser, base, "")
	if len(anonymous) == 0 {
		t.Fatal("no studies anonymously")
	}
	if cfg := config(); cfg["authenticationMethod"] != "optional_oauth2" || cfg["user_display_name"] != "anonymousUser" {
		t.Fatalf("anonymous config_service: %v / %v", cfg["authenticationMethod"], cfg["user_display_name"])
	}

	// Sign in as the frontend's sign-in window does: /login, Keycloak's form,
	// back on the portal's home page.
	resp, err = browser.Get(base + "/login")
	if err != nil {
		t.Fatal(err)
	}
	page := readAll(resp)
	m := kcLoginForm.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no Keycloak login form at %s (%d)", resp.Request.URL, resp.StatusCode)
	}
	resp, err = browser.PostForm(html.UnescapeString(m[1]), url.Values{
		"username": {envOr("KEYCLOAK_USER", "testuser")}, "password": {envOr("KEYCLOAK_PASSWORD", "P@ssword1")}, "credentialId": {""},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAll(resp)
	if resp.Request.URL.String() != base+"/" {
		t.Fatalf("login ended at %s, want %s/", resp.Request.URL, base)
	}
	user, _ := config()["user_display_name"].(string)
	if user == "" || user == "anonymousUser" {
		t.Fatalf("signed in, user_display_name %q", user)
	}
	// authorization=false: signing in changes no study access.
	if got := studyIDs(t, browser, base, ""); !slices.Equal(got, anonymous) {
		t.Errorf("studies signed in %v, anonymously %v", got, anonymous)
	}

	// A signed-in user saves a virtual study and finds it in their list.
	name := "optional-oauth2-" + time.Now().Format("150405.000")
	body := `{"name":"` + name + `","studies":[{"id":"` + anonymous[0] + `","samples":[]}]}`
	resp, err = browser.Post(base+"/api/session/virtual_study/save", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if saved := readAll(resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("save virtual study: %d %.200q", resp.StatusCode, saved)
	}
	resp, err = browser.Get(base + "/api/session/virtual_study")
	if err != nil {
		t.Fatal(err)
	}
	if list := readAll(resp); !strings.Contains(list, name) {
		t.Errorf("saved virtual study %s not in the user's list: %.300q", name, list)
	}

	// Logout returns to the home page, anonymous again.
	resp, err = browser.Get(base + "/logout")
	if err != nil {
		t.Fatal(err)
	}
	readAll(resp)
	if resp.Request.URL.String() != base+"/" {
		t.Errorf("logout ended at %s", resp.Request.URL)
	}
	if cfg := config(); cfg["user_display_name"] != "anonymousUser" {
		t.Errorf("after logout user_display_name %v", cfg["user_display_name"])
	}
}
