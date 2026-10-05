package api

import (
	"bytes"
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
	"strings"
	"sync"
	"time"
)

// OAuth2 login (authenticate=oauth2), as the Java portal's Spring Security
// oauth2Login: an OpenID Connect authorization-code flow against one provider
// (Keycloak in cBioPortal deployments), configured with the same
// spring.security.oauth2.client.* properties.
//
//   - Unauthenticated /api/** requests get an empty 401; other pages redirect
//     to /login, which redirects to /oauth2/authorization/{registrationId}
//     and on to the provider. The provider returns to
//     /login/oauth2/code/{registrationId}, which starts a session and sends
//     the browser back to the page it first asked for (re-posting a form
//     that was posted before the login).
//   - /logout ends the session and the provider's (RP-initiated logout).
//   - With dat.method=oauth2, API requests with an Authorization header are
//     authenticated by their data access token instead (dat.go).
//   - Roles come from the ID token and userinfo claims at
//     spring.security.oauth2.client.jwt-roles-path (default
//     resource_access::cbioportal::roles) and decide which studies the user
//     may read (see access.go).
//
// Sessions live in memory: a restart signs everyone out.

const (
	sessionCookie  = "JSESSIONID"
	loginCookie    = "CBIOPORTAL_LOGIN"
	sessionTimeout = 30 * time.Minute
	loginTimeout   = 10 * time.Minute
)

type oauth2Config struct {
	RegistrationID    string
	ClientID          string
	ClientSecret      string
	Scopes            []string
	RedirectURI       string // may contain {baseUrl} and {registrationId}
	ClientAuthPost    bool   // client_secret_post instead of client_secret_basic
	IssuerURI         string
	AuthorizationURI  string
	TokenURI          string
	UserInfoURI       string
	JWKSetURI         string
	UserNameAttribute string
	RolesPath         []string
}

// loadOAuth2Config reads the spring.security.oauth2.client.* properties. It
// returns nil when authenticate is not oauth2, and an error for an
// authentication method this server does not implement, so a portal meant to
// be private never starts open.
func loadOAuth2Config(props map[string]string) (*oauth2Config, error) {
	switch method := strings.TrimSpace(props["authenticate"]); method {
	case "", "false", "noauthsessionservice":
		return nil, nil
	case "oauth2":
	default:
		return nil, fmt.Errorf("authenticate=%s is not supported (use oauth2 or false)", method)
	}
	const reg = "spring.security.oauth2.client.registration."
	var ids []string
	for k := range props {
		if id, ok := strings.CutSuffix(strings.TrimPrefix(k, reg), ".client-id"); ok && strings.HasPrefix(k, reg) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("authenticate=oauth2 needs " + reg + "<id>.client-id")
	}
	slices.Sort(ids)
	id := ids[0]
	r := func(k string) string { return strings.TrimSpace(props[reg+id+"."+k]) }
	provider := r("provider")
	if provider == "" {
		provider = id
	}
	p := func(k string) string {
		return strings.TrimSpace(props["spring.security.oauth2.client.provider."+provider+"."+k])
	}
	c := &oauth2Config{
		RegistrationID:    id,
		ClientID:          r("client-id"),
		ClientSecret:      r("client-secret"),
		RedirectURI:       r("redirect-uri"),
		ClientAuthPost:    r("client-authentication-method") == "client_secret_post",
		IssuerURI:         strings.TrimSuffix(p("issuer-uri"), "/"),
		AuthorizationURI:  p("authorization-uri"),
		TokenURI:          p("token-uri"),
		UserInfoURI:       p("user-info-uri"),
		JWKSetURI:         p("jwk-set-uri"),
		UserNameAttribute: p("user-name-attribute"),
	}
	for _, s := range strings.Split(r("scope"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.Scopes = append(c.Scopes, s)
		}
	}
	if len(c.Scopes) == 0 {
		c.Scopes = []string{"openid"}
	}
	if c.RedirectURI == "" {
		c.RedirectURI = "{baseUrl}/login/oauth2/code/{registrationId}"
	}
	if c.UserNameAttribute == "" {
		c.UserNameAttribute = "sub"
	}
	rolesPath := strings.TrimSpace(props["spring.security.oauth2.client.jwt-roles-path"])
	if rolesPath == "" {
		rolesPath = "resource_access::cbioportal::roles"
	}
	c.RolesPath = strings.Split(rolesPath, "::")
	if c.IssuerURI == "" && (c.AuthorizationURI == "" || c.TokenURI == "" || c.JWKSetURI == "") {
		return nil, errors.New("authenticate=oauth2 needs the provider's issuer-uri (or authorization-uri, token-uri and jwk-set-uri)")
	}
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

type authSession struct {
	user    User
	idToken string
	expires time.Time
	// replay is a form posted before the login, re-posted when the browser
	// returns to its URL.
	replay *savedRequest
}

type savedRequest struct {
	url  string // path and query
	form url.Values
	post bool
}

type loginAttempt struct {
	state, nonce string
	target       *savedRequest
	expires      time.Time
}

type oauth2Login struct {
	cfg    *oauth2Config
	client *http.Client

	metaMu sync.Mutex
	meta   *providerMeta
	keys   *jwks

	mu       sync.Mutex
	sessions map[string]*authSession
	pending  map[string]*loginAttempt // by browser login cookie

	// dat authenticates API requests that carry an Authorization header
	// (dat.method=oauth2); nil leaves them to the session.
	dat *datProvider
}

func newOAuth2Login(cfg *oauth2Config) *oauth2Login {
	return &oauth2Login{
		cfg:      cfg,
		client:   &http.Client{Timeout: 15 * time.Second},
		sessions: map[string]*authSession{},
		pending:  map[string]*loginAttempt{},
	}
}

// provider returns the provider metadata, discovering it on first use (the
// provider may start after the portal).
func (o *oauth2Login) provider() (*providerMeta, *jwks, error) {
	o.metaMu.Lock()
	defer o.metaMu.Unlock()
	if o.meta != nil {
		return o.meta, o.keys, nil
	}
	m := &providerMeta{}
	if o.cfg.IssuerURI != "" {
		resp, err := o.client.Get(o.cfg.IssuerURI + "/.well-known/openid-configuration")
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
		if strings.TrimSuffix(m.Issuer, "/") != o.cfg.IssuerURI {
			return nil, nil, fmt.Errorf("OpenID discovery: issuer %q does not match issuer-uri %q", m.Issuer, o.cfg.IssuerURI)
		}
	}
	for _, e := range []struct {
		dst *string
		v   string
	}{
		{&m.AuthorizationEndpoint, o.cfg.AuthorizationURI}, {&m.TokenEndpoint, o.cfg.TokenURI},
		{&m.UserInfoEndpoint, o.cfg.UserInfoURI}, {&m.JWKSURI, o.cfg.JWKSetURI},
	} {
		if e.v != "" {
			*e.dst = e.v
		}
	}
	if m.Issuer == "" {
		m.Issuer = o.cfg.IssuerURI
	}
	o.meta, o.keys = m, newJWKS(m.JWKSURI, o.client)
	return o.meta, o.keys, nil
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

func isAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || p == "/webservice.do"
}

// middleware authenticates every request from its session cookie and routes
// the login endpoints; unauthenticated requests outside the public paths are
// refused (API) or sent to the login (pages).
func (o *oauth2Login) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/login":
			o.loginPage(w, r)
			return
		case p == "/oauth2/authorization/"+o.cfg.RegistrationID:
			o.authorize(w, r)
			return
		case p == "/login/oauth2/code/"+o.cfg.RegistrationID:
			o.callback(w, r)
			return
		case p == "/logout":
			o.logout(w, r)
			return
		}
		if o.dat != nil && isAPIPath(r.URL.Path) && r.Header.Get("Authorization") != "" {
			o.dat.tokenAuth(w, r, next)
			return
		}
		if sess := o.session(r); sess != nil {
			r = o.replay(r, sess)
			next.ServeHTTP(w, withUser(r, sess.user))
			return
		}
		switch {
		case publicPath(r.URL.Path):
			next.ServeHTTP(w, r)
		case isAPIPath(r.URL.Path):
			readBody(r, nil)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			o.saveRequest(w, r)
			http.Redirect(w, r, "/login", http.StatusFound)
		}
	})
}

// session returns the live session of the request's cookie, extending it.
func (o *oauth2Login) session(r *http.Request) *authSession {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	sess := o.sessions[c.Value]
	if sess == nil {
		return nil
	}
	if time.Now().After(sess.expires) {
		delete(o.sessions, c.Value)
		return nil
	}
	sess.expires = time.Now().Add(sessionTimeout)
	return sess
}

// replay turns the browser's return to a page whose form was posted before
// the login back into that POST.
func (o *oauth2Login) replay(r *http.Request, sess *authSession) *http.Request {
	o.mu.Lock()
	saved := sess.replay
	if saved != nil && r.Method == http.MethodGet && r.URL.RequestURI() == saved.url {
		sess.replay = nil
	} else {
		saved = nil
	}
	o.mu.Unlock()
	if saved == nil {
		return r
	}
	body := saved.form.Encode()
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
	saved := &savedRequest{url: r.URL.RequestURI()}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err == nil {
			saved.form, saved.post = r.PostForm, true
		}
	} else {
		readBody(r, nil)
	}
	id := randomToken()
	o.mu.Lock()
	o.prune()
	o.pending[id] = &loginAttempt{target: saved, expires: time.Now().Add(loginTimeout)}
	o.mu.Unlock()
	o.setCookie(w, r, loginCookie, id, loginTimeout)
}

// loginPage sends the browser to the provider, or explains a refused login.
func (o *oauth2Login) loginPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !q.Has("logout_failure") {
		http.Redirect(w, r, "/oauth2/authorization/"+o.cfg.RegistrationID, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>cBioPortal login</title></head>`+
		`<body style="font-family:sans-serif;margin:3em"><h2>cBioPortal</h2>`+
		`<p>You are not authorized to access this resource.</p>`+
		`<p><a href="/oauth2/authorization/%s">Sign in with: %s</a></p></body></html>`,
		html.EscapeString(o.cfg.RegistrationID), html.EscapeString(o.cfg.RegistrationID))
}

// authorize starts the authorization-code flow.
func (o *oauth2Login) authorize(w http.ResponseWriter, r *http.Request) {
	meta, _, err := o.provider()
	if err != nil {
		log.Printf("oauth2: %v", err)
		http.Error(w, "login provider unavailable", http.StatusBadGateway)
		return
	}
	attempt := &loginAttempt{state: randomToken(), nonce: randomToken(), expires: time.Now().Add(loginTimeout)}
	id := ""
	if c, err := r.Cookie(loginCookie); err == nil {
		id = c.Value
	}
	o.mu.Lock()
	o.prune()
	if prev := o.pending[id]; prev != nil {
		attempt.target = prev.target
	}
	if id == "" || o.pending[id] == nil {
		id = randomToken()
	}
	o.pending[id] = attempt
	o.mu.Unlock()
	o.setCookie(w, r, loginCookie, id, loginTimeout)

	q := url.Values{
		"response_type": {"code"},
		"client_id":     {o.cfg.ClientID},
		"scope":         {strings.Join(o.cfg.Scopes, " ")},
		"state":         {attempt.state},
		"redirect_uri":  {o.redirectURI(r)},
		"nonce":         {attempt.nonce},
	}
	sep := "?"
	if strings.Contains(meta.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	http.Redirect(w, r, meta.AuthorizationEndpoint+sep+q.Encode(), http.StatusFound)
}

// callback completes the login: it exchanges the code, verifies the ID token
// and starts the session.
func (o *oauth2Login) callback(w http.ResponseWriter, r *http.Request) {
	fail := func(format string, args ...any) {
		loginsTotal.Inc("failure")
		log.Printf("oauth2: login failed: "+format, args...)
		http.Redirect(w, r, "/login?logout_failure", http.StatusFound)
	}
	c, err := r.Cookie(loginCookie)
	if err != nil {
		fail("no login in progress")
		return
	}
	o.mu.Lock()
	attempt := o.pending[c.Value]
	delete(o.pending, c.Value)
	o.mu.Unlock()
	o.setCookie(w, r, loginCookie, "", -1)
	q := r.URL.Query()
	if attempt == nil || attempt.state == "" || time.Now().After(attempt.expires) {
		fail("login expired")
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(attempt.state)) != 1 {
		fail("state mismatch")
		return
	}
	if e := q.Get("error"); e != "" {
		fail("provider error %s", e)
		return
	}
	user, idToken, err := o.exchange(r, q.Get("code"), attempt.nonce)
	if err != nil {
		fail("%v", err)
		return
	}

	sess := &authSession{user: user, idToken: idToken, expires: time.Now().Add(sessionTimeout)}
	target := "/"
	if attempt.target != nil {
		target = attempt.target.url
		if attempt.target.post {
			sess.replay = attempt.target
		}
	}
	id := randomToken()
	o.mu.Lock()
	o.sessions[id] = sess
	o.mu.Unlock()
	o.setCookie(w, r, sessionCookie, id, 0)
	loginsTotal.Inc("success")
	http.Redirect(w, r, target, http.StatusFound)
}

// exchange redeems an authorization code and returns the user it identifies.
func (o *oauth2Login) exchange(r *http.Request, code, nonce string) (User, string, error) {
	meta, keys, err := o.provider()
	if err != nil {
		return User{}, "", err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {o.redirectURI(r)}}
	if o.cfg.ClientAuthPost {
		form.Set("client_id", o.cfg.ClientID)
		form.Set("client_secret", o.cfg.ClientSecret)
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, meta.TokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if !o.cfg.ClientAuthPost {
		req.SetBasicAuth(url.QueryEscape(o.cfg.ClientID), url.QueryEscape(o.cfg.ClientSecret))
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return User{}, "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || resp.StatusCode != http.StatusOK {
		return User{}, "", fmt.Errorf("token request: HTTP %d %s", resp.StatusCode, tok.Error)
	}
	if tok.IDToken == "" {
		return User{}, "", errors.New("token response has no id_token (scope must include openid)")
	}
	claims, err := keys.verify(tok.IDToken)
	if err != nil {
		return User{}, "", err
	}
	if err := checkIDToken(claims, meta.Issuer, o.cfg.ClientID, nonce, time.Now()); err != nil {
		return User{}, "", err
	}
	var userinfo map[string]any
	if meta.UserInfoEndpoint != "" && tok.AccessToken != "" {
		userinfo, err = o.userInfo(r.Context(), meta.UserInfoEndpoint, tok.AccessToken)
		if err != nil {
			return User{}, "", err
		}
		if sub, _ := userinfo["sub"].(string); sub != claims["sub"] {
			return User{}, "", errors.New("userinfo subject does not match the ID token")
		}
	}
	return o.userFromClaims(claims, userinfo), tok.IDToken, nil
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

// checkIDToken validates the claims of a signature-checked ID token.
func checkIDToken(claims map[string]any, issuer, clientID, nonce string, now time.Time) error {
	if iss, _ := claims["iss"].(string); iss != issuer {
		return fmt.Errorf("ID token issuer %q is not %q", iss, issuer)
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
func (o *oauth2Login) userFromClaims(idClaims, userinfo map[string]any) User {
	attrs := map[string]any{}
	for k, v := range idClaims {
		attrs[k] = v
	}
	for k, v := range userinfo {
		attrs[k] = v
	}
	name := fmt.Sprint(attrs[o.cfg.UserNameAttribute])
	if attrs[o.cfg.UserNameAttribute] == nil {
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
	return User{Name: name, Roles: roles}
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

// logout ends the session and, when the provider supports it, its session
// too, returning to the portal.
func (o *oauth2Login) logout(w http.ResponseWriter, r *http.Request) {
	idToken := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		o.mu.Lock()
		if sess := o.sessions[c.Value]; sess != nil {
			idToken = sess.idToken
		}
		delete(o.sessions, c.Value)
		o.mu.Unlock()
	}
	o.setCookie(w, r, sessionCookie, "", -1)
	target := "/"
	if meta, _, err := o.provider(); err == nil && meta.EndSessionEndpoint != "" && idToken != "" {
		q := url.Values{"id_token_hint": {idToken}, "post_logout_redirect_uri": {baseURL(r)}}
		target = meta.EndSessionEndpoint + "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (o *oauth2Login) redirectURI(r *http.Request) string {
	return strings.NewReplacer("{baseUrl}", baseURL(r), "{registrationId}", o.cfg.RegistrationID).Replace(o.cfg.RedirectURI)
}

// baseURL is the portal's origin as the browser sees it.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

func (o *oauth2Login) setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge time.Duration) {
	c := &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: strings.HasPrefix(baseURL(r), "https:"),
	}
	switch {
	case maxAge < 0:
		c.MaxAge = -1
	case maxAge > 0:
		c.MaxAge = int(maxAge.Seconds())
	}
	http.SetCookie(w, c)
}

// prune drops expired sessions and login attempts; the caller holds o.mu.
func (o *oauth2Login) prune() {
	now := time.Now()
	for k, a := range o.pending {
		if now.After(a.expires) {
			delete(o.pending, k)
		}
	}
	for k, s := range o.sessions {
		if now.After(s.expires) {
			delete(o.sessions, k)
		}
	}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// bufferedBody reads a request body and puts it back for the handler.
func bufferedBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b
}
