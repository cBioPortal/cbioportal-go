package core

import (
	"fmt"
	"strings"
	"sync"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// SQLArgs collects request values as ClickHouse query parameters while a
// statement is assembled: each method returns a {name:Type} placeholder to
// splice into the SQL in place of the value, and the statement runs with
// args.params(). Equal values share one placeholder. Safe for concurrent use,
// so fragments can be built from parallel goroutines.
type SQLArgs struct {
	mu   sync.Mutex
	p    ch.Params
	seen map[string]string
}

// NewArgs returns an empty SQLArgs.
func NewArgs() *SQLArgs { return &SQLArgs{p: ch.Params{}, seen: map[string]string{}} }

func (a *SQLArgs) bind(typ string, v any) string {
	enc, err := ch.EncodeParam(v)
	if err != nil {
		panic(err)
	}
	key := typ + "\x00" + enc
	a.mu.Lock()
	defer a.mu.Unlock()
	if ph, ok := a.seen[key]; ok {
		return ph
	}
	name := fmt.Sprintf("a%d", len(a.p))
	a.p[name] = v
	ph := "{" + name + ":" + typ + "}"
	a.seen[key] = ph
	return ph
}

// Str binds a String value.
func (a *SQLArgs) Str(v string) string { return a.bind("String", v) }

// Strs binds an Array(String) value, usable as `col IN <placeholder>` or as
// an array expression.
func (a *SQLArgs) Strs(v []string) string {
	if v == nil {
		v = []string{}
	}
	return a.bind("Array(String)", v)
}

// Pairs binds an Array(Tuple(String, String)) value, for `(a, b) IN …`.
func (a *SQLArgs) Pairs(v [][2]string) string {
	if v == nil {
		v = [][2]string{}
	}
	return a.bind("Array(Tuple(String, String))", v)
}

// Triples binds an Array(Tuple(String, String, String)) value.
func (a *SQLArgs) Triples(v [][3]string) string {
	if v == nil {
		v = [][3]string{}
	}
	return a.bind("Array(Tuple(String, String, String))", v)
}

// Params returns the bound values sql refers to, so one sqlArgs can serve
// several statements without each carrying the others' values.
func (a *SQLArgs) Params(sql string) ch.Params {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := ch.Params{}
	for name, v := range a.p {
		if strings.Contains(sql, "{"+name+":") {
			out[name] = v
		}
	}
	return out
}

// QueryA runs a SELECT with the values bound in a.
func QueryA[T any](c *ch.Client, sql string, a *SQLArgs) ([]T, error) {
	return ch.QueryP[T](c, sql, a.Params(sql))
}

// IntListSQL writes integers as a comma-separated SQL list.
func IntListSQL(vals []int64) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, ", ")
}
