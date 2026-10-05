package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Data access tokens with dat.method=oauth2, as the Java portal's
// OAuth2DataAccessTokenController and OAuth2TokenAuthenticationProvider:
//
//   - GET /api/data-access-token (logged in) redirects to the provider's
//     authorize endpoint asking for scope "openid offline_access", returning
//     to dat.oauth2.redirectUri (/api/data-access-token/oauth2). That
//     exchanges the code and hands the provider's offline (refresh) token to
//     the browser as cbioportal_data_access_token.txt.
//   - An API request carrying an Authorization header is authenticated by
//     that token alone: a refresh_token grant at the provider yields an
//     access token, whose signature (dat.oauth2.jwkUrl), issuer and expiry
//     are checked; the user is its sub claim and the roles are at
//     dat.oauth2.jwtRolesPath. Study authorization then applies as for a
//     logged-in session. Any failure is a 401.
//   - The token management endpoints (/api/data-access-tokens*) answer 501.
//
// Unlike Java, the download round trip carries an OAuth2 state bound to the
// browser by a short-lived cookie, so a forged callback cannot hand a user
// someone else's token.
//
// Verified users are cached by token hash until shortly before the access
// token expires, so the provider is not asked on every request.

const (
	datFileName = "cbioportal_data_access_token.txt"
	datCookie   = "CBIOPORTAL_DAT"
	datScope    = "openid offline_access"
	// datCacheSize bounds the number of tokens whose users are cached.
	datCacheSize = 1024
	// datCacheMargin is how long before the access token's expiry a cached
	// user is dropped.
	datCacheMargin = 30 * time.Second
)

const (
	msgDATNoIdentity     = "No authenticated identity found while processing request"
	msgDATProhibited     = "You are prohibited from using Data Access Tokens"
	msgDATNotImplemented = "Requested API is not implemented yet"
)

type datConfig struct {
	ClientID             string
	ClientSecret         string
	Issuer               string
	AccessTokenURI       string
	UserAuthorizationURI string
	JWKURL               string
	RedirectURI          string
	RolesPath            []string
	// FilterUserRole, when set, is the authority (e.g. ROLE_dat_user) a user
	// needs to download a token.
	FilterUserRole string
}

// loadDATConfig reads the dat.* properties. It returns nil unless
// dat.method is oauth2, and an error for the token methods this server does
// not implement.
func loadDATConfig(props map[string]string) (*datConfig, error) {
	switch method := strings.TrimSpace(props["dat.method"]); method {
	case "", "none":
		return nil, nil
	case "oauth2":
	default:
		return nil, fmt.Errorf("dat.method=%s is not supported (use oauth2 or none)", method)
	}
	p := func(k string) string { return strings.TrimSpace(props["dat.oauth2."+k]) }
	c := &datConfig{
		ClientID:             p("clientId"),
		ClientSecret:         p("clientSecret"),
		Issuer:               p("issuer"),
		AccessTokenURI:       p("accessTokenUri"),
		UserAuthorizationURI: p("userAuthorizationUri"),
		JWKURL:               p("jwkUrl"),
		RedirectURI:          p("redirectUri"),
		FilterUserRole:       strings.TrimSpace(props["dat.filter_user_role"]),
	}
	var missing []string
	for _, f := range []struct{ key, v string }{
		{"clientId", c.ClientID}, {"issuer", c.Issuer}, {"accessTokenUri", c.AccessTokenURI},
		{"userAuthorizationUri", c.UserAuthorizationURI}, {"jwkUrl", c.JWKURL}, {"redirectUri", c.RedirectURI},
	} {
		if f.v == "" {
			missing = append(missing, "dat.oauth2."+f.key)
		}
	}
	if len(missing) > 0 {
		return nil, errors.New("dat.method=oauth2 needs " + strings.Join(missing, ", "))
	}
	rolesPath := p("jwtRolesPath")
	if rolesPath == "" {
		rolesPath = "resource_access::cbioportal::roles"
	}
	c.RolesPath = strings.Split(rolesPath, "::")
	return c, nil
}

type datEntry struct {
	user    User
	expires time.Time
}

type datProvider struct {
	cfg    *datConfig
	client *http.Client
	keys   *jwks
	now    func() time.Time

	mu       sync.Mutex
	cache    map[string]datEntry // by token hash
	inflight singleflight.Group
	pending  map[string]datAttempt // token downloads in progress, by cookie
}

type datAttempt struct {
	state   string
	expires time.Time
}

func newDATProvider(cfg *datConfig) *datProvider {
	client := &http.Client{Timeout: 15 * time.Second}
	return &datProvider{
		cfg:     cfg,
		client:  client,
		keys:    newJWKS(cfg.JWKURL, client),
		now:     time.Now,
		cache:   map[string]datEntry{},
		pending: map[string]datAttempt{},
	}
}

// bearerToken extracts the token of an Authorization header, as Java's
// TokenAuthenticationFilter: the "Bearer" scheme in any case.
func bearerToken(header string) (string, bool) {
	if len(header) < len("Bearer") || !strings.EqualFold(header[:len("Bearer")], "Bearer") {
		return "", false
	}
	tok := strings.TrimSpace(header[len("Bearer"):])
	return tok, tok != ""
}

// authenticate returns the user an offline token stands for.
func (d *datProvider) authenticate(ctx context.Context, offlineToken string) (User, error) {
	sum := sha256.Sum256([]byte(offlineToken))
	key := hex.EncodeToString(sum[:])
	d.mu.Lock()
	e, ok := d.cache[key]
	if ok && d.now().After(e.expires) {
		delete(d.cache, key)
		ok = false
	}
	d.mu.Unlock()
	if ok {
		datChecks.Inc("cached")
		return e.user, nil
	}
	v, err, _ := d.inflight.Do(key, func() (any, error) {
		user, expires, err := d.refresh(context.WithoutCancel(ctx), offlineToken)
		if err != nil {
			datChecks.Inc("rejected")
			return User{}, err
		}
		datChecks.Inc("refreshed")
		d.store(key, datEntry{user: user, expires: expires.Add(-datCacheMargin)})
		return user, nil
	})
	if err != nil {
		return User{}, err
	}
	return v.(User), nil
}

// store caches an entry, making room by dropping expired entries and then
// the one expiring soonest.
func (d *datProvider) store(key string, e datEntry) {
	now := d.now()
	if !e.expires.After(now) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.cache) >= datCacheSize {
		soonest := ""
		for k, c := range d.cache {
			if now.After(c.expires) {
				delete(d.cache, k)
			} else if soonest == "" || c.expires.Before(d.cache[soonest].expires) {
				soonest = k
			}
		}
		if len(d.cache) >= datCacheSize {
			delete(d.cache, soonest)
		}
	}
	d.cache[key] = e
}

// refresh trades the offline token for an access token and returns the
// user it names, with the access token's expiry.
func (d *datProvider) refresh(ctx context.Context, offlineToken string) (User, time.Time, error) {
	tok, err := d.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {d.cfg.ClientID},
		"client_secret": {d.cfg.ClientSecret},
		"refresh_token": {offlineToken},
	})
	if err != nil {
		return User{}, time.Time{}, err
	}
	if tok.AccessToken == "" {
		return User{}, time.Time{}, errors.New("authentication server did not return an access token")
	}
	claims, err := d.keys.verify(tok.AccessToken)
	if err != nil {
		return User{}, time.Time{}, err
	}
	if iss, _ := claims["iss"].(string); iss != d.cfg.Issuer {
		return User{}, time.Time{}, fmt.Errorf("access token issuer %q is not %q", iss, d.cfg.Issuer)
	}
	exp, ok := claims["exp"].(float64)
	expires := time.Unix(int64(exp), 0)
	if !ok || d.now().After(expires) {
		return User{}, time.Time{}, errors.New("access token expired")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return User{}, time.Time{}, errors.New("user name could not be found in access token")
	}
	return User{Name: sub, Roles: claimRoles(claims, d.cfg.RolesPath)}, expires, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
}

// tokenRequest posts a grant to the token endpoint, the client credentials
// in the form as Java sends them.
func (d *datProvider) tokenRequest(ctx context.Context, form url.Values) (tokenResponse, error) {
	var tok tokenResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.AccessTokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return tok, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return tok, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || resp.StatusCode != http.StatusOK {
		return tok, fmt.Errorf("token request: HTTP %d %s", resp.StatusCode, tok.Error)
	}
	return tok, nil
}

// authorizationURL is where a token download starts, built as Java builds
// it, plus the state.
func (d *datProvider) authorizationURL(state string) string {
	return fmt.Sprintf("%s?response_type=code&scope=%s&client_id=%s&redirect_uri=%s&state=%s",
		d.cfg.UserAuthorizationURI, url.QueryEscape(datScope),
		url.QueryEscape(d.cfg.ClientID), url.QueryEscape(d.cfg.RedirectURI), url.QueryEscape(state))
}

// begin records a download in progress and binds it to the browser.
func (d *datProvider) begin(w http.ResponseWriter, r *http.Request) string {
	id, state := randomToken(), randomToken()
	now := d.now()
	d.mu.Lock()
	for k, a := range d.pending {
		if now.After(a.expires) {
			delete(d.pending, k)
		}
	}
	d.pending[id] = datAttempt{state: state, expires: now.Add(loginTimeout)}
	d.mu.Unlock()
	setDATCookie(w, r, id, loginTimeout)
	return state
}

// finish consumes the browser's download in progress and reports whether
// the callback's state is its own.
func (d *datProvider) finish(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie(datCookie)
	if err != nil {
		return false
	}
	setDATCookie(w, r, "", -1)
	d.mu.Lock()
	a, ok := d.pending[c.Value]
	delete(d.pending, c.Value)
	d.mu.Unlock()
	return ok && !d.now().After(a.expires) &&
		subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(a.state)) == 1
}

func setDATCookie(w http.ResponseWriter, r *http.Request, value string, maxAge time.Duration) {
	c := &http.Cookie{
		Name: datCookie, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: strings.HasPrefix(baseURL(r), "https:"), MaxAge: int(maxAge.Seconds()),
	}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// routeDataAccessTokens registers the dat.method=oauth2 endpoints.
func (s *State) routeDataAccessTokens(mux *http.ServeMux, d *datProvider) {
	mux.HandleFunc("GET /api/data-access-token", d.download)
	mux.HandleFunc("GET /api/data-access-token/oauth2", d.callback)
	notImplemented := func(w http.ResponseWriter, r *http.Request) {
		readBody(r, nil)
		writeError(w, http.StatusNotImplemented, msgDATNotImplemented)
	}
	for _, p := range []string{
		"POST /api/data-access-tokens", "GET /api/data-access-tokens", "DELETE /api/data-access-tokens",
		"GET /api/data-access-tokens/{token}", "DELETE /api/data-access-tokens/{token}",
	} {
		mux.HandleFunc(p, notImplemented)
	}
}

// download sends a user allowed data access tokens to the provider.
func (d *datProvider) download(w http.ResponseWriter, r *http.Request) {
	user, ok := GetUser(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, msgDATNoIdentity)
		return
	}
	if d.cfg.FilterUserRole != "" && !hasAuthority(user, d.cfg.FilterUserRole) {
		writeError(w, http.StatusUnauthorized, msgDATProhibited)
		return
	}
	w.Header().Set("Location", d.authorizationURL(d.begin(w, r)))
	w.WriteHeader(http.StatusFound)
}

// hasAuthority reports whether the user has a Spring authority: a role
// prefixed ROLE_.
func hasAuthority(u User, authority string) bool {
	for _, role := range u.Roles {
		if "ROLE_"+role == authority {
			return true
		}
	}
	return false
}

// callback redeems the provider's code for an offline token and returns it
// as a download.
func (d *datProvider) callback(w http.ResponseWriter, r *http.Request) {
	if !d.finish(w, r) {
		log.Print("data access token: callback without a matching download in progress")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	tok, err := d.tokenRequest(r.Context(), url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {r.URL.Query().Get("code")},
		"client_id":     {d.cfg.ClientID},
		"client_secret": {d.cfg.ClientSecret},
		"redirect_uri":  {d.cfg.RedirectURI},
		"scope":         {datScope},
	})
	if err == nil && tok.RefreshToken == "" {
		err = errors.New("no refresh_token in the token response")
	}
	if err != nil {
		log.Printf("data access token: offline token could not be retrieved: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+datFileName)
	fmt.Fprintf(w, "token: %s\n", tok.RefreshToken)
}

// tokenAuth authenticates an API request by its Authorization header alone,
// as Java's TokenAuthenticationFilter does on the API paths: a session is
// not consulted, and a missing or refused token is a 401.
func (d *datProvider) tokenAuth(w http.ResponseWriter, r *http.Request, next http.Handler) {
	tok, ok := bearerToken(r.Header.Get("Authorization"))
	var user User
	err := errors.New("no bearer token in the Authorization header")
	if ok {
		user, err = d.authenticate(r.Context(), tok)
	}
	if err != nil {
		log.Printf("data access token refused for %s %s: %v", r.Method, r.URL.Path, err)
		readBody(r, nil)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	next.ServeHTTP(w, withUser(r, user))
}
