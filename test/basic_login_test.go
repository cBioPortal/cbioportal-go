package contract

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// The shared user with a password (basic.*) end to end: it signs in at
// /j_spring_security_check and reads only the studies its role grants.
// Needs the API with the basic login on (BASIC=true test/e2e_api.sh):
//
//	BASIC_LOGIN=1 API=http://localhost:18090 go test -run BasicLogin ./test/
func TestBasicLogin(t *testing.T) {
	if os.Getenv("BASIC_LOGIN") == "" {
		t.Skip("BASIC_LOGIN unset: needs an API with the basic login (BASIC=true test/e2e_api.sh)")
	}
	requireAPI(t)
	base := apiBase()
	granted, denied := "ascn_test_study", "study_es_0"
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := func(password string) string {
		t.Helper()
		resp, err := c.PostForm(base+"/j_spring_security_check", url.Values{"j_username": {"basicuser"}, "j_password": {password}})
		if err != nil {
			t.Fatal(err)
		}
		readAll(resp)
		return resp.Header.Get("Location")
	}
	status := func(method, path, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		readAll(resp)
		return resp.StatusCode
	}

	if loc := login("wrong"); loc != "/login?error" {
		t.Fatalf("wrong password redirected to %q, want /login?error", loc)
	}
	if got := status("GET", "/api/studies", ""); got != http.StatusUnauthorized {
		t.Fatalf("after a refused login: %d, want 401", got)
	}
	if loc := login("basicpass"); loc != "/restful_login" {
		t.Fatalf("login redirected to %q, want /restful_login", loc)
	}
	if got := studyIDs(t, c, base, ""); !slices.Equal(got, []string{granted}) {
		t.Fatalf("studies %v, want [%s]", got, granted)
	}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/api/studies/" + granted, "", http.StatusOK},
		{"GET", "/api/studies/" + denied, "", http.StatusForbidden},
		{"POST", "/api/molecular-profiles/fetch", `{"studyIds":["` + denied + `"]}`, http.StatusForbidden},
		{"POST", "/api/molecular-profiles/fetch", `{"studyIds":["` + granted + `"]}`, http.StatusOK},
	} {
		if got := status(tc.method, tc.path, tc.body); got != tc.want {
			t.Errorf("%s %s %s: %d, want %d", tc.method, tc.path, tc.body, got, tc.want)
		}
	}
	if got := status("GET", "/j_spring_security_logout", ""); got != http.StatusFound {
		t.Fatalf("logout: %d", got)
	}
	if got := status("GET", "/api/studies", ""); got != http.StatusUnauthorized {
		t.Fatalf("after logout: %d, want 401", got)
	}
}
