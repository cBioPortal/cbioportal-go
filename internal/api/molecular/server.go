// Package molecular serves the molecular profiles and their data: mutations,
// copy-number alterations and segments, structural variants, generic assays,
// gene sets, gene panels, significance results and co-expression.
package molecular

import (
	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/studies"
)

// Server serves the endpoints of this package; it embeds the studies
// Server, whose lookups the molecular endpoints share.
type Server struct{ studies.Server }

// New returns the Server over s.
func New(s *core.State) Server { return Server{studies.New(s)} }

// Bulk returns the Server for the bulk endpoints (studies.Server.Bulk).
func (s Server) Bulk() Server { return Server{s.Server.Bulk()} }
