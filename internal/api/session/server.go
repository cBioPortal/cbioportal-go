// Package session serves the session service endpoints (/api/session/...)
// and the public virtual studies over the core.State's session store.
package session

import (
	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/studyview"
)

// Server serves the endpoints of this package; it embeds the study-view
// Server, which resolves the samples of dynamic virtual studies.
type Server struct{ studyview.Server }

// New returns the Server over s.
func New(s *core.State) Server { return Server{studyview.New(s)} }
