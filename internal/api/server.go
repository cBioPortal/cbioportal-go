// Package api serves the cBioPortal REST contract from the ClickHouse transform
// views, with JSON field shapes matching the Java backend so the existing
// frontend works unchanged.
package api

import (
	"errors"
	"log"
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/studyview"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

// State is the API's state: the handlers' servers plus the login.
type State struct {
	studyview.Server
	// login is the OAuth2 login (authenticate=oauth2 or optional_oauth2) and
	// access its study authorization; nil login serves every study
	// anonymously.
	login  *oauth2Login
	access accessPolicy
	// dat serves and verifies data access tokens (dat.method=oauth2).
	dat *datProvider
}

// NewState returns the API state over ClickHouse client c; ConfigureAuth and
// ConfigureSessions apply the portal properties.
func NewState(c *ch.Client) *State { return newState(core.NewState(c)) }

// newState returns the API state over cs, without a login.
func newState(cs *core.State) *State {
	return &State{Server: studyview.New(cs)}
}

// MetricsHandler serves the Prometheus metrics.
func MetricsHandler() http.Handler { return core.MetricsHandler() }

// ConfigureAuth sets up the login and study authorization the portal
// properties ask for.
func (s *State) ConfigureAuth() error {
	props := config.EffectiveProperties(s.Props, s.PropsReplaceDefaults)
	cfg, err := loadOAuth2Config(props)
	if err != nil {
		return err
	}
	datCfg, err := loadDATConfig(props)
	if err != nil {
		return err
	}
	if datCfg != nil {
		if cfg == nil || cfg.optional() {
			return errors.New("dat.method=oauth2 needs authenticate=oauth2")
		}
		if loadAuthConfig().Enabled {
			return errors.New("dat.method=oauth2 verifies bearer tokens itself; unset AUTH_ENABLED")
		}
	}
	if cfg == nil {
		return nil
	}
	store, err := loadLoginStore(props)
	if err != nil {
		return err
	}
	s.login = newOAuth2Login(cfg, store)
	s.AuthMethod = cfg.Method
	s.access = newAccessPolicy(props)
	s.access.anonymous = cfg.optional()
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
	log.Printf("login sessions: %s", store.describe())
	if datCfg != nil {
		s.dat = newDATProvider(datCfg)
		s.login.dat = s.dat
		log.Printf("dat.method=oauth2: data access tokens from %s (client %s)", datCfg.Issuer, datCfg.ClientID)
	}
	return nil
}

// NewRouter builds the API handler with CORS, the OAuth2 login, data access
// tokens and study authorization (authenticate=oauth2), optional HS256
// bearer-token auth (disabled by default), Java's trailing-slash matching
// (trailingSlash) and a body-draining catch-all,
// behind the request-target checks of Tomcat and Spring's firewall.
func NewRouter(s *State) http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	authCfg := loadAuthConfig()
	if s.login != nil {
		authCfg.DefaultUser = "" // users log in
	}
	h := auth(authCfg, s.StudyAvailability(core.ExtraEndpoints(s.StudiesMeta, core.UninterceptedFetch(core.StudyViewBodyErrors(s.JavaContract(core.PageList(core.MetaProjection(core.BodyErrors(mux)))))))))
	if s.login != nil {
		if s.login.cfg.MethodAuthorization {
			h = s.studyAccess(s.access, h)
		}
		h = s.login.middleware(h)
	}
	return s.Observe(core.JavaFirewall(core.CORS(core.GzipResponse(core.GunzipBody(s.TrailingSlash(h))))))
}
