// Package apidocs serves the API descriptions (Swagger 2.0 and OpenAPI 3.1)
// and Swagger UI, with the paging parameters go adds laid over them. The
// snapshots it serves are the files in this directory.
package apidocs

import "github.com/cbioportal/cbioportal-go/internal/api/core"

// Server serves the endpoints of this package.
type Server struct{ *core.State }

// New returns the Server over s.
func New(s *core.State) Server { return Server{s} }
