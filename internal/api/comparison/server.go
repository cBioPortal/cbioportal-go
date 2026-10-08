// Package comparison serves group comparison: alteration, expression,
// generic assay and clinical enrichments, survival data and the clinical
// events (timeline) they are built from.
package comparison

import (
	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/studyview"
)

// Server serves the endpoints of this package; it embeds the study-view
// Server, whose filters and counts the comparisons share.
type Server struct{ studyview.Server }

// New returns the Server over s.
func New(s *core.State) Server { return Server{studyview.New(s)} }

// Bulk returns the Server for the bulk endpoints (studies.Server.Bulk).
func (s Server) Bulk() Server { return Server{s.Server.Bulk()} }
