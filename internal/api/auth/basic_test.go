package auth

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// noFollow stops c from following redirects, so a test reads the Location
// the login answers with.
func noFollow(c *http.Client) *http.Client {
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// bcryptHash is the bcrypt hash of password, as basic.password holds it.
func bcryptHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

func TestBasicLogin(t *testing.T) {
	p := newFakeProvider(t)
	basic, err := loadBasicLogin(map[string]string{
		"basic.username": "robot", "basic.password": bcryptHash(t, "pw"), "basic.authorities": "study_a, all_tcga",
	})
	if err != nil {
		t.Fatal(err)
	}
	portal := loginPortal(t, p, func(o *oauth2Login) { o.basic = basic })

	// A wrong password is refused.
	c := noFollow(browser(t))
	resp, err := c.PostForm(portal.URL+"/j_spring_security_check", url.Values{"j_username": {"robot"}, "j_password": {"no"}})
	if err != nil || resp.Header.Get("Location") != "/login?error" {
		t.Fatalf("wrong password: %v %q", err, resp.Header.Get("Location"))
	}
	if resp, _ := c.Get(portal.URL + "/api/studies"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after a refused login: %d, want 401", resp.StatusCode)
	}

	// The right one, also as query parameters of a GET, starts a session
	// with the basic user's roles.
	resp, err = c.Get(portal.URL + "/j_spring_security_check?j_username=robot&j_password=pw")
	if err != nil || resp.Header.Get("Location") != "/restful_login" {
		t.Fatalf("login: %v %q", err, resp.Header.Get("Location"))
	}
	if got := mustBody(t)(c.Get(portal.URL + "/restful_login")); !strings.Contains(got, "Connecting to the MSK cBioPortal") {
		t.Fatalf("restful_login: %q", got)
	}
	if got := mustBody(t)(c.Get(portal.URL + "/api/studies")); !strings.Contains(got, "user=robot roles=study_a,all_tcga") {
		t.Fatalf("API with session: %q", got)
	}

	// Logging out ends it.
	resp, err = c.Get(portal.URL + "/j_spring_security_logout")
	if err != nil || resp.Header.Get("Location") != "/" {
		t.Fatalf("logout: %v %q", err, resp.Header.Get("Location"))
	}
	if resp, _ := c.Get(portal.URL + "/api/studies"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: %d, want 401", resp.StatusCode)
	}
}

func TestLoadBasicLogin(t *testing.T) {
	if b, err := loadBasicLogin(map[string]string{}); b != nil || err != nil {
		t.Fatalf("without basic.username: %v %v", b, err)
	}
	if _, err := loadBasicLogin(map[string]string{"basic.username": "robot", "basic.password": "plain"}); err == nil {
		t.Fatal("a password that is not a bcrypt hash was accepted")
	}
}

// TestBasicLoginWithDataAccessTokens pins that a basic login's password is
// still checked on a portal that also hands out data access tokens, and that
// only the basic user can log in this way. Java's UUID token provider
// accepted username/password logins whose password it never checked, so with
// dat.method=uuid a wrong password was let through (cbioportal#12514).
func TestBasicLoginWithDataAccessTokens(t *testing.T) {
	p := newFakeProvider(t)
	portal := datPortal(t, p, map[string]string{
		"basic.username": "robot", "basic.password": bcryptHash(t, "pw"), "basic.authorities": "study_a",
	})
	check := func(c *http.Client, user, password string) string {
		t.Helper()
		resp, err := c.PostForm(portal.URL+"/j_spring_security_check",
			url.Values{"j_username": {user}, "j_password": {password}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.Header.Get("Location")
	}

	// A wrong password, and the right password of another user, are refused.
	c := noFollow(browser(t))
	for _, login := range []struct{ user, password string }{
		{"robot", "no"}, {"robot", ""}, {"someone", "pw"},
	} {
		if got := check(c, login.user, login.password); got != "/login?error" {
			t.Errorf("login %q/%q: %q, want /login?error", login.user, login.password, got)
		}
		if resp, _ := c.Get(portal.URL + "/api/studies"); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("after a refused login %q/%q: %d, want 401", login.user, login.password, resp.StatusCode)
		}
	}

	// The right one starts a session with the basic user's roles.
	if got := check(c, "robot", "pw"); got != "/restful_login" {
		t.Fatalf("login: %q, want /restful_login", got)
	}
	if got := mustBody(t)(c.Get(portal.URL + "/api/studies")); !strings.Contains(got, "user=robot roles=study_a") {
		t.Fatalf("API with session: %q", got)
	}
}
