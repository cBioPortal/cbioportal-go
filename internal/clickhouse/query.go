package clickhouse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"reflect"
	"sync"
	"time"
	"unsafe"
)

// QueryJSONLines runs a SELECT and returns each result row as raw JSON. The
// statement must NOT include a FORMAT clause — JSONEachRow is appended. 64-bit
// integers are emitted as numbers, not quoted strings.
func (c *Client) QueryJSONLines(sql string) ([][]byte, error) {
	return c.QueryJSONLinesP(sql, nil)
}

// QueryJSONLinesP is QueryJSONLines with query parameters.
func (c *Client) QueryJSONLinesP(sql string, p Params) ([][]byte, error) {
	q := url.Values{
		"database": {c.database},
		"output_format_json_quote_64bit_integers": {"0"},
	}
	req, err := c.newRequest(context.Background(), q, c.rewrite(sql)+" FORMAT JSONEachRow", p)
	if err != nil {
		return nil, err
	}
	rc, err := c.do(req, "")
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		if line := sc.Bytes(); len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	return lines, sc.Err()
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

func queryJSON[T any](c *Client, sql string, p Params) ([]T, error) {
	lines, err := c.QueryJSONLinesP(sql, p)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(lines))
	for _, line := range lines {
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return nil, fmt.Errorf("decoding row: %w", err)
		}
		out = append(out, v)
	}
	return out, nil
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
	jsonDecodes.Inc()
	if logFallbacks {
		log.Printf("clickhouse: JSON decode for %s: %v: %s", k.t, reason, insertSQLHead(k.sig))
	}
}

// logFallbacks logs each statement that falls back to the JSON path
// (CLICKHOUSE_DECODE=log).
var logFallbacks = os.Getenv("CLICKHOUSE_DECODE") == "log"

// queryNative decodes the rows of a struct type T from the driver. ok is
// false when the JSON path must answer instead.
func queryNative[T any](c *Client, sql string, p Params) (out []T, ok bool) {
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
	rows, ok := c.queryRows(ctx, sql, p)
	if !ok {
		return nil, false
	}
	defer rows.Close()
	plan, err := planFor(t, rows.Columns(), rows.ColumnTypes())
	if err != nil {
		cancel()
		setJSONOnly(key, err)
		return nil, false
	}
	st := plan.newState()
	out = []T{}
	var zero T
	for rows.Next() {
		out = append(out, zero)
		if err := st.scanRow(rows, unsafe.Pointer(&out[len(out)-1])); err != nil {
			cancel()
			return nil, false
		}
	}
	if rows.Err() != nil {
		return nil, false
	}
	return out, true
}
