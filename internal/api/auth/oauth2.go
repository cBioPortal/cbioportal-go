package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// OAuth2 login, as the Java portal's Spring Security oauth2Login: an OpenID
// Connect authorization-code flow against the client registrations of the
// spring.security.oauth2.client.* properties (Keycloak, Google, …).
//
// authenticate=oauth2 makes the portal private:
//   - Unauthenticated /api/** requests get an empty 401; other pages redirect
//     to /login, which redirects to /oauth2/authorization/{registrationId}
//     (or lists the registrations when there are several) and on to the
//     provider. The provider returns to /login/oauth2/code/{registrationId},
//     which starts a session and sends the browser back to the page it first
//     asked for (re-posting a form that was posted before the login).
//   - /logout ends the session and the provider's (RP-initiated logout).
//   - With dat.method=oauth2, API requests with an Authorization header are
//     authenticated by their data access token instead (dat.go).
//   - Roles come from the ID token and userinfo claims at
//     spring.security.oauth2.client.jwt-roles-path (default
//     resource_access::cbioportal::roles) and decide which studies the user
//     may read (see access.go).
//
// authenticate=optional_oauth2 keeps the portal open, as Java's
// OptionalOAuth2SecurityConfig: every request is served, anonymous ones as
// with authenticate=false; signing in (the frontend opens /login) makes the
// user known to the API, e.g. to save virtual studies and settings. After the
// login the browser lands on /, and /logout returns there. Study
// authorization applies only with security.method_authorization_enabled=true.
//
// Sessions and logins in progress live in a loginStore: memory by default,
// Redis with spring.session.store-type=redis.

const (
	sessionCookie  = "JSESSIONID"
	loginCookie    = "CBIOPORTAL_LOGIN"
	sessionTimeout = 30 * time.Minute
	loginTimeout   = 10 * time.Minute
	// sessionRefresh is how stale a session's expiry may get before a request
	// extends it, sparing the store a write per request.
	sessionRefresh = time.Minute
)

// oauth2Registration is one spring.security.oauth2.client.registration.{id}
// with its provider.
type oauth2Registration struct {
	ID             string
	ClientID       string
	ClientSecret   string
	Scopes         []string
	RedirectURI    string // may contain {baseUrl}, {action} and {registrationId}
	ClientAuthPost bool   // client_secret_post instead of client_secret_basic
	IssuerURI      string
	// Discover is set when issuer-uri is configured: the provider's endpoints
	// then come from its OpenID discovery document.
	Discover          bool
	AuthorizationURI  string
	TokenURI          string
	UserInfoURI       string
	JWKSetURI         string
	UserNameAttribute string
	// Issuers are further ID token issuers accepted besides IssuerURI.
	Issuers []string
}

type oauth2Config struct {
	Method        string // oauth2 or optional_oauth2
	Registrations []*oauth2Registration
	RolesPath     []string
	// ForceRedirect sends /login straight to the provider when there is one
	// registration (security.force_redirect_on_one_idp, default true).
	ForceRedirect bool
	// MethodAuthorization applies study authorization: always with oauth2,
	// with optional_oauth2 when security.method_authorization_enabled=true.
	MethodAuthorization bool
}

func (c *oauth2Config) optional() bool { return c.Method == "optional_oauth2" }

func (c *oauth2Config) registration(id string) *oauth2Registration {
	for _, r := range c.Registrations {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// commonProviders are the defaults Spring's CommonOAuth2Provider gives a
// registration whose provider (or id) names it; set properties override them.
var commonProviders = map[string]oauth2Registration{
	"google": {
		Scopes:            []string{"openid", "profile", "email"},
		AuthorizationURI:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURI:          "https://www.googleapis.com/oauth2/v4/token",
		JWKSetURI:         "https://www.googleapis.com/oauth2/v3/certs",
		IssuerURI:         "https://accounts.google.com",
		UserInfoURI:       "https://www.googleapis.com/oauth2/v3/userinfo",
		UserNameAttribute: "sub",
		// Google's documented alternative form of its issuer.
		Issuers: []string{"accounts.google.com"},
	},
}

// Spring's CommonOAuth2Provider entries without OpenID Connect, which this
// server's login (it verifies an ID token) does not support.
var nonOIDCProviders = map[string]bool{"github": true, "facebook": true}

// relaxedName is the canonical (lower-case, dash-separated) form of a
// property name segment, as Spring's relaxed binding reads clientId,
// client_id and client-id alike.
func relaxedName(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '_':
			b.WriteByte('-')
		case unicode.IsUpper(r):
			if i > 0 && s[i-1] != '-' && s[i-1] != '_' && s[i-1] != '.' {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// loadOAuth2Config reads the spring.security.oauth2.client.* properties. It
// returns nil when authenticate asks for no login, and an error for an
// authentication method this server does not implement, so a portal meant to
// be private never starts open.
func loadOAuth2Config(props map[string]string) (*oauth2Config, error) {
	method := strings.TrimSpace(props["authenticate"])
	switch method {
	case "", "false", "noauthsessionservice":
		return nil, nil
	case "oauth2", "optional_oauth2":
	default:
		return nil, fmt.Errorf("authenticate=%s is not supported (use oauth2, optional_oauth2 or false)", method)
	}
	flag := func(k string, def bool) (bool, error) {
		v := strings.TrimSpace(props[k])
		if v == "" {
			return def, nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("%s=%s is not true or false", k, v)
		}
		return b, nil
	}
	if authz, err := flag("authorization", false); err != nil {
		return nil, err
	} else if authz {
		return nil, errors.New("authorization=true (users and authorities from the database) is not supported")
	}
	c := &oauth2Config{Method: method, MethodAuthorization: method == "oauth2"}
	var err error
	if c.ForceRedirect, err = flag("security.force_redirect_on_one_idp", true); err != nil {
		return nil, err
	}
	if c.optional() {
		if c.MethodAuthorization, err = flag("security.method_authorization_enabled", false); err != nil {
			return nil, err
		}
	}

	// Registration and provider settings by id, their names relaxed.
	const prefix = "spring.security.oauth2.client."
	regs, providers := map[string]map[string]string{}, map[string]map[string]string{}
	for k, v := range props {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		kind, rest, _ := strings.Cut(rest, ".")
		id, name, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		var m map[string]map[string]string
		switch kind {
		case "registration":
			m = regs
		case "provider":
			m = providers
		default:
			continue
		}
		if m[id] == nil {
			m[id] = map[string]string{}
		}
		m[id][relaxedName(name)] = strings.TrimSpace(v)
	}
	var ids []string
	for id, r := range regs {
		if r["client-id"] != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("authenticate=%s needs %sregistration.<id>.client-id", method, prefix)
	}
	slices.Sort(ids)
	for _, id := range ids {
		r := regs[id]
		provider := r["provider"]
		if provider == "" {
			provider = id
		}
		p := providers[provider]
		if nonOIDCProviders[strings.ToLower(provider)] {
			return nil, fmt.Errorf("registration %s: provider %s does not support OpenID Connect", id, provider)
		}
		reg := commonProviders[strings.ToLower(provider)]
		reg.ID, reg.ClientID, reg.ClientSecret = id, r["client-id"], r["client-secret"]
		reg.RedirectURI = r["redirect-uri"]
		reg.ClientAuthPost = r["client-authentication-method"] == "client_secret_post"
		if v := p["issuer-uri"]; v != "" {
			reg.IssuerURI, reg.Discover, reg.Issuers = strings.TrimSuffix(v, "/"), true, nil
		}
		for _, e := range []struct {
			dst *string
			key string
		}{
			{&reg.AuthorizationURI, "authorization-uri"}, {&reg.TokenURI, "token-uri"},
			{&reg.UserInfoURI, "user-info-uri"}, {&reg.JWKSetURI, "jwk-set-uri"},
			{&reg.UserNameAttribute, "user-name-attribute"},
		} {
			if v := p[e.key]; v != "" {
				*e.dst = v
			}
		}
		if v := r["scope"]; v != "" {
			reg.Scopes = nil
			for _, s := range strings.Split(v, ",") {
				if s = strings.TrimSpace(s); s != "" {
					reg.Scopes = append(reg.Scopes, s)
				}
			}
		}
		reg.Scopes = slices.Clone(reg.Scopes)
		if len(reg.Scopes) == 0 {
			reg.Scopes = []string{"openid"}
		}
		if reg.RedirectURI == "" {
			reg.RedirectURI = "{baseUrl}/{action}/oauth2/code/{registrationId}"
		}
		if reg.UserNameAttribute == "" {
			reg.UserNameAttribute = "sub"
		}
		if !reg.Discover && (reg.AuthorizationURI == "" || reg.TokenURI == "" || reg.JWKSetURI == "") {
			return nil, fmt.Errorf("registration %s needs the provider's issuer-uri (or authorization-uri, token-uri and jwk-set-uri)", id)
		}
		c.Registrations = append(c.Registrations, &reg)
	}
	rolesPath := strings.TrimSpace(props["spring.security.oauth2.client.jwt-roles-path"])
	if rolesPath == "" {
		rolesPath = "resource_access::cbioportal::roles"
	}
	c.RolesPath = strings.Split(rolesPath, "::")
	return c, nil
}

// providerMeta is the provider's OpenID configuration: discovered from
// issuer-uri, with explicitly configured endpoints taking precedence.
type providerMeta struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

// authSession is a login session as kept in the loginStore.
type authSession struct {
	User         string    `json:"user"`
	Roles        []string  `json:"roles,omitempty"`
	Registration string    `json:"registration"`
	IDToken      string    `json:"idToken,omitempty"`
	Expires      time.Time `json:"expires"`
	// Replay is a form posted before the login, re-posted when the browser
	// returns to its URL.
	Replay *savedRequest `json:"replay,omitempty"`
}

type savedRequest struct {
	URL  string     `json:"url"` // path and query
	Form url.Values `json:"form,omitempty"`
	Post bool       `json:"post,omitempty"`
}

// loginAttempt is a login in progress, keyed by the browser's login cookie:
// the page to return to and, once the provider is asked, the state and nonce
// its answer must carry.
type loginAttempt struct {
	Registration string        `json:"registration,omitempty"`
	State        string        `json:"state,omitempty"`
	Nonce        string        `json:"nonce,omitempty"`
	Target       *savedRequest `json:"target,omitempty"`
}

type providerState struct {
	meta *providerMeta
	keys *jwks
}

type oauth2Login struct {
	cfg    *oauth2Config
	client *http.Client
	store  loginStore

	metaMu    sync.Mutex
	providers map[string]providerState // by registration id

	// dat authenticates API requests that carry an Authorization header
	// (dat.method=oauth2); nil leaves them to the session.
	dat *datProvider
}

func newOAuth2Login(cfg *oauth2Config, store loginStore) *oauth2Login {
	if store == nil {
		store = newMemoryLoginStore()
	}
	return &oauth2Login{
		cfg:       cfg,
		client:    &http.Client{Timeout: 15 * time.Second},
		store:     store,
		providers: map[string]providerState{},
	}
}

// provider returns the registration's provider metadata, discovering it on
// first use (the provider may start after the portal).
func (o *oauth2Login) provider(reg *oauth2Registration) (*providerMeta, *jwks, error) {
	o.metaMu.Lock()
	defer o.metaMu.Unlock()
	if p, ok := o.providers[reg.ID]; ok {
		return p.meta, p.keys, nil
	}
	m := &providerMeta{}
	if reg.Discover {
		resp, err := o.client.Get(reg.IssuerURI + "/.well-known/openid-configuration")
		if err != nil {
			return nil, nil, fmt.Errorf("OpenID discovery: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, nil, fmt.Errorf("OpenID discovery: HTTP %d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(m); err != nil {
			return nil, nil, fmt.Errorf("OpenID discovery: %w", err)
		}
		if strings.TrimSuffix(m.Issuer, "/") != reg.IssuerURI {
			return nil, nil, fmt.Errorf("OpenID discovery: issuer %q does not match issuer-uri %q", m.Issuer, reg.IssuerURI)
		}
	}
	for _, e := range []struct {
		dst *string
		v   string
	}{
		{&m.AuthorizationEndpoint, reg.AuthorizationURI}, {&m.TokenEndpoint, reg.TokenURI},
		{&m.UserInfoEndpoint, reg.UserInfoURI}, {&m.JWKSURI, reg.JWKSetURI},
	} {
		if e.v != "" {
			*e.dst = e.v
		}
	}
	if m.Issuer == "" {
		m.Issuer = reg.IssuerURI
	}
	p := providerState{m, newJWKS(m.JWKSURI, o.client)}
	o.providers[reg.ID] = p
	return p.meta, p.keys, nil
}

// publicPath reports the paths open without a login, as in the Java
// portal's API and OAuth2 security chains.
func publicPath(p string) bool {
	switch p {
	case "/health", "/api/health", "/login", "/logout", "/api/swagger-ui.html":
		return true
	}
	for _, prefix := range []string{"/images/", "/oauth2/authorization/", "/login/oauth2/code/",
		"/api/public_virtual_studies", "/api/swagger-resources/", "/api/cache"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// middleware authenticates every request from its session cookie and routes
// the login endpoints. With oauth2, unauthenticated requests outside the
// public paths are refused (API) or sent to the login (pages); with
// optional_oauth2 they are served anonymously.
func (o *oauth2Login) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/login":
			o.loginPage(w, r)
			return
		case p == "/logout":
			o.logout(w, r)
			return
		case strings.HasPrefix(p, "/oauth2/authorization/"):
			if reg := o.cfg.registration(strings.TrimPrefix(p, "/oauth2/authorization/")); reg != nil {
				o.authorize(w, r, reg)
				return
			}
		case strings.HasPrefix(p, "/login/oauth2/code/"):
			if reg := o.cfg.registration(strings.TrimPrefix(p, "/login/oauth2/code/")); reg != nil {
				o.callback(w, r, reg)
				return
			}
		}
		if o.dat != nil && core.IsAPIPath(p) && r.Header.Get("Authorization") != "" {
			o.dat.tokenAuth(w, r, next)
			return
		}
		sess, id, err := o.session(r)
		if err != nil {
			log.Printf("oauth2: login session: %v", err)
			if !o.cfg.optional() {
				core.ReadBody(r, nil)
				http.Error(w, "login sessions unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		if sess != nil {
			r = o.replay(r, sess, id)
			next.ServeHTTP(w, core.WithUser(r, core.User{Name: sess.User, Roles: sess.Roles}))
			return
		}
		switch {
		case o.cfg.optional(), publicPath(p):
			next.ServeHTTP(w, r)
		case core.IsAPIPath(p):
			core.ReadBody(r, nil)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			o.saveRequest(w, r)
			http.Redirect(w, r, "/login", http.StatusFound)
		}
	})
}

func sessionKey(id string) string { return "session:" + id }
func attemptKey(id string) string { return "attempt:" + id }

func (o *oauth2Login) put(ctx context.Context, key string, v any, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return o.store.set(ctx, key, b, ttl)
}

// session returns the live session of the request's cookie and its id,
// extending it.
func (o *oauth2Login) session(r *http.Request) (*authSession, string, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, "", nil
	}
	b, ok, err := o.store.get(r.Context(), sessionKey(c.Value))
	if err != nil || !ok {
		return nil, "", err
	}
	sess := &authSession{}
	if err := json.Unmarshal(b, sess); err != nil {
		return nil, "", fmt.Errorf("stored session: %w", err)
	}
	now := time.Now()
	if now.After(sess.Expires) {
		return nil, "", nil
	}
	if sess.Expires.Sub(now) < sessionTimeout-sessionRefresh {
		sess.Expires = now.Add(sessionTimeout)
		if err := o.put(r.Context(), sessionKey(c.Value), sess, sessionTimeout); err != nil {
			log.Printf("oauth2: extending session: %v", err)
		}
	}
	return sess, c.Value, nil
}

// replay turns the browser's return to a page whose form was posted before
// the login back into that POST.
func (o *oauth2Login) replay(r *http.Request, sess *authSession, id string) *http.Request {
	saved := sess.Replay
	if saved == nil || r.Method != http.MethodGet || r.URL.RequestURI() != saved.URL {
		return r
	}
	sess.Replay = nil
	if err := o.put(r.Context(), sessionKey(id), sess, time.Until(sess.Expires)); err != nil {
		log.Printf("oauth2: saving session: %v", err)
	}
	body := saved.Form.Encode()
	r2 := r.Clone(r.Context())
	r2.Method = http.MethodPost
	r2.Body = io.NopCloser(strings.NewReader(body))
	r2.ContentLength = int64(len(body))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r2
}

// saveRequest remembers the page an unauthenticated browser asked for (with
// its form, if posted), to return to it after the login.
func (o *oauth2Login) saveRequest(w http.ResponseWriter, r *http.Request) {
	saved := &savedRequest{URL: r.URL.RequestURI()}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err == nil {
			saved.Form, saved.Post = r.PostForm, true
		}
	} else {
		core.ReadBody(r, nil)
	}
	id := randomToken()
	if err := o.put(r.Context(), attemptKey(id), loginAttempt{Target: saved}, loginTimeout); err != nil {
		log.Printf("oauth2: saving request: %v", err)
		return
	}
	o.setCookie(w, r, loginCookie, id, loginTimeout)
}

// loginPage sends the browser to the provider when there is only one, as the
// Java portal's LoginPageController, or lists the registrations to sign in
// with (and explains a refused login).
func (o *oauth2Login) loginPage(w http.ResponseWriter, r *http.Request) {
	failed := r.URL.Query().Has("logout_failure")
	if len(o.cfg.Registrations) == 1 && !failed && o.cfg.ForceRedirect {
		http.Redirect(w, r, "/oauth2/authorization/"+o.cfg.Registrations[0].ID, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	status := http.StatusOK
	if failed {
		status = http.StatusUnauthorized
	}
	w.WriteHeader(status)
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>cBioPortal login</title></head>` +
		`<body style="font-family:sans-serif;margin:3em"><h2>cBioPortal</h2>`)
	if failed {
		b.WriteString(`<p>You are not authorized to access this resource.</p>`)
	}
	for _, reg := range o.cfg.Registrations {
		id := html.EscapeString(reg.ID)
		fmt.Fprintf(&b, `<p><a href="/oauth2/authorization/%s">Sign in with: %s</a></p>`, id, id)
	}
	b.WriteString(`</body></html>`)
	io.WriteString(w, b.String())
}

// authorize starts the authorization-code flow.
func (o *oauth2Login) authorize(w http.ResponseWriter, r *http.Request, reg *oauth2Registration) {
	meta, _, err := o.provider(reg)
	if err != nil {
		log.Printf("oauth2: %s: %v", reg.ID, err)
		http.Error(w, "login provider unavailable", http.StatusBadGateway)
		return
	}
	attempt := loginAttempt{Registration: reg.ID, State: randomToken(), Nonce: randomToken()}
	id := ""
	if c, err := r.Cookie(loginCookie); err == nil && c.Value != "" {
		id = c.Value
		var prev loginAttempt
		if b, ok, err := o.store.take(r.Context(), attemptKey(id)); err == nil && ok && json.Unmarshal(b, &prev) == nil {
			attempt.Target = prev.Target
		}
	}
	id = randomToken()
	if err := o.put(r.Context(), attemptKey(id), attempt, loginTimeout); err != nil {
		log.Printf("oauth2: saving login: %v", err)
		http.Error(w, "login sessions unavailable", http.StatusServiceUnavailable)
		return
	}
	o.setCookie(w, r, loginCookie, id, loginTimeout)

	q := url.Values{
		"response_type": {"code"},
		"client_id":     {reg.ClientID},
		"scope":         {strings.Join(reg.Scopes, " ")},
		"state":         {attempt.State},
		"redirect_uri":  {o.redirectURI(r, reg)},
		"nonce":         {attempt.Nonce},
	}
	sep := "?"
	if strings.Contains(meta.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	http.Redirect(w, r, meta.AuthorizationEndpoint+sep+q.Encode(), http.StatusFound)
}

// callback completes the login: it exchanges the code, verifies the ID token
// and starts the session.
func (o *oauth2Login) callback(w http.ResponseWriter, r *http.Request, reg *oauth2Registration) {
	fail := func(format string, args ...any) {
		core.LoginsTotal.Inc("failure")
		log.Printf("oauth2: %s: login failed: "+format, append([]any{reg.ID}, args...)...)
		http.Redirect(w, r, "/login?logout_failure", http.StatusFound)
	}
	c, err := r.Cookie(loginCookie)
	if err != nil || c.Value == "" {
		fail("no login in progress")
		return
	}
	o.setCookie(w, r, loginCookie, "", -1)
	b, ok, err := o.store.take(r.Context(), attemptKey(c.Value))
	if err != nil {
		fail("%v", err)
		return
	}
	var attempt loginAttempt
	if !ok || json.Unmarshal(b, &attempt) != nil || attempt.State == "" {
		fail("login expired")
		return
	}
	q := r.URL.Query()
	if attempt.Registration != reg.ID {
		fail("login started with %s", attempt.Registration)
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(attempt.State)) != 1 {
		fail("state mismatch")
		return
	}
	if e := q.Get("error"); e != "" {
		fail("provider error %s", e)
		return
	}
	user, idToken, err := o.exchange(r, reg, q.Get("code"), attempt.Nonce)
	if err != nil {
		fail("%v", err)
		return
	}

	sess := &authSession{
		User: user.Name, Roles: user.Roles, Registration: reg.ID, IDToken: idToken,
		Expires: time.Now().Add(sessionTimeout),
	}
	target := "/"
	if attempt.Target != nil {
		target = attempt.Target.URL
		if attempt.Target.Post {
			sess.Replay = attempt.Target
		}
	}
	id := randomToken()
	if err := o.put(r.Context(), sessionKey(id), sess, sessionTimeout); err != nil {
		fail("saving session: %v", err)
		return
	}
	o.setCookie(w, r, sessionCookie, id, 0)
	core.LoginsTotal.Inc("success")
	http.Redirect(w, r, target, http.StatusFound)
}

// exchange redeems an authorization code and returns the user it identifies.
func (o *oauth2Login) exchange(r *http.Request, reg *oauth2Registration, code, nonce string) (core.User, string, error) {
	meta, keys, err := o.provider(reg)
	if err != nil {
		return core.User{}, "", err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {o.redirectURI(r, reg)}}
	if reg.ClientAuthPost {
		form.Set("client_id", reg.ClientID)
		form.Set("client_secret", reg.ClientSecret)
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, meta.TokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if !reg.ClientAuthPost {
		req.SetBasicAuth(url.QueryEscape(reg.ClientID), url.QueryEscape(reg.ClientSecret))
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return core.User{}, "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || resp.StatusCode != http.StatusOK {
		return core.User{}, "", fmt.Errorf("token request: HTTP %d %s", resp.StatusCode, tok.Error)
	}
	if tok.IDToken == "" {
		return core.User{}, "", errors.New("token response has no id_token (scope must include openid)")
	}
	claims, err := keys.verify(tok.IDToken)
	if err != nil {
		return core.User{}, "", err
	}
	issuers := append([]string{meta.Issuer}, reg.Issuers...)
	if err := checkIDToken(claims, issuers, reg.ClientID, nonce, time.Now()); err != nil {
		return core.User{}, "", err
	}
	var userinfo map[string]any
	if meta.UserInfoEndpoint != "" && tok.AccessToken != "" {
		userinfo, err = o.userInfo(r.Context(), meta.UserInfoEndpoint, tok.AccessToken)
		if err != nil {
			return core.User{}, "", err
		}
		if sub, _ := userinfo["sub"].(string); sub != claims["sub"] {
			return core.User{}, "", errors.New("userinfo subject does not match the ID token")
		}
	}
	return o.userFromClaims(reg, claims, userinfo), tok.IDToken, nil
}

func (o *oauth2Login) userInfo(ctx context.Context, endpoint, accessToken string) (map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo: HTTP %d", resp.StatusCode)
	}
	var claims map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&claims); err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	return claims, nil
}

// checkIDToken validates the claims of a signature-checked ID token: issued
// by one of issuers, for clientID, unexpired, with the login's nonce.
func checkIDToken(claims map[string]any, issuers []string, clientID, nonce string, now time.Time) error {
	if iss, _ := claims["iss"].(string); !slices.Contains(issuers, iss) {
		return fmt.Errorf("ID token issuer %q is not %q", iss, issuers[0])
	}
	var aud []string
	switch v := claims["aud"].(type) {
	case string:
		aud = []string{v}
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok {
				aud = append(aud, s)
			}
		}
	}
	if !slices.Contains(aud, clientID) {
		return errors.New("ID token is not for this client")
	}
	const skew = time.Minute
	exp, ok := claims["exp"].(float64)
	if !ok || now.After(time.Unix(int64(exp), 0).Add(skew)) {
		return errors.New("ID token expired")
	}
	if n, _ := claims["nonce"].(string); subtle.ConstantTimeCompare([]byte(n), []byte(nonce)) != 1 {
		return errors.New("ID token nonce mismatch")
	}
	return nil
}

// userFromClaims names the user by the user-name-attribute claim and
// collects the roles at the roles path of the userinfo and ID token claims.
func (o *oauth2Login) userFromClaims(reg *oauth2Registration, idClaims, userinfo map[string]any) core.User {
	attrs := map[string]any{}
	for k, v := range idClaims {
		attrs[k] = v
	}
	for k, v := range userinfo {
		attrs[k] = v
	}
	name := fmt.Sprint(attrs[reg.UserNameAttribute])
	if attrs[reg.UserNameAttribute] == nil {
		name, _ = attrs["sub"].(string)
	}
	var roles []string
	for _, claims := range []map[string]any{userinfo, idClaims} {
		for _, role := range claimRoles(claims, o.cfg.RolesPath) {
			if !slices.Contains(roles, role) {
				roles = append(roles, role)
			}
		}
	}
	return core.User{Name: name, Roles: roles}
}

// claimRoles walks path through the claims, as Java's
// ClaimRoleExtractorUtil: the leaf is a string array, or a string holding one.
func claimRoles(claims map[string]any, path []string) []string {
	var node any = claims
	for _, key := range path {
		m, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = m[key]
	}
	if s, ok := node.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(s), &parsed) != nil {
			return nil
		}
		node = parsed
	}
	arr, _ := node.([]any)
	var roles []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			roles = append(roles, s)
		}
	}
	return roles
}

// logout ends the session and returns to the portal. With oauth2 it ends the
// provider's session too when the provider supports it; optional_oauth2
// returns to / directly, as Java's logoutSuccessUrl("/").
func (o *oauth2Login) logout(w http.ResponseWriter, r *http.Request) {
	var sess authSession
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if b, ok, err := o.store.take(r.Context(), sessionKey(c.Value)); err != nil {
			log.Printf("oauth2: ending session: %v", err)
		} else if ok {
			json.Unmarshal(b, &sess)
		}
	}
	o.setCookie(w, r, sessionCookie, "", -1)
	target := "/"
	if reg := o.cfg.registration(sess.Registration); reg != nil && !o.cfg.optional() && sess.IDToken != "" {
		if meta, _, err := o.provider(reg); err == nil && meta.EndSessionEndpoint != "" {
			q := url.Values{"id_token_hint": {sess.IDToken}, "post_logout_redirect_uri": {core.BaseURL(r)}}
			target = meta.EndSessionEndpoint + "?" + q.Encode()
		}
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (o *oauth2Login) redirectURI(r *http.Request, reg *oauth2Registration) string {
	return strings.NewReplacer("{baseUrl}", core.BaseURL(r), "{action}", "login", "{registrationId}", reg.ID).Replace(reg.RedirectURI)
}

func (o *oauth2Login) setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge time.Duration) {
	c := &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: strings.HasPrefix(core.BaseURL(r), "https:"),
	}
	switch {
	case maxAge < 0:
		c.MaxAge = -1
	case maxAge > 0:
		c.MaxAge = int(maxAge.Seconds())
	}
	http.SetCookie(w, c)
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
