// Package api serves the cBioPortal REST contract from the ClickHouse transform
// views, with JSON field shapes matching the Java backend so the existing
// frontend works unchanged.
package api

import (
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/auth"
	"github.com/cbioportal/cbioportal-go/internal/api/comparison"
	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

// State is the API's state: the handlers' servers plus the login.
type State struct {
	comparison.Server
	// auth is the login; nil serves every study anonymously.
	auth *auth.Login
}

// NewState returns the API state over ClickHouse client c; ConfigureAuth and
// ConfigureSessions apply the portal properties.
func NewState(c *ch.Client) *State { return newState(core.NewState(c)) }

// newState returns the API state over cs, without a login.
func newState(cs *core.State) *State {
	return &State{Server: comparison.New(cs)}
}

// MetricsHandler serves the Prometheus metrics.
func MetricsHandler() http.Handler { return core.MetricsHandler() }

// ConfigureAuth sets up the login and study authorization the portal
// properties ask for.
func (s *State) ConfigureAuth() error {
	l, err := auth.Configure(config.EffectiveProperties(s.Props, s.PropsReplaceDefaults))
	if err != nil || l == nil {
		return err
	}
	s.auth = l
	s.AuthMethod = l.Method()
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
	h := auth.BearerAuth(s.auth != nil, s.StudyAvailability(core.ExtraEndpoints(s.StudiesMeta, core.UninterceptedFetch(core.StudyViewBodyErrors(s.JavaContract(core.PageList(core.MetaProjection(core.BodyErrors(mux)))))))))
	if s.auth != nil {
		h = s.auth.Wrap(s.State, h)
	}
	return s.Observe(core.JavaFirewall(core.CORS(core.GzipResponse(core.GunzipBody(s.TrailingSlash(h))))))
}
