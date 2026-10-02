package api

import (
	"fmt"
	"slices"
	"sync"
	"time"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// metaCache memoizes results of metadata queries (studies, profiles, sample
// ids) that the frontend repeats many times per page. Entries are dropped
// whenever the databases' tables change — checked at most once per
// metaCacheRecheck — so a re-import or transform is picked up promptly.
type metaCache struct {
	mu      sync.Mutex
	entries map[string]any
	version string
	checked time.Time
}

const metaCacheRecheck = time.Second

func (c *metaCache) validate(client *ch.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.checked) < metaCacheRecheck {
		return
	}
	c.checked = time.Now()
	v, err := client.Execute(fmt.Sprintf(
		"SELECT max(metadata_modification_time), sum(ifNull(total_rows, 0)), count() FROM system.tables "+
			"WHERE database IN ('%s', 'cbioportal_raw', 'cbioportal_ref') FORMAT TabSeparated", sqlLit(client.Database())))
	if err != nil || v != c.version {
		c.entries = nil
		c.version = v
	}
}

// cachedQuery runs a metadata query through the State's cache. Callers get
// their own copy of the cached slice.
func cachedQuery[T any](s *State, sql string) ([]T, error) {
	s.meta.validate(s.CH)
	s.meta.mu.Lock()
	if v, ok := s.meta.entries[sql]; ok {
		s.meta.mu.Unlock()
		return slices.Clone(v.([]T)), nil
	}
	version := s.meta.version
	s.meta.mu.Unlock()
	rows, err := ch.Query[T](s.CH, sql)
	if err != nil {
		return nil, err
	}
	s.meta.mu.Lock()
	if s.meta.version != version {
		s.meta.mu.Unlock()
		return rows, nil
	}
	if s.meta.entries == nil {
		s.meta.entries = map[string]any{}
	}
	s.meta.entries[sql] = rows
	s.meta.mu.Unlock()
	return slices.Clone(rows), nil
}
