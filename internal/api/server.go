// Package api serves the cBioPortal REST contract from the ClickHouse transform
// views, with JSON field shapes matching the Java backend so the existing
// frontend works unchanged.
package api

import (
	"context"
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/auth"
	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/studies"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

// State is the API's state: the core.State the handler packages serve
// from, plus the login.
type State struct {
	*core.State
	// auth is the login; nil serves every study anonymously.
	auth *auth.Login
}

// NewState returns the API state over ClickHouse client c with the server
// settings o; ConfigureAuth and ConfigureSessions apply the portal
// properties.
func NewState(c *ch.Client, o Options) *State { return newState(core.NewState(c, o)) }

// Options are the server settings the API uses (core.Options).
type Options = core.Options

// newState returns the API state over cs, without a login.
func newState(cs *core.State) *State {
	return &State{State: cs}
}

// MetricsHandler serves the Prometheus metrics.
func MetricsHandler() http.Handler { return core.MetricsHandler() }

// ConfigureAuth sets up the login and study authorization the portal
// properties ask for.
func (s *State) ConfigureAuth() error {
	l, err := auth.Configure(config.EffectiveProperties(s.Props, s.PropsReplaceDefaults), s.Opts.BearerAuth)
	if err != nil || l == nil {
		return err
	}
	s.auth = l
	s.AuthMethod = l.Method()
	return nil
}

// CheckLogin reaches the sign-in providers and the login session store of
// the configured login; nil without one.
func (s *State) CheckLogin(ctx context.Context) []auth.CheckResult {
	if s.auth == nil {
		return nil
	}
	return s.auth.Check(ctx)
}

// NewRouter builds the API handler with CORS, the OAuth2 login, data access
// tokens and study authorization (authenticate=oauth2), optional HS256
// bearer-token auth (disabled by default), Java's trailing-slash matching
// (trailingSlash), Spring's binding of repeated String parameters
// (SpringStringParams) and a catch-all for the portal's pages and static files,
// with Spring Security's default response headers, behind the
// request-target checks of Tomcat and Spring's firewall.
func NewRouter(s *State) http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	h := auth.BearerAuth(s.Opts, s.auth != nil, core.SpringStringParams(s.StudyAvailability(core.ExtraEndpoints(studies.New(s.State).StudiesMeta, core.StudyViewBodyErrors(s.JavaContract(core.PageList(core.MetaProjection(core.BodyErrors(mux)))))))))
	if s.auth != nil {
		h = s.auth.Wrap(s.State, h)
	}
	return s.Observe(core.JavaFirewall(core.SecurityHeaders(core.CORS(core.GzipResponse(core.GunzipBody(s.TrailingSlash(h)))))))
}
