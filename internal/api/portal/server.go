// Package portal serves the portal's own endpoints: the frontend
// configuration (/config_service), server info, health and timestamps, the
// cache endpoints, robots.txt, llms.txt and the sitemaps.
package portal

import "github.com/cbioportal/cbioportal-go/internal/api/core"

// Server serves the endpoints of this package.
type Server struct{ *core.State }

// New returns the Server over s.
func New(s *core.State) Server { return Server{s} }
