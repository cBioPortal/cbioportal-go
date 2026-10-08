package core

import (
	"strconv"
	"time"
)

// BulkLimits bound each ClickHouse query of the bulk endpoints: the reads
// of whole profiles of data (molecular data, copy number, generic assays,
// gene panels, mutations, segments), the enrichments, co-expression and
// the study export. Their responses stream, so the API's own memory stays
// small however much they return; these limits bound what one request
// costs the shared ClickHouse service instead.
type BulkLimits struct {
	// MaxMemory stops a query that needs more memory (max_memory_usage);
	// GROUP BY spills to disk at half of it. 0: no limit.
	MaxMemory int64
	// MaxTime stops a query that runs longer (max_execution_time), the
	// client's reading of a streamed result included. 0: no limit.
	MaxTime time.Duration
	// SortSpill is the memory an ORDER BY may use before it sorts on disk
	// (max_bytes_before_external_sort). 0: the server's setting.
	SortSpill int64
}

// DefaultBulkLimits are the limits without configuration. The memory limit
// sits above the largest everyday queries seen on the public portal's
// mirror (one gene across a pan-cancer study's profiles: 7-9 GiB) and
// below what took a 30 GiB replica to its limit (15 GiB); the time limit
// above its slowest everyday query (under 2 minutes).
var DefaultBulkLimits = BulkLimits{MaxMemory: 10 << 30, MaxTime: 10 * time.Minute, SortSpill: 1 << 30}

// Settings returns the ClickHouse settings that apply the limits.
func (l BulkLimits) Settings() map[string]string {
	s := map[string]string{}
	if l.MaxMemory > 0 {
		s["max_memory_usage"] = strconv.FormatInt(l.MaxMemory, 10)
		s["max_bytes_before_external_group_by"] = strconv.FormatInt(l.MaxMemory/2, 10)
	}
	if l.SortSpill > 0 {
		s["max_bytes_before_external_sort"] = strconv.FormatInt(l.SortSpill, 10)
	}
	if l.MaxTime > 0 {
		s["max_execution_time"] = strconv.FormatFloat(l.MaxTime.Seconds(), 'f', -1, 64)
	}
	return s
}

// SetBulkLimits makes the bulk endpoints' queries (BulkCH) run with the
// limits.
func (s *State) SetBulkLimits(l BulkLimits) {
	s.BulkCH = s.CH.WithSettings(l.Settings())
}

// MsgQueryLimit answers a request whose query stopped at its memory or
// time limit ("memory" or "time").
func MsgQueryLimit(limit string) string {
	what := "needs more database memory than one request may use"
	if limit == "time" {
		what = "takes longer than one request may run"
	}
	return "This request " + what + ". Ask for less data at a time: name the genes " +
		"(entrezGeneIds) or fewer samples, or page through the result with pageSize and pageNumber."
}
