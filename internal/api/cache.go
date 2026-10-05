package api

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/transform"
	"golang.org/x/sync/singleflight"
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
	// inflight collapses concurrent identical queries on a miss.
	inflight singleflight.Group
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
	return cachedQueryP[T](s, sql, nil)
}

// cachedQueryA is cachedQuery for a statement whose values are bound in a.
func cachedQueryA[T any](s *State, sql string, a *sqlArgs) ([]T, error) {
	return cachedQueryP[T](s, sql, a.params(sql))
}

// cachedQueryP is cachedQuery with query parameters; entries are keyed on the
// statement and its parameter values.
func cachedQueryP[T any](s *State, sql string, p ch.Params) ([]T, error) {
	key, err := cacheKey(sql, p)
	if err != nil {
		return nil, err
	}
	s.meta.validate(s.CH)
	s.meta.mu.Lock()
	if v, ok := s.meta.entries[key]; ok {
		s.meta.mu.Unlock()
		metaCacheLookups.Inc("hit")
		return slices.Clone(v.([]T)), nil
	}
	metaCacheLookups.Inc("miss")
	version := s.meta.version
	s.meta.mu.Unlock()
	// Concurrent misses for the same query share one ClickHouse round trip.
	v, err, _ := s.meta.inflight.Do(version+"\x00"+key, func() (any, error) {
		return ch.QueryP[T](s.CH, sql, p)
	})
	if err != nil {
		return nil, err
	}
	rows := v.([]T)
	s.meta.mu.Lock()
	if s.meta.version != version {
		s.meta.mu.Unlock()
		return rows, nil
	}
	if s.meta.entries == nil {
		s.meta.entries = map[string]any{}
	}
	s.meta.entries[key] = rows
	s.meta.mu.Unlock()
	return slices.Clone(rows), nil
}

// cacheKey identifies a statement with its parameter values: the SQL, then
// each parameter's name, Go type and text encoding in name order.
func cacheKey(sql string, p ch.Params) (string, error) {
	if len(p) == 0 {
		return sql, nil
	}
	var b strings.Builder
	b.WriteString(sql)
	for _, name := range slices.Sorted(maps.Keys(p)) {
		enc, err := ch.EncodeParam(p[name])
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\x00%s\x00%T\x00%s", name, p[name], enc)
	}
	return b.String(), nil
}

// rawColumns lists a raw staging table's columns in file order, or none when
// no study staged that table. Staging tables exist only for the data types
// some ingested study has, and their columns follow its files.
func (s *State) rawColumns(table string) []string {
	type col struct {
		Name string `json:"name"`
	}
	cols, err := cachedQueryP[col](s,
		"SELECT name FROM system.columns WHERE database = 'cbioportal_raw' AND table = {table:String} ORDER BY position",
		ch.Params{"table": table})
	if err != nil {
		return nil
	}
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

// modelHasColumn reports whether a table of the portal database has a column.
func (s *State) modelHasColumn(table, column string) bool {
	type row struct {
		N uint64 `json:"n"`
	}
	rows, err := cachedQueryP[row](s,
		"SELECT count() AS n FROM system.columns WHERE database = currentDatabase() AND table = {table:String} AND name = {column:String}",
		ch.Params{"table": table, "column": column})
	return err == nil && len(rows) == 1 && rows[0].N > 0
}

// rawSource returns a FROM-clause source for a raw staging table that has at
// least the given columns: the table itself when it has them all, the table
// with each missing column added as an empty string, or an empty relation of
// those columns when the table is absent.
// rawFileOrder is the expression ordering a raw staging table's rows of one
// study as its files list them (transform.StagingOrderSQL); prefix qualifies
// the columns (e.g. "t.").
func (s *State) rawFileOrder(table, prefix string) string {
	return transform.StagingOrderSQL(prefix, slices.Contains(s.rawColumns(table), "row_num"))
}

func (s *State) rawSource(table string, cols ...string) string {
	have := s.rawColumns(table)
	if len(have) == 0 {
		sel := make([]string, len(cols))
		for i, c := range cols {
			sel[i] = "'' AS " + c
		}
		return fmt.Sprintf("(SELECT %s WHERE 0)", strings.Join(sel, ", "))
	}
	var add []string
	for _, c := range cols {
		if !slices.Contains(have, c) {
			add = append(add, "'' AS "+c)
		}
	}
	if len(add) == 0 {
		return "cbioportal_raw." + table
	}
	return fmt.Sprintf("(SELECT *, %s FROM cbioportal_raw.%s)", strings.Join(add, ", "), table)
}
