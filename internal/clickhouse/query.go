package clickhouse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// QueryJSONLines runs a SELECT and returns each result row as raw JSON. The
// statement must NOT include a FORMAT clause — JSONEachRow is appended. 64-bit
// integers are emitted as numbers, not quoted strings.
func (c *Client) QueryJSONLines(sql string) ([][]byte, error) {
	q := url.Values{
		"database":                              {c.database},
		"output_format_json_quote_64bit_integers": {"0"},
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/?"+q.Encode(),
		strings.NewReader(sql+" FORMAT JSONEachRow"))
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("clickhouse HTTP %d: %s", resp.StatusCode, string(body))
	}
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
	lines, err := c.QueryJSONLines(sql)
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
