package clickhouse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
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

// Query runs a SELECT and decodes each JSONEachRow row into T.
func Query[T any](c *Client, sql string) ([]T, error) {
	return QueryP[T](c, sql, nil)
}

// QueryP runs a SELECT with query parameters and decodes each JSONEachRow
// row into T.
func QueryP[T any](c *Client, sql string, p Params) ([]T, error) {
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
