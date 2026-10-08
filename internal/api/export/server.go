// Package export serves the study export (/export/study/{studyId}.zip):
// a loaded study written back out as cBioPortal study files.
package export

import (
	"github.com/cbioportal/cbioportal-go/internal/api/comparison"
	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// Server serves the endpoints of this package; it embeds the comparison
// Server, whose data readers the export writes out.
type Server struct{ comparison.Server }

// New returns the Server over s.
func New(s *core.State) Server { return Server{comparison.New(s)} }

// Bulk returns the Server for the bulk endpoints (studies.Server.Bulk).
func (s Server) Bulk() Server { return Server{s.Server.Bulk()} }
