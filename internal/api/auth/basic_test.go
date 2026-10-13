package auth

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestBasicLogin(t *testing.T) {
	p := newFakeProvider(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	basic, err := loadBasicLogin(map[string]string{
		"basic.username": "robot", "basic.password": string(hash), "basic.authorities": "study_a, all_tcga",
	})
	if err != nil {
		t.Fatal(err)
	}
	portal := loginPortal(t, p, func(o *oauth2Login) { o.basic = basic })
	noFollow := func(c *http.Client) *http.Client {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}

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
