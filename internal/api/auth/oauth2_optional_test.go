package auth

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/portal"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

// keycloakProps are the properties of a login through the fake provider.
func keycloakProps(p *fakeProvider, method string) map[string]string {
	return map[string]string{
		"authenticate": method,
		"spring.security.oauth2.client.registration.keycloak.client-id":                    "portal",
		"spring.security.oauth2.client.registration.keycloak.client-secret":                "secret",
		"spring.security.oauth2.client.registration.keycloak.client-authentication-method": "client_secret_post",
		"spring.security.oauth2.client.registration.keycloak.scope":                        "openid,email,roles",
		"spring.security.oauth2.client.provider.keycloak.issuer-uri":                       p.URL + "/realms/cbio",
		"spring.security.oauth2.client.provider.keycloak.user-name-attribute":              "email",
	}
}

// optionalPortal is a portal with authenticate=optional_oauth2 whose
// handler echoes the request's user; /config_service is the real one.
func optionalPortal(t *testing.T, props map[string]string) *httptest.Server {
	t.Helper()
	l, err := Configure(config.EffectiveProperties(props, true))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	cs := &core.State{Props: props, PropsReplaceDefaults: true, AuthMethod: l.Method()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /config_service", portal.New(cs).ConfigService)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		u, ok := core.GetUser(r.Context())
		fmt.Fprintf(w, "%s %s user=%s known=%v", r.Method, r.URL.Path, u.Name, ok)
	})
	srv := httptest.NewServer(l.login.middleware(mux))
	t.Cleanup(srv.Close)
	return srv
}

func configOf(t *testing.T, c *http.Client, base string) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(mustBody(t)(c.Get(base+"/config_service"))), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOptionalOAuth2(t *testing.T) {
	p := newFakeProvider(t)
	p.roles = []string{"study_a"}
	portal := optionalPortal(t, keycloakProps(p, "optional_oauth2"))

	// Anonymous requests are served as without a login: no 401, no redirect.
	c := browser(t)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, path := range []string{"/api/studies", "/study/summary?id=study_a", "/"} {
		resp, err := c.Get(portal.URL + path)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("anonymous %s: %v %v, want 200", path, resp.StatusCode, err)
		}
		resp.Body.Close()
	}
	if got := mustBody(t)(c.Get(portal.URL + "/api/studies")); got != "GET /api/studies user= known=false" {
		t.Fatalf("anonymous API: %q", got)
	}
	cfg := configOf(t, c, portal.URL)
	if cfg["authenticationMethod"] != "optional_oauth2" || cfg["user_display_name"] != "anonymousUser" {
		t.Fatalf("anonymous config_service: authenticationMethod %v user_display_name %v",
			cfg["authenticationMethod"], cfg["user_display_name"])
	}

	// The frontend's sign-in window opens /login; with one registration it
	// goes straight to the provider and lands on /, signed in.
	resp, _ := c.Get(portal.URL + "/login")
	if resp.Header.Get("Location") != "/oauth2/authorization/keycloak" {
		t.Fatalf("/login redirects to %q", resp.Header.Get("Location"))
	}
	c.CheckRedirect = nil
	resp, err := c.Get(portal.URL + "/login")
	if err != nil || resp.Request.URL.Path != "/" {
		t.Fatalf("login ended at %v (%v), want /", resp.Request.URL, err)
	}
	resp.Body.Close()
	if got := mustBody(t)(c.Get(portal.URL + "/api/studies")); got != "GET /api/studies user=testuser@example.org known=true" {
		t.Fatalf("signed-in API: %q", got)
	}
	if cfg := configOf(t, c, portal.URL); cfg["user_display_name"] != "testuser@example.org" {
		t.Fatalf("signed-in user_display_name %v", cfg["user_display_name"])
	}

	// Logout returns to / without ending the provider's session.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ = c.Get(portal.URL + "/logout")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("logout: %d to %q, want 302 to /", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := mustBody(t)(c.Get(portal.URL + "/api/studies")); got != "GET /api/studies user= known=false" {
		t.Fatalf("after logout: %q", got)
	}
}

// Signing in grants no study access with authorization=false: no study
// authorization applies unless security.method_authorization_enabled.
func TestOptionalOAuth2StudyAuthorization(t *testing.T) {
	p := newFakeProvider(t)
	for _, tc := range []struct {
		enabled string
		want    bool
	}{{"", false}, {"false", false}, {"true", true}} {
		props := keycloakProps(p, "optional_oauth2")
		props["security.method_authorization_enabled"] = tc.enabled
		props["authorization"] = "false"
		cfg, err := loadOAuth2Config(props)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MethodAuthorization != tc.want {
			t.Errorf("method_authorization_enabled=%q: MethodAuthorization %v", tc.enabled, cfg.MethodAuthorization)
		}
	}
	props := keycloakProps(p, "optional_oauth2")
	props["authorization"] = "true"
	if _, err := loadOAuth2Config(props); err == nil {
		t.Error("authorization=true accepted")
	}
	props = keycloakProps(p, "optional_oauth2")
	props["dat.method"] = "oauth2"
	if _, err := Configure(config.EffectiveProperties(props, true)); err == nil {
		t.Error("dat.method=oauth2 accepted with optional_oauth2")
	}
}

func TestGoogleRegistrationDefaults(t *testing.T) {
	cfg, err := loadOAuth2Config(map[string]string{
		"authenticate":                          "optional_oauth2",
		"authorization":                         "false",
		"security.method_authorization_enabled": "false",
		"spring.security.oauth2.client.registration.google.clientId":        "id-123.apps.googleusercontent.com",
		"spring.security.oauth2.client.registration.google.clientSecret":    "s3cret",
		"spring.security.oauth2.client.provider.google.user-name-attribute": "email",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Registrations) != 1 {
		t.Fatalf("registrations %d", len(cfg.Registrations))
	}
	g := cfg.Registrations[0]
	want := oauth2Registration{
		ID: "google", ClientID: "id-123.apps.googleusercontent.com", ClientSecret: "s3cret",
		AuthorizationURI:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURI:          "https://www.googleapis.com/oauth2/v4/token",
		JWKSetURI:         "https://www.googleapis.com/oauth2/v3/certs",
		UserInfoURI:       "https://www.googleapis.com/oauth2/v3/userinfo",
		IssuerURI:         "https://accounts.google.com",
		RedirectURI:       "{baseUrl}/{action}/oauth2/code/{registrationId}",
		UserNameAttribute: "email",
	}
	if g.ID != want.ID || g.ClientID != want.ClientID || g.ClientSecret != want.ClientSecret ||
		g.AuthorizationURI != want.AuthorizationURI || g.TokenURI != want.TokenURI || g.JWKSetURI != want.JWKSetURI ||
		g.UserInfoURI != want.UserInfoURI || g.IssuerURI != want.IssuerURI || g.RedirectURI != want.RedirectURI ||
		g.UserNameAttribute != want.UserNameAttribute || g.Discover || g.ClientAuthPost {
		t.Errorf("google registration\n got %+v\nwant %+v", *g, want)
	}
	if strings.Join(g.Scopes, ",") != "openid,profile,email" {
		t.Errorf("scopes %v", g.Scopes)
	}
	// The defaults are not shared between registrations.
	if commonProviders["google"].UserNameAttribute != "sub" {
		t.Error("registration changed the shared Google defaults")
	}

	// Behind the TLS-terminating ingress the redirect URI is the one
	// registered with Google.
	login := newOAuth2Login(cfg, nil)
	srv := httptest.NewServer(login.middleware(http.NotFoundHandler()))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/oauth2/authorization/google", nil)
	req.Host = "go.cbioportal.org"
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != want.AuthorizationURI {
		t.Errorf("authorization endpoint %q", got)
	}
	if q.Get("redirect_uri") != "https://go.cbioportal.org/login/oauth2/code/google" ||
		q.Get("client_id") != want.ClientID || q.Get("scope") != "openid profile email" || q.Get("response_type") != "code" {
		t.Errorf("authorization request %v", q)
	}
	if strings.Contains(resp.Header.Get("Location"), "s3cret") {
		t.Error("client secret in the authorization redirect")
	}
	for _, c := range resp.Cookies() {
		if c.Name == loginCookie && !c.Secure {
			t.Error("login cookie not Secure behind https")
		}
	}

	// Google's alternative issuer form is accepted.
	if err := checkIDToken(map[string]any{"iss": "accounts.google.com", "aud": want.ClientID, "exp": float64(4e9), "nonce": "n"},
		append([]string{g.IssuerURI}, g.Issuers...), want.ClientID, "n", time.Unix(1_000_000_000, 0)); err != nil {
		t.Errorf("accounts.google.com issuer: %v", err)
	}
}

func TestBaseURLBehindProxy(t *testing.T) {
	for _, tc := range []struct {
		host string
		hdr  map[string]string
		tls  bool
		want string
	}{
		{"localhost:8080", nil, false, "http://localhost:8080"},
		{"go.cbioportal.org", map[string]string{"X-Forwarded-Proto": "https"}, false, "https://go.cbioportal.org"},
		{"10.0.0.5:8080", map[string]string{"X-Forwarded-Proto": "https, http", "X-Forwarded-Host": "go.cbioportal.org"}, false, "https://go.cbioportal.org"},
		{"go.cbioportal.org", nil, true, "https://go.cbioportal.org"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = tc.host
		for k, v := range tc.hdr {
			r.Header.Set(k, v)
		}
		if tc.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := core.BaseURL(r); got != tc.want {
			t.Errorf("baseURL(%s %v) = %q, want %q", tc.host, tc.hdr, got, tc.want)
		}
	}
}

func TestRegistrationProperties(t *testing.T) {
	p := newFakeProvider(t)
	props := keycloakProps(p, "optional_oauth2")
	props["spring.security.oauth2.client.registration.google.client-id"] = "gid"
	props["spring.security.oauth2.client.registration.google.client-secret"] = "gsecret"
	cfg, err := loadOAuth2Config(props)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Registrations) != 2 || cfg.Registrations[0].ID != "google" || cfg.Registrations[1].ID != "keycloak" {
		t.Fatalf("registrations %+v", cfg.Registrations)
	}
	// With several registrations /login lists them, as Java's login page.
	portal := optionalPortal(t, props)
	resp, err := http.Get(portal.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	page := mustBody(t)(resp, err)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `href="/oauth2/authorization/google"`) ||
		!strings.Contains(page, `href="/oauth2/authorization/keycloak"`) {
		t.Fatalf("login page %d %q", resp.StatusCode, page)
	}
	// Each registration completes its own flow.
	c := browser(t)
	mustBody(t)(c.Get(portal.URL + "/oauth2/authorization/keycloak"))
	if got := mustBody(t)(c.Get(portal.URL + "/api/x")); !strings.Contains(got, "user=testuser@example.org") {
		t.Fatalf("keycloak login: %q", got)
	}

	for name, props := range map[string]map[string]string{
		"github":       {"authenticate": "oauth2", "spring.security.oauth2.client.registration.github.client-id": "x"},
		"no endpoints": {"authenticate": "oauth2", "spring.security.oauth2.client.registration.corp.client-id": "x"},
		"bad flag": {"authenticate": "optional_oauth2", "security.method_authorization_enabled": "yes",
			"spring.security.oauth2.client.registration.google.client-id": "x"},
	} {
		if _, err := loadOAuth2Config(props); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if got := relaxedName("clientId"); got != "client-id" {
		t.Errorf("relaxedName(clientId) = %q", got)
	}
	if got := relaxedName("user_name_attribute"); got != "user-name-attribute" {
		t.Errorf("relaxedName(user_name_attribute) = %q", got)
	}
}

// www.cbioportal.org's Microsoft registration names the common endpoint's
// URIs and no issuer-uri; its ID tokens carry each user's own tenant as
// issuer, which is then not checked (signature, audience, expiry and nonce
// still are).
func TestMicrosoftCommonRegistration(t *testing.T) {
	const reg, prov = "spring.security.oauth2.client.registration.azure.", "spring.security.oauth2.client.provider.azure."
	cfg, err := loadOAuth2Config(map[string]string{
		"authenticate":                   "optional_oauth2",
		reg + "client-id":                "mid",
		reg + "clientSecret":             "msecret",
		reg + "authorization-grant-type": "authorization_code",
		reg + "redirect-uri":             "{baseUrl}/login/oauth2/code/azure",
		reg + "scope":                    "openid,profile,email",
		prov + "user-name-attribute":     "email",
		prov + "authorization-uri":       "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		prov + "token-uri":               "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		prov + "jwk-set-uri":             "https://login.microsoftonline.com/common/discovery/v2.0/keys",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Registrations) != 1 || cfg.Registrations[0].ID != "azure" || cfg.Registrations[0].IssuerURI != "" {
		t.Fatalf("registrations %+v", cfg.Registrations)
	}
	now := time.Unix(1_000_000_000, 0)
	tok := map[string]any{"iss": "https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0",
		"aud": "mid", "exp": float64(4e9), "nonce": "n"}
	if err := checkIDToken(tok, nil, "mid", "n", now); err != nil {
		t.Errorf("tenant issuer without issuer-uri: %v", err)
	}
	if err := checkIDToken(tok, []string{"https://login.microsoftonline.com/common/v2.0"}, "mid", "n", now); err == nil {
		t.Error("issuer not checked against a configured issuer")
	}
	if err := checkIDToken(tok, nil, "other", "n", now); err == nil {
		t.Error("audience not checked without issuer-uri")
	}
}
