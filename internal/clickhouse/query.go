package clickhouse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// QueryJSONLines runs a SELECT and returns each result row as raw JSON. The
// statement must NOT include a FORMAT clause — JSONEachRow is appended. 64-bit
// integers are emitted as numbers, not quoted strings.
func (c *Client) QueryJSONLines(sql string) ([][]byte, error) {
	return c.QueryJSONLinesP(sql, nil)
}

// QueryJSONLinesP is QueryJSONLines with query parameters.
func (c *Client) QueryJSONLinesP(sql string, p Params) ([][]byte, error) {
	var lines [][]byte
	err := c.eachJSONLine(sql, p, func(line []byte) error {
		lines = append(lines, append([]byte(nil), line...))
		return nil
	})
	return lines, err
}

// eachJSONLine runs a SELECT as JSONEachRow and calls fn with each row as it
// arrives (the slice is reused).
func (c *Client) eachJSONLine(sql string, p Params, fn func([]byte) error) error {
	q := url.Values{
		"database": {c.database},
		"output_format_json_quote_64bit_integers": {"0"},
	}
	req, err := c.newRequest(context.Background(), q, c.rewrite(sql)+" FORMAT JSONEachRow", p)
	if err != nil {
		return err
	}
	rc, err := c.do(req, "")
	if err != nil {
		return err
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		if line := sc.Bytes(); len(bytes.TrimSpace(line)) > 0 {
			if err := fn(line); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// Query runs a SELECT and decodes each row into T.
func Query[T any](c *Client, sql string) ([]T, error) {
	return QueryP[T](c, sql, nil)
}

// QueryP runs a SELECT with query parameters and decodes each row into T as
// encoding/json decodes ClickHouse's JSONEachRow output. Struct rows are read
// through the driver's columnar format; results it cannot reproduce exactly,
// and any error, go through JSONEachRow.
func QueryP[T any](c *Client, sql string, p Params) ([]T, error) {
	if out, ok := queryNative[T](c, sql, p); ok {
		return out, nil
	}
	return queryJSON[T](c, sql, p)
}

// QueryEachP is QueryP for results too large to hold: it calls fn with each
// row in order (the *T is reused between calls) and keeps none. An error
// before the first call is the error QueryP returns; one after it (the
// query failing mid-stream, or fn's own) ends the stream.
func QueryEachP[T any](c *Client, sql string, p Params, fn func(*T) error) error {
	called := false
	counted := func(v *T) error {
		called = true
		return fn(v)
	}
	err, ok := eachNative(c, sql, p, streamSettings, counted)
	if ok || called {
		return err
	}
	return eachJSON(c, sql, p, fn)
}

func queryJSON[T any](c *Client, sql string, p Params) ([]T, error) {
	out := []T{}
	err := eachJSON(c, sql, p, func(v *T) error {
		out = append(out, *v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func eachJSON[T any](c *Client, sql string, p Params, fn func(*T) error) error {
	return c.eachJSONLine(sql, p, func(line []byte) error {
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return fmt.Errorf("decoding row: %w", err)
		}
		return fn(&v)
	})
}

// jsonOnly remembers statements whose results the JSON path decodes, so they
// are not first sent through the driver again.
var jsonOnly struct {
	sync.Mutex
	m map[planKey]bool
}

const maxJSONOnly = 4096

func isJSONOnly(k planKey) bool {
	jsonOnly.Lock()
	defer jsonOnly.Unlock()
	return jsonOnly.m[k]
}

func setJSONOnly(k planKey, reason error) {
	jsonOnly.Lock()
	defer jsonOnly.Unlock()
	if jsonOnly.m == nil || len(jsonOnly.m) >= maxJSONOnly {
		jsonOnly.m = map[planKey]bool{}
	}
	jsonOnly.m[k] = true
	logFallback("columns", k, reason)
}

// logFallback counts (and with CLICKHOUSE_DECODE=log, logs) a statement
// answered through the JSON path.
func logFallback(reason string, k planKey, err error) {
	jsonDecodes.Inc(reason)
	if logFallbacks {
		log.Printf("clickhouse: JSON decode (%s) for %s: %v: %s", reason, k.t, err, insertSQLHead(k.sig))
	}
}

// logFallbacks logs each statement that falls back to the JSON path
// (CLICKHOUSE_DECODE=log).
var logFallbacks = os.Getenv("CLICKHOUSE_DECODE") == "log"

// queryNative decodes the rows of a struct type T from the driver. ok is
// false when the JSON path must answer instead.
func queryNative[T any](c *Client, sql string, p Params) (out []T, ok bool) {
	out = []T{}
	err, ok := eachNative(c, sql, p, nil, func(v *T) error {
		out = append(out, *v)
		return nil
	})
	if !ok || err != nil {
		return nil, false
	}
	return out, true
}

// errStream marks a failure of the driver's stream itself, as opposed to an
// error returned by the row callback.
var errStream = errors.New("clickhouse: driver stream")

// eachNative calls fn for each row of a struct type T decoded from the
// driver. ok is false when the JSON path must answer instead (nothing could
// be decoded here, or the stream failed); err is then the reason, or else
// fn's error. settings, if any, apply to the query.
func eachNative[T any](c *Client, sql string, p Params, settings clickhouse.Settings, fn func(*T) error) (err error, ok bool) {
	t := reflect.TypeFor[T]()
	if !nativeReads || t.Kind() != reflect.Struct {
		return nil, false
	}
	key := planKey{t, sql}
	if isJSONOnly(key) {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	rows, err := c.queryRows(ctx, sql, p, settings)
	if err != nil {
		reason := "error"
		if err == errLargeRequest {
			reason = "request"
		}
		logFallback(reason, key, err)
		return err, false
	}
	defer rows.Close()
	plan, err := planFor(t, rows.Columns(), rows.ColumnTypes())
	if err != nil {
		cancel()
		setJSONOnly(key, err)
		return err, false
	}
	st := plan.newState()
	var v, zero T
	for rows.Next() {
		v = zero
		if err := st.scanRow(rows, unsafe.Pointer(&v)); err != nil {
			cancel()
			logFallback("error", key, err)
			return fmt.Errorf("%w: %w", errStream, err), false
		}
		if err := fn(&v); err != nil {
			cancel()
			return err, true
		}
	}
	if err := rows.Err(); err != nil {
		logFallback("error", key, err)
		return fmt.Errorf("%w: %w", errStream, err), false
	}
	return nil, true
}
