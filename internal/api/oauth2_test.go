package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeProvider is a minimal OpenID provider: discovery, JWKS, an authorize
// endpoint that logs the user in at once, token and userinfo endpoints.
type fakeProvider struct {
	*httptest.Server
	key      *rsa.PrivateKey
	roles    []string
	codes    map[string]string // code -> nonce
	tamper   func(claims map[string]any)
	tokenReq url.Values
}

func newFakeProvider(t *testing.T) *fakeProvider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{key: key, codes: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/cbio/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := p.URL + "/realms/cbio"
		writeJSON(w, map[string]string{
			"issuer": base, "authorization_endpoint": base + "/auth", "token_endpoint": base + "/token",
			"userinfo_endpoint": base + "/userinfo", "jwks_uri": base + "/certs", "end_session_endpoint": base + "/logout",
		})
	})
	mux.HandleFunc("/realms/cbio/certs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/realms/cbio/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := randomToken()
		p.codes[code] = q.Get("nonce")
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/realms/cbio/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p.tokenReq = r.PostForm
		nonce, ok := p.codes[r.PostForm.Get("code")]
		if !ok || r.PostForm.Get("client_secret") != "secret" {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant"})
			return
		}
		claims := map[string]any{
			"iss": p.URL + "/realms/cbio", "aud": "portal", "sub": "u1", "email": "testuser@example.org",
			"exp": time.Now().Add(5 * time.Minute).Unix(), "nonce": nonce,
			"resource_access": map[string]any{"cbioportal": map[string]any{"roles": p.roles}},
		}
		if p.tamper != nil {
			p.tamper(claims)
		}
		writeJSON(w, map[string]string{"access_token": "at", "id_token": p.sign(t, claims)})
	})
	mux.HandleFunc("/realms/cbio/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"sub": "u1", "email": "testuser@example.org"})
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

func (p *fakeProvider) sign(t *testing.T, claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(map[string]string{"alg": "RS256", "kid": "k1"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// loginPortal is a portal behind the OAuth2 login whose handler echoes the
// request it receives.
func loginPortal(t *testing.T, p *fakeProvider) *httptest.Server {
	cfg, err := loadOAuth2Config(map[string]string{
		"authenticate": "oauth2",
		"spring.security.oauth2.client.registration.keycloak.client-id":                    "portal",
		"spring.security.oauth2.client.registration.keycloak.client-secret":                "secret",
		"spring.security.oauth2.client.registration.keycloak.client-authentication-method": "client_secret_post",
		"spring.security.oauth2.client.registration.keycloak.scope":                        "openid,email,roles",
		"spring.security.oauth2.client.provider.keycloak.issuer-uri":                       p.URL + "/realms/cbio",
		"spring.security.oauth2.client.provider.keycloak.user-name-attribute":              "email",
	})
	if err != nil || cfg == nil {
		t.Fatalf("config: %v", err)
	}
	login := newOAuth2Login(cfg)
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := GetUser(r.Context())
		r.ParseForm()
		fmt.Fprintf(w, "%s %s user=%s roles=%s form=%s", r.Method, r.URL.Path, u.Name, strings.Join(u.Roles, ","), r.PostForm.Encode())
	})
	srv := httptest.NewServer(login.middleware(echo))
	t.Cleanup(srv.Close)
	return srv
}

func browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// mustBody reads a response body, failing the test on a request error.
func mustBody(t *testing.T) func(*http.Response, error) string {
	return func(resp *http.Response, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
}

func TestOAuth2LoginFlow(t *testing.T) {
	p := newFakeProvider(t)
	p.roles = []string{"study_a", "study_b"}
	portal := loginPortal(t, p)

	// The API refuses unauthenticated requests without redirecting.
	resp, err := http.Get(portal.URL + "/api/studies")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API: %v %v, want 401", resp.StatusCode, err)
	}
	// Open paths need no login.
	if got := mustBody(t)(http.Get(portal.URL+"/api/health")); !strings.HasPrefix(got, "GET /api/health user=") {
		t.Fatalf("health: %q", got)
	}

	// A page goes through the provider and lands where it started, logged in.
	c := browser(t)
	got := mustBody(t)(c.Get(portal.URL+"/study/summary?id=study_a"))
	if want := "GET /study/summary user=testuser@example.org roles=study_a,study_b"; !strings.HasPrefix(got, want) {
		t.Fatalf("after login got %q, want prefix %q", got, want)
	}
	if p.tokenReq.Get("redirect_uri") != portal.URL+"/login/oauth2/code/keycloak" {
		t.Errorf("redirect_uri %q", p.tokenReq.Get("redirect_uri"))
	}
	// The session cookie authenticates API calls.
	if got := mustBody(t)(c.Get(portal.URL+"/api/studies")); !strings.Contains(got, "user=testuser@example.org") {
		t.Fatalf("API with session: %q", got)
	}

	// Logout ends the session at the provider too.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err = c.Get(portal.URL + "/logout")
	if err != nil || !strings.HasPrefix(resp.Header.Get("Location"), p.URL+"/realms/cbio/logout?id_token_hint=") {
		t.Fatalf("logout redirect %q", resp.Header.Get("Location"))
	}
	resp, _ = c.Get(portal.URL + "/api/studies")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: %d, want 401", resp.StatusCode)
	}
}

func TestOAuth2LoginReplaysPostedForm(t *testing.T) {
	p := newFakeProvider(t)
	portal := loginPortal(t, p)
	c := browser(t)
	got := mustBody(t)(c.PostForm(portal.URL+"/results", url.Values{"gene_list": {"TP53"}, "cancer_study_list": {"study_a"}}))
	if want := "POST /results user=testuser@example.org roles= form=cancer_study_list=study_a&gene_list=TP53"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Only once: the next visit is a plain GET.
	if got := mustBody(t)(c.Get(portal.URL+"/results")); !strings.HasPrefix(got, "GET /results") {
		t.Fatalf("second visit %q", got)
	}
}

func TestOAuth2LoginRejectsBadTokens(t *testing.T) {
	for name, tamper := range map[string]func(map[string]any){
		"wrong audience": func(c map[string]any) { c["aud"] = "other" },
		"wrong issuer":   func(c map[string]any) { c["iss"] = "https://evil.example" },
		"expired":        func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"wrong nonce":    func(c map[string]any) { c["nonce"] = "x" },
	} {
		t.Run(name, func(t *testing.T) {
			p := newFakeProvider(t)
			p.tamper = tamper
			portal := loginPortal(t, p)
			c := browser(t)
			c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				if req.URL.Path == "/login" && req.URL.RawQuery == "logout_failure" {
					return http.ErrUseLastResponse
				}
				return nil
			}
			resp, err := c.Get(portal.URL + "/")
			if err != nil || resp.Header.Get("Location") != "/login?logout_failure" {
				t.Fatalf("got %v %q, want redirect to /login?logout_failure", err, resp.Header.Get("Location"))
			}
		})
	}
}

func TestOAuth2LoginRejectsForgedSignature(t *testing.T) {
	p := newFakeProvider(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys := newJWKS(p.URL+"/realms/cbio/certs", http.DefaultClient)
	forged := (&fakeProvider{key: other}).sign(t, map[string]any{"sub": "u1"})
	if _, err := keys.verify(forged); err == nil {
		t.Fatal("token signed with another key verified")
	}
	if _, err := keys.verify(p.sign(t, map[string]any{"sub": "u1"})); err != nil {
		t.Fatalf("genuine token: %v", err)
	}
}

func TestLoadOAuth2Config(t *testing.T) {
	if c, err := loadOAuth2Config(map[string]string{"authenticate": "false"}); c != nil || err != nil {
		t.Fatalf("authenticate=false: %v %v", c, err)
	}
	for _, method := range []string{"saml", "optional_oauth2", "ad"} {
		if _, err := loadOAuth2Config(map[string]string{"authenticate": method}); err == nil {
			t.Errorf("authenticate=%s accepted; an unsupported method must not serve the portal open", method)
		}
	}
	if _, err := loadOAuth2Config(map[string]string{"authenticate": "oauth2"}); err == nil {
		t.Error("oauth2 without a client registration accepted")
	}
}

func TestClaimRoles(t *testing.T) {
	path := []string{"resource_access", "cbioportal", "roles"}
	claims := map[string]any{"resource_access": map[string]any{"cbioportal": map[string]any{"roles": []any{"a", "b"}}}}
	if got := claimRoles(claims, path); strings.Join(got, ",") != "a,b" {
		t.Errorf("array: %v", got)
	}
	claims = map[string]any{"resource_access": map[string]any{"cbioportal": map[string]any{"roles": `["c"]`}}}
	if got := claimRoles(claims, path); strings.Join(got, ",") != "c" {
		t.Errorf("JSON string: %v", got)
	}
	if got := claimRoles(map[string]any{}, path); got != nil {
		t.Errorf("missing: %v", got)
	}
}
