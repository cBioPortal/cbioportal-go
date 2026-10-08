package clickhouse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Params are values for ClickHouse server-side query parameters. The SQL
// refers to them as {name:Type}, e.g. `WHERE study_id = {study:String}` or
// `sample_id IN {ids:Array(String)}`, and the values travel separately from
// the statement, so they are never parsed as SQL.
//
// Supported value types: string, bool, int, int64, uint64, float64,
// []string, []int, []int64, [][2]string and [][3]string (for Array(Tuple(String, …))).
type Params map[string]any

const (
	// maxURLParams bounds the URL-encoded size of parameters sent in the
	// request URL; larger sets go in a multipart/form-data body.
	maxURLParams = 8 << 10
	// maxFieldParam is the largest encoded array sent as a form field.
	// ClickHouse caps form fields at http_max_field_value_size (128 KiB by
	// default), so longer arrays are sent as external tables instead.
	maxFieldParam = 64 << 10
	// maxFormQuery is the largest statement sent as a form field; a longer
	// one goes in the URL (bounded by http_max_uri_size, 1 MiB by default).
	maxFormQuery = 100 << 10
)

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EscapeParam encodes a scalar string in ClickHouse's Escaped text format, the
// format parameter values are parsed in.
func EscapeParam(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case 0:
			b.WriteString(`\0`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// quoteParam writes s as a single-quoted string as it appears inside an
// array or tuple value.
func quoteParam(b *strings.Builder, s string) {
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case 0:
			b.WriteString(`\0`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
}

// EncodeParam returns the text-format encoding of a parameter value.
func EncodeParam(v any) (string, error) {
	var b strings.Builder
	switch x := v.(type) {
	case string:
		return EscapeParam(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case []string:
		b.WriteByte('[')
		for i, s := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			quoteParam(&b, s)
		}
		b.WriteByte(']')
	case []int:
		b.WriteByte('[')
		for i, n := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Itoa(n))
		}
		b.WriteByte(']')
	case []int64:
		b.WriteByte('[')
		for i, n := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.FormatInt(n, 10))
		}
		b.WriteByte(']')
	case [][2]string:
		b.WriteByte('[')
		for i, p := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('(')
			quoteParam(&b, p[0])
			b.WriteByte(',')
			quoteParam(&b, p[1])
			b.WriteByte(')')
		}
		b.WriteByte(']')
	case [][3]string:
		b.WriteByte('[')
		for i, p := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('(')
			quoteParam(&b, p[0])
			b.WriteByte(',')
			quoteParam(&b, p[1])
			b.WriteByte(',')
			quoteParam(&b, p[2])
			b.WriteByte(')')
		}
		b.WriteByte(']')
	default:
		return "", fmt.Errorf("clickhouse: unsupported parameter type %T", v)
	}
	return b.String(), nil
}

// extTable is an array parameter sent as an external table: one row per
// element, numbered by i so the array form keeps the element order.
type extTable struct {
	name      string // table name in the query
	structure string // column definitions
	cols      string // element columns
	data      []byte // TabSeparated rows
}

// tableParam converts an array value to an external table, or reports false
// for a value that is not an array.
func tableParam(name string, v any) (extTable, bool) {
	t := extTable{name: "_param_" + name, structure: "i UInt64, v String", cols: "v"}
	var buf bytes.Buffer
	row := func(i int, fields ...string) {
		buf.WriteString(strconv.Itoa(i))
		for _, f := range fields {
			buf.WriteByte('\t')
			buf.WriteString(f)
		}
		buf.WriteByte('\n')
	}
	switch x := v.(type) {
	case []string:
		for i, s := range x {
			row(i, EscapeParam(s))
		}
	case []int:
		t.structure = "i UInt64, v Int64"
		for i, n := range x {
			row(i, strconv.Itoa(n))
		}
	case []int64:
		t.structure = "i UInt64, v Int64"
		for i, n := range x {
			row(i, strconv.FormatInt(n, 10))
		}
	case [][2]string:
		t.structure, t.cols = "i UInt64, c1 String, c2 String", "c1, c2"
		for i, p := range x {
			row(i, EscapeParam(p[0]), EscapeParam(p[1]))
		}
	case [][3]string:
		t.structure, t.cols = "i UInt64, c1 String, c2 String, c3 String", "c1, c2, c3"
		for i, p := range x {
			row(i, EscapeParam(p[0]), EscapeParam(p[1]), EscapeParam(p[2]))
		}
	default:
		return t, false
	}
	t.data = buf.Bytes()
	return t, true
}

// placeholderRE matches {name:Type} for a given name.
func placeholderRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`\{\s*` + regexp.QuoteMeta(name) + `\s*:[^{}]+\}`)
}

// precededByIN reports whether sql[:i] ends with the IN keyword (also NOT IN
// and GLOBAL IN).
func precededByIN(sql string, i int) bool {
	s := strings.TrimRight(sql[:i], " \t\r\n")
	if len(s) < 2 || !strings.EqualFold(s[len(s)-2:], "IN") {
		return false
	}
	if len(s) == 2 {
		return true
	}
	c := s[len(s)-3]
	return !(c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z')
}

// substituteTable replaces each {name:Array(...)} placeholder with a subquery
// over the external table: the table's rows on the right of IN, the rows
// collected into an array, in element order, anywhere else.
func substituteTable(sql, name string, t extTable) string {
	re := placeholderRE(name)
	elem := "x.2"
	if n := strings.Count(t.cols, ",") + 1; n > 1 {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf("x.%d", i+2)
		}
		elem = "(" + strings.Join(parts, ", ") + ")"
	}
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringIndex(sql, -1) {
		b.WriteString(sql[last:m[0]])
		if precededByIN(sql, m[0]) {
			fmt.Fprintf(&b, "(SELECT %s FROM %s)", t.cols, t.name)
		} else {
			fmt.Fprintf(&b, "(SELECT arrayMap(x -> %s, arraySort(groupArray((i, %s)))) FROM %s)", elem, t.cols, t.name)
		}
		last = m[1]
	}
	b.WriteString(sql[last:])
	return b.String()
}

// newRequest builds a POST running sql with the given URL settings and
// parameters. Without parameters the statement is the body. Small parameter
// sets ride in the URL; larger ones make the body multipart/form-data, with
// arrays too long for a form field sent as external tables.
func (c *Client) newRequest(ctx context.Context, q url.Values, sql string, p Params) (*http.Request, error) {
	q = withSessionSettings(c.withClientSettings(q))
	if len(p) == 0 {
		return http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/?"+q.Encode(), strings.NewReader(sql))
	}
	names := make([]string, 0, len(p))
	for name := range p {
		if !identRE.MatchString(name) {
			return nil, fmt.Errorf("clickhouse: invalid parameter name %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	fields := url.Values{}
	var tables []extTable
	for _, name := range names {
		enc, err := EncodeParam(p[name])
		if err != nil {
			return nil, err
		}
		if len(enc) > maxFieldParam {
			if t, ok := tableParam(name, p[name]); ok {
				tables = append(tables, t)
				sql = substituteTable(sql, name, t)
				continue
			}
		}
		fields.Set("param_"+name, enc)
	}
	if len(tables) == 0 && len(fields.Encode()) <= maxURLParams {
		for k, v := range fields {
			q[k] = v
		}
		return http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/?"+q.Encode(), strings.NewReader(sql))
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if len(sql) <= maxFormQuery {
		if err := mw.WriteField("query", sql); err != nil {
			return nil, err
		}
	} else {
		q.Set("query", sql)
	}
	fieldNames := make([]string, 0, len(fields))
	for k := range fields {
		fieldNames = append(fieldNames, k)
	}
	sort.Strings(fieldNames)
	for _, k := range fieldNames {
		if err := mw.WriteField(k, fields.Get(k)); err != nil {
			return nil, err
		}
	}
	for _, t := range tables {
		q.Set(t.name+"_structure", t.structure)
		q.Set(t.name+"_format", "TabSeparated")
		fw, err := mw.CreateFormFile(t.name, t.name+".tsv")
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(t.data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/?"+q.Encode(), &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req, nil
}

// do sends a request and returns the response body of a successful one.
// Errors name the start of the statement when sql is non-empty.
func (c *Client) do(req *http.Request, sql string) (io.ReadCloser, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clickhouse transport: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if sql == "" {
			return nil, fmt.Errorf("clickhouse HTTP %d: %s", resp.StatusCode, string(out))
		}
		return nil, fmt.Errorf("clickhouse HTTP %d %s: %s", resp.StatusCode, insertSQLHead(sql), string(out))
	}
	return resp.Body, nil
}

// ExecuteP runs a statement with query parameters and returns the text
// response.
func (c *Client) ExecuteP(sql string, p Params) (string, error) {
	sql = c.rewrite(sql)
	req, err := c.newRequest(context.Background(), url.Values{"database": {c.database}}, sql, p)
	if err != nil {
		return "", err
	}
	body, err := c.do(req, sql)
	if err != nil {
		return "", err
	}
	defer body.Close()
	out, err := io.ReadAll(body)
	return string(out), err
}
