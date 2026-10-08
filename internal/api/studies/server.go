// Package studies serves the studies, samples, patients, clinical data and
// attributes, sample lists, genes, cancer types, study resources and tags.
package studies

import (
	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// Server serves the endpoints of this package.
//
// CH, which every handler package embedding this Server reads as s.CH, is
// the client the handlers query: the State's CH, or for the bulk endpoints
// its BulkCH with the per-query limits (see Bulk).
type Server struct {
	*core.State
	CH *ch.Client
}

// New returns the Server over s.
func New(s *core.State) Server { return Server{s, s.CH} }

// Bulk returns the Server whose queries run with the bulk endpoints'
// limits (core.BulkLimits). Its metadata lookups through the State (profile
// and sample list caches) keep the State's own client. A State without a
// BulkCH (built by hand) serves the bulk endpoints from CH.
func (s Server) Bulk() Server {
	if s.BulkCH != nil {
		s.CH = s.BulkCH
	}
	return s
}
