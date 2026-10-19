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
// args.Params(sql). Equal values share one placeholder. Safe for concurrent use,
// so fragments can be built from parallel goroutines.
type SQLArgs struct {
	mu   sync.Mutex
	p    ch.Params
	seen map[string]string
	err  error
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

// Params returns the bound values sql refers to, so one SQLArgs can serve
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

// Fail records that a fragment bound in a could not be built, for a
// builder that returns SQL text only: the statements run with a then fail
// with err (the first one recorded).
func (a *SQLArgs) Fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err == nil {
		a.err = err
	}
}

// Err returns the error recorded by Fail, or nil.
func (a *SQLArgs) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err
}

// QueryA runs a SELECT with the values bound in a; it fails without running
// when a fragment of it could not be built (Fail).
func QueryA[T any](c *ch.Client, sql string, a *SQLArgs) ([]T, error) {
	if err := a.Err(); err != nil {
		return nil, err
	}
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
