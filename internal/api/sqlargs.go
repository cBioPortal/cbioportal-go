package api

import (
	"fmt"
	"strings"
	"sync"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// sqlArgs collects request values as ClickHouse query parameters while a
// statement is assembled: each method returns a {name:Type} placeholder to
// splice into the SQL in place of the value, and the statement runs with
// args.params(). Equal values share one placeholder. Safe for concurrent use,
// so fragments can be built from parallel goroutines.
type sqlArgs struct {
	mu   sync.Mutex
	p    ch.Params
	seen map[string]string
}

func newArgs() *sqlArgs { return &sqlArgs{p: ch.Params{}, seen: map[string]string{}} }

func (a *sqlArgs) bind(typ string, v any) string {
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

// str binds a String value.
func (a *sqlArgs) str(v string) string { return a.bind("String", v) }

// strs binds an Array(String) value, usable as `col IN <placeholder>` or as
// an array expression.
func (a *sqlArgs) strs(v []string) string {
	if v == nil {
		v = []string{}
	}
	return a.bind("Array(String)", v)
}

// pairs binds an Array(Tuple(String, String)) value, for `(a, b) IN …`.
func (a *sqlArgs) pairs(v [][2]string) string {
	if v == nil {
		v = [][2]string{}
	}
	return a.bind("Array(Tuple(String, String))", v)
}

// triples binds an Array(Tuple(String, String, String)) value.
func (a *sqlArgs) triples(v [][3]string) string {
	if v == nil {
		v = [][3]string{}
	}
	return a.bind("Array(Tuple(String, String, String))", v)
}

// params returns the bound values sql refers to, so one sqlArgs can serve
// several statements without each carrying the others' values.
func (a *sqlArgs) params(sql string) ch.Params {
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

// queryA runs a SELECT with the values bound in a.
func queryA[T any](c *ch.Client, sql string, a *sqlArgs) ([]T, error) {
	return ch.QueryP[T](c, sql, a.params(sql))
}
