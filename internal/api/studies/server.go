// Package studies serves the studies, samples, patients, clinical data and
// attributes, sample lists, genes, cancer types, study resources and tags.
package studies

import "github.com/cbioportal/cbioportal-go/internal/api/core"

// Server serves the endpoints of this package.
type Server struct{ *core.State }

// New returns the Server over s.
func New(s *core.State) Server { return Server{s} }
