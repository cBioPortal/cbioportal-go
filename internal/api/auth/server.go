// Package auth is the portal's authentication and authorization: the
// OAuth2 login (authenticate=oauth2 or optional_oauth2) with its login
// sessions, the study authorization, the data access tokens
// (dat.method=oauth2) and the optional HS256 bearer-token auth.
package auth

import "github.com/cbioportal/cbioportal-go/internal/api/core"

// Server serves the data-access-token endpoints and reads the studies the
// study authorization checks.
type Server struct{ *core.State }
