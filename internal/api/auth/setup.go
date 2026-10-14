package auth

import (
	"errors"
	"log"
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// Login is the OAuth2 login (authenticate=oauth2 or optional_oauth2), its
// study authorization and the data access tokens (dat.method=oauth2) the
// portal properties configure.
type Login struct {
	login  *oauth2Login
	access accessPolicy
	// dat serves and verifies data access tokens; nil without dat.method.
	dat *datProvider
}

// Configure returns the login the portal properties props ask for, nil
// when they ask for none. bearerAuth is whether the server requires HS256
// bearer tokens (--auth-enabled), which a data access token login excludes.
func Configure(props map[string]string, bearerAuth bool) (*Login, error) {
	cfg, err := loadOAuth2Config(props)
	if err != nil {
		return nil, err
	}
	datCfg, err := loadDATConfig(props)
	if err != nil {
		return nil, err
	}
	if datCfg != nil {
		if cfg == nil || cfg.optional() {
			return nil, errors.New("dat.method=oauth2 needs authenticate=oauth2")
		}
		if bearerAuth {
			return nil, errors.New("dat.method=oauth2 verifies bearer tokens itself; unset --auth-enabled")
		}
	}
	if cfg == nil {
		return nil, nil
	}
	store, err := loadLoginStore(props)
	if err != nil {
		return nil, err
	}
	basic, err := loadBasicLogin(props)
	if err != nil {
		return nil, err
	}
	l := &Login{login: newOAuth2Login(cfg, store), access: newAccessPolicy(props)}
	l.login.basic = basic
	l.access.anonymous = cfg.optional()
	for _, reg := range cfg.Registrations {
		issuer := reg.IssuerURI
		if issuer == "" {
			issuer = reg.AuthorizationURI
		}
		log.Printf("authenticate=%s: login with %s through %s (client %s)", cfg.Method, reg.ID, issuer, reg.ClientID)
	}
	if !cfg.MethodAuthorization {
		log.Printf("authenticate=%s: every study is open to everyone (security.method_authorization_enabled is off)", cfg.Method)
	}
	if basic != nil {
		log.Printf("authenticate=%s: basic login for user %s at /j_spring_security_check", cfg.Method, basic.user)
	}
	log.Printf("login sessions: %s", store.describe())
	if datCfg != nil {
		l.dat = newDATProvider(datCfg)
		l.login.dat = l.dat
		log.Printf("dat.method=oauth2: data access tokens from %s (client %s)", datCfg.Issuer, datCfg.ClientID)
	}
	return l, nil
}

// Method is the login's authenticate= method.
func (l *Login) Method() string { return l.login.cfg.Method }

// Wrap puts the login, and with method authorization the study access
// checks (reading the studies through s), in front of next.
func (l *Login) Wrap(s *core.State, next http.Handler) http.Handler {
	h := next
	if l.login.cfg.MethodAuthorization {
		h = Server{s}.studyAccess(l.access, h)
	}
	return l.login.middleware(h)
}

// BearerAuth is the optional HS256 bearer-token auth of the server settings
// o in front of next; loggedIn drops its default user, as users log in.
func BearerAuth(o core.Options, loggedIn bool, next http.Handler) http.Handler {
	authCfg := bearerConfig(o)
	if loggedIn {
		authCfg.DefaultUser = "" // users log in
	}
	return auth(authCfg, next)
}

// RouteDataAccessTokens registers the data-access-token endpoints:
// dat.method=oauth2's, or placeholders without it (l nil or without dat).
func (l *Login) RouteDataAccessTokens(s *core.State, mux *http.ServeMux) {
	if l != nil && l.dat != nil {
		Server{s}.routeDataAccessTokens(mux, l.dat)
		return
	}
	// Data-access tokens without dat.method=oauth2: placeholders.
	mux.HandleFunc("GET /api/data-access-token", func(w http.ResponseWriter, r *http.Request) { core.WriteJSON(w, map[string]any{}) })
	mux.HandleFunc("GET /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) { core.WriteJSON(w, []any{}) })
	mux.HandleFunc("POST /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) {
		core.ReadBody(r, nil)
		core.WriteJSON(w, map[string]any{})
	})
	mux.HandleFunc("DELETE /api/data-access-tokens", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /api/data-access-tokens/{token}", func(w http.ResponseWriter, r *http.Request) { core.WriteJSON(w, map[string]any{}) })
	mux.HandleFunc("DELETE /api/data-access-tokens/{token}", func(w http.ResponseWriter, r *http.Request) {})
}
