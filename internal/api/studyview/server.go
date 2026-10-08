// Package studyview serves the study view: the study-view filter, the
// filtered samples, clinical and genomic counts and bins, the clinical data
// table, plots, treatments and namespace attributes.
package studyview

import (
	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/molecular"
)

// Server serves the endpoints of this package; it embeds the molecular
// Server, whose lookups the study view shares.
type Server struct{ molecular.Server }

// New returns the Server over s.
func New(s *core.State) Server { return Server{molecular.New(s)} }

// Bulk returns the Server for the bulk endpoints (studies.Server.Bulk).
func (s Server) Bulk() Server { return Server{s.Server.Bulk()} }
