package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// datPortal is a portal with the OAuth2 login and dat.method=oauth2 whose
// other routes echo the request they receive.
func datPortal(t *testing.T, p *fakeProvider, extra map[string]string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	realm := p.URL + "/realms/cbio"
	props := map[string]string{
		"authenticate": "oauth2",
		"spring.security.oauth2.client.registration.keycloak.client-id":                    "portal",
		"spring.security.oauth2.client.registration.keycloak.client-secret":                "secret",
		"spring.security.oauth2.client.registration.keycloak.client-authentication-method": "client_secret_post",
		"spring.security.oauth2.client.provider.keycloak.issuer-uri":                       realm,
		"spring.security.oauth2.client.provider.keycloak.user-name-attribute":              "email",
		"dat.method":                      "oauth2",
		"dat.oauth2.clientId":             "portal",
		"dat.oauth2.clientSecret":         "secret",
		"dat.oauth2.issuer":               realm,
		"dat.oauth2.accessTokenUri":       realm + "/token",
		"dat.oauth2.userAuthorizationUri": realm + "/auth",
		"dat.oauth2.jwkUrl":               realm + "/certs",
		"dat.oauth2.redirectUri":          base + "/api/data-access-token/oauth2",
	}
	for k, v := range extra {
		props[k] = v
	}
	s := &State{Props: props, PropsReplaceDefaults: true}
	if err := s.ConfigureAuth(); err != nil {
		t.Fatalf("ConfigureAuth: %v", err)
	}
	mux := http.NewServeMux()
	s.routeDataAccessTokens(mux, s.dat)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		u, _ := GetUser(r.Context())
		fmt.Fprintf(w, "%s %s user=%s roles=%s", r.Method, r.URL.Path, u.Name, strings.Join(u.Roles, ","))
	})
	srv.Config.Handler = s.login.middleware(mux)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func getWith(t *testing.T, c *http.Client, u, authorization string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := c.Do(req)
	return resp, mustBody(t)(resp, err)
}

// downloadToken logs a browser in and downloads a data access token.
func downloadToken(t *testing.T, p *fakeProvider, portal *httptest.Server) string {
	t.Helper()
	c := browser(t)
	mustBody(t)(c.Get(portal.URL + "/"))

	var first *http.Response
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if first == nil {
			first = req.Response
		}
		return nil
	}
	resp, body := getWith(t, c, portal.URL+"/api/data-access-token", "")
	if first == nil || first.StatusCode != http.StatusFound {
		t.Fatalf("data-access-token did not redirect")
	}
	want := p.URL + "/realms/cbio/auth?response_type=code&scope=openid+offline_access&client_id=portal&redirect_uri=" +
		url.QueryEscape(portal.URL+"/api/data-access-token/oauth2")
	if got := first.Header.Get("Location"); got != want {
		t.Errorf("Location %q, want %q", got, want)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token download: %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Disposition"); got != "attachment; filename=cbioportal_data_access_token.txt" {
		t.Errorf("Content-Disposition %q", got)
	}
	p.mu.Lock()
	form := p.tokenReq
	p.mu.Unlock()
	for k, v := range map[string]string{
		"grant_type": "authorization_code", "client_id": "portal", "client_secret": "secret",
		"redirect_uri": portal.URL + "/api/data-access-token/oauth2", "scope": "openid offline_access",
	} {
		if form.Get(k) != v {
			t.Errorf("code exchange %s=%q, want %q", k, form.Get(k), v)
		}
	}
	tok, ok := strings.CutPrefix(body, "token: ")
	if !ok || !strings.HasSuffix(tok, "\n") {
		t.Fatalf("token file %q", body)
	}
	return strings.TrimSuffix(tok, "\n")
}

func TestDATDownloadAndUse(t *testing.T) {
	p := newFakeProvider(t)
	p.roles = []string{"study_a"}
	portal := datPortal(t, p, nil)
	tok := downloadToken(t, p, portal)

	// The token authenticates API requests without a session; the user is
	// the access token's subject.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for range 3 {
		resp, body := getWith(t, c, portal.URL+"/api/studies", "Bearer "+tok)
		if resp.StatusCode != http.StatusOK || body != "GET /api/studies user=u1 roles=study_a" {
			t.Fatalf("bearer request: %d %q", resp.StatusCode, body)
		}
	}
	// Refreshed once, then served from the cache.
	if p.refreshes != 1 {
		t.Errorf("%d refreshes for 3 requests, want 1", p.refreshes)
	}
	// The header scheme is case-insensitive.
	if resp, _ := getWith(t, c, portal.URL+"/api/studies", "bearer "+tok); resp.StatusCode != http.StatusOK {
		t.Errorf("lowercase bearer: %d", resp.StatusCode)
	}

	for name, h := range map[string]string{
		"bogus token": "Bearer bogus", "empty token": "Bearer ", "basic auth": "Basic dTpw",
	} {
		resp, body := getWith(t, c, portal.URL+"/api/studies", h)
		if resp.StatusCode != http.StatusUnauthorized || body != "" {
			t.Errorf("%s: %d %q, want empty 401", name, resp.StatusCode, body)
		}
	}
	// As in Java, a token is checked on the open API paths too.
	if resp, _ := getWith(t, c, portal.URL+"/api/health", "Bearer bogus"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("open path with bad token: %d, want 401", resp.StatusCode)
	}
	// Pages ignore the header and go to the login.
	if resp, _ := getWith(t, c, portal.URL+"/study", "Bearer "+tok); resp.StatusCode != http.StatusFound {
		t.Errorf("page with token: %d, want redirect to login", resp.StatusCode)
	}
}

func TestDATDownloadNeedsLogin(t *testing.T) {
	p := newFakeProvider(t)
	portal := datPortal(t, p, nil)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/api/data-access-token", "/api/data-access-token/oauth2?code=x"} {
		if resp, _ := getWith(t, c, portal.URL+path, ""); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s anonymous: %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestDATBadCode(t *testing.T) {
	p := newFakeProvider(t)
	portal := datPortal(t, p, nil)
	c := browser(t)
	mustBody(t)(c.Get(portal.URL + "/"))
	if resp, body := getWith(t, c, portal.URL+"/api/data-access-token/oauth2?code=forged", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("forged code: %d %q, want 401", resp.StatusCode, body)
	}
}

func TestDATFilterUserRole(t *testing.T) {
	for _, tc := range []struct {
		roles []string
		want  int
	}{
		{[]string{"study_a"}, http.StatusUnauthorized},
		{[]string{"study_a", "dat_user"}, http.StatusFound},
	} {
		p := newFakeProvider(t)
		p.roles = tc.roles
		portal := datPortal(t, p, map[string]string{"dat.filter_user_role": "ROLE_dat_user"})
		c := browser(t)
		mustBody(t)(c.Get(portal.URL + "/"))
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, body := getWith(t, c, portal.URL+"/api/data-access-token", "")
		if resp.StatusCode != tc.want {
			t.Errorf("roles %v: %d %q, want %d", tc.roles, resp.StatusCode, body, tc.want)
		}
		if tc.want == http.StatusUnauthorized && body != `{"message":"You are prohibited from using Data Access Tokens"}` {
			t.Errorf("prohibited body %q", body)
		}
	}
}

func TestDATTokenManagementNotImplemented(t *testing.T) {
	p := newFakeProvider(t)
	portal := datPortal(t, p, nil)
	tok := downloadToken(t, p, portal)
	for _, op := range []string{"GET /api/data-access-tokens", "POST /api/data-access-tokens",
		"DELETE /api/data-access-tokens", "GET /api/data-access-tokens/x", "DELETE /api/data-access-tokens/x"} {
		method, path, _ := strings.Cut(op, " ")
		req, _ := http.NewRequest(method, portal.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		body := mustBody(t)(resp, err)
		if resp.StatusCode != http.StatusNotImplemented || body != `{"message":"Requested API is not implemented yet"}` {
			t.Errorf("%s: %d %q", op, resp.StatusCode, body)
		}
	}
}

func TestDATRejectsBadAccessTokens(t *testing.T) {
	for name, tamper := range map[string]func(map[string]any){
		"wrong issuer": func(c map[string]any) { c["iss"] = "https://evil.example" },
		"expired":      func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"no subject":   func(c map[string]any) { delete(c, "sub") },
	} {
		t.Run(name, func(t *testing.T) {
			p := newFakeProvider(t)
			portal := datPortal(t, p, nil)
			tok := downloadToken(t, p, portal)
			p.mu.Lock()
			p.tamperAccess = tamper
			p.mu.Unlock()
			if resp, body := getWith(t, http.DefaultClient, portal.URL+"/api/studies", "Bearer "+tok); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%d %q, want 401", resp.StatusCode, body)
			}
		})
	}
}

func TestDATShortLivedAccessTokensNotCached(t *testing.T) {
	p := newFakeProvider(t)
	p.accessTTL = datCacheMargin / 2
	portal := datPortal(t, p, nil)
	tok := downloadToken(t, p, portal)
	for range 2 {
		if resp, _ := getWith(t, http.DefaultClient, portal.URL+"/api/studies", "Bearer "+tok); resp.StatusCode != http.StatusOK {
			t.Fatalf("bearer request: %d", resp.StatusCode)
		}
	}
	if p.refreshes != 2 {
		t.Errorf("%d refreshes, want 2", p.refreshes)
	}
}

func TestDATCacheBounded(t *testing.T) {
	d := newDATProvider(&datConfig{})
	now := time.Now()
	for i := range datCacheSize + 10 {
		d.store(strconv.Itoa(i), datEntry{expires: now.Add(time.Hour + time.Duration(i)*time.Second)})
	}
	if len(d.cache) != datCacheSize {
		t.Fatalf("cache holds %d entries, want %d", len(d.cache), datCacheSize)
	}
	if _, ok := d.cache["0"]; ok {
		t.Error("the entry expiring soonest was kept")
	}
	if _, ok := d.cache[strconv.Itoa(datCacheSize+9)]; !ok {
		t.Error("the newest entry was dropped")
	}
}

func TestLoadDATConfig(t *testing.T) {
	for _, m := range []string{"", "none"} {
		if c, err := loadDATConfig(map[string]string{"dat.method": m}); c != nil || err != nil {
			t.Errorf("dat.method=%q: %v %v", m, c, err)
		}
	}
	for _, m := range []string{"uuid", "jwt"} {
		if _, err := loadDATConfig(map[string]string{"dat.method": m}); err == nil {
			t.Errorf("dat.method=%s accepted", m)
		}
	}
	_, err := loadDATConfig(map[string]string{"dat.method": "oauth2", "dat.oauth2.clientId": "c"})
	if err == nil || !strings.Contains(err.Error(), "dat.oauth2.jwkUrl") || strings.Contains(err.Error(), "clientId") {
		t.Errorf("missing keys: %v", err)
	}
	s := &State{Props: map[string]string{"dat.method": "oauth2", "dat.oauth2.clientId": "c", "dat.oauth2.issuer": "i",
		"dat.oauth2.accessTokenUri": "t", "dat.oauth2.userAuthorizationUri": "a", "dat.oauth2.jwkUrl": "j",
		"dat.oauth2.redirectUri": "r"}, PropsReplaceDefaults: true}
	if err := s.ConfigureAuth(); err == nil || !strings.Contains(err.Error(), "authenticate=oauth2") {
		t.Errorf("dat.method=oauth2 without login: %v", err)
	}
}

func TestBearerToken(t *testing.T) {
	for h, want := range map[string]string{"Bearer abc": "abc", "BEARER  abc ": "abc", "Bearer": "", "Basic abc": "", "": ""} {
		if got, _ := bearerToken(h); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", h, got, want)
		}
	}
}
