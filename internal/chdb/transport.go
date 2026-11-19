package chdb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"unsafe"
)

// Transport returns an http.RoundTripper that answers ClickHouse HTTP
// interface requests (the ones internal/clickhouse sends) from the engine:
//
//   - the statement is the query parameter or field, or else the body;
//   - database, settings (any other parameter) and query parameters
//     (param_<name>) apply to that statement only;
//   - external tables (<name> file, <name>_structure, <name>_format) become
//     temporary tables of the connection for the statement;
//   - a body with a query parameter is an INSERT's data: an INSERT … SELECT
//     … FROM input(…) reads it from a temporary file, any other INSERT
//     streams it;
//   - a failed statement answers 500 with ClickHouse's error text.
func (e *Engine) Transport() http.RoundTripper { return transport{e} }

type transport struct{ e *Engine }

// notSettings are HTTP interface parameters that are not ClickHouse settings.
var notSettings = map[string]bool{
	"database": true, "query": true, "default_format": true, "user": true, "password": true,
	"query_id": true, "session_id": true, "session_timeout": true, "session_check": true,
	"compress": true, "decompress": true, "buffer_size": true, "wait_end_of_query": true,
	"quota_key": true, "role": true,
}

// serverDefaults are the settings whose default differs between a
// ClickHouse server's HTTP interface and chDB, at the server's value.
var serverDefaults = map[string]string{
	"output_format_json_quote_64bit_integers": "1",
}

type external struct {
	name, structure, format string
	data                    []byte
}

type request struct {
	sql, database, format string
	values                map[string]string // settings and param_<name>
	externals             []external
	data                  io.Reader // INSERT data, or nil
}

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	req, err := parseRequest(r)
	if err != nil {
		return respond(r, http.StatusBadRequest, []byte(err.Error())), nil
	}
	body, err := t.e.run(req)
	if err != nil {
		return respond(r, http.StatusInternalServerError, []byte(err.Error())), nil
	}
	return respond(r, http.StatusOK, body), nil
}

func respond(r *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain; charset=UTF-8"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}
}

func parseRequest(r *http.Request) (*request, error) {
	fields := r.URL.Query()
	req := &request{values: map[string]string{}}
	var files map[string][]byte
	if mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		mr := multipart.NewReader(r.Body, params["boundary"])
		files = map[string][]byte{}
		for {
			p, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			b, err := io.ReadAll(p)
			if err != nil {
				return nil, err
			}
			if p.FileName() != "" {
				files[p.FormName()] = b
			} else {
				fields.Add(p.FormName(), string(b))
			}
		}
	}
	req.database = fields.Get("database")
	req.format = fields.Get("default_format")
	for name, data := range files {
		format := fields.Get(name + "_format")
		if format == "" {
			format = "TabSeparated"
		}
		req.externals = append(req.externals, external{name: name, structure: fields.Get(name + "_structure"), format: format, data: data})
		fields.Del(name + "_structure")
		fields.Del(name + "_format")
		fields.Del(name + "_types")
	}
	for k, v := range serverDefaults {
		req.values[k] = v
	}
	for k, v := range fields {
		if !notSettings[k] && len(v) > 0 {
			req.values[k] = v[0]
		}
	}
	switch {
	case fields.Has("query"):
		req.sql = fields.Get("query")
		if files == nil && r.Body != nil {
			req.data = r.Body
		}
	case files == nil && r.Body != nil:
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		req.sql = string(b)
	}
	if strings.TrimSpace(req.sql) == "" {
		return nil, errors.New("Code: 62. DB::Exception: Empty query. (SYNTAX_ERROR)")
	}
	return req, nil
}

var (
	// formatClause is a statement's trailing FORMAT clause.
	formatClause = regexp.MustCompile(`(?is)\s+FORMAT\s+([A-Za-z0-9_]+)\s*;?\s*$`)
	// inputCall is the input() table function with its structure.
	inputCall = regexp.MustCompile(`(?s)\binput\s*\(\s*('(?:[^'\\]|\\.|'')*')\s*\)`)
	tempFiles atomic.Uint64
)

// run executes a request on a connection of the pool.
func (e *Engine) run(req *request) ([]byte, error) {
	c := e.acquire()
	defer e.release(c)
	if err := e.use(c, req.database); err != nil {
		return nil, err
	}
	reset, err := e.setAll(c, req.values)
	if err != nil {
		return nil, err
	}
	defer reset()
	for _, x := range req.externals {
		name := "`" + strings.ReplaceAll(x.name, "`", "``") + "`"
		if err := e.exec(c, fmt.Sprintf("CREATE TEMPORARY TABLE %s (%s) ENGINE = Memory", name, x.structure)); err != nil {
			return nil, err
		}
		defer e.exec(c, "DROP TEMPORARY TABLE IF EXISTS "+name)
		if err := e.insert(c, "INSERT INTO "+name, x.format, bytes.NewReader(x.data)); err != nil {
			return nil, err
		}
	}
	sql := req.sql
	if req.data != nil {
		m := formatClause.FindStringSubmatchIndex(sql)
		if m == nil {
			return nil, errors.New("Code: 62. DB::Exception: chdb: an INSERT with data needs a FORMAT clause. (SYNTAX_ERROR)")
		}
		format := sql[m[2]:m[3]]
		sql = sql[:m[0]]
		if inputCall.MatchString(sql) {
			return nil, e.insertFromInput(c, sql, format, req.data)
		}
		return nil, e.insert(c, sql, format, req.data)
	}
	format := req.format
	if format == "" {
		format = "TabSeparated"
	}
	r := e.l.query(c.h, sql, format)
	defer e.l.destroyResult(r)
	if msg := e.l.resultError(r); msg != "" {
		return nil, errors.New(strings.TrimSpace(msg))
	}
	return e.l.bytes(r), nil
}

// insert streams data in format into the INSERT statement sql (which has no
// FORMAT clause).
func (e *Engine) insert(c *conn, sql, format string, data io.Reader) error {
	s := e.l.streamInsert(c.h, sql, format)
	defer e.l.destroyInsertStream(s)
	if msg := e.l.streamInsertError(s); msg != "" {
		return errors.New(strings.TrimSpace(msg))
	}
	buf := make([]byte, 1<<20)
	for {
		n, err := data.Read(buf)
		if n > 0 && e.l.streamAppend(s, unsafe.Pointer(&buf[0]), uintptr(n)) != 0 {
			msg := e.l.streamInsertError(s)
			e.l.streamCancelInsert(s)
			return errors.New(strings.TrimSpace(msg))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			e.l.streamCancelInsert(s)
			return err
		}
	}
	r := e.l.streamDone(s)
	defer e.l.destroyResult(r)
	if msg := e.l.resultError(r); msg != "" {
		return errors.New(strings.TrimSpace(msg))
	}
	return nil
}

// insertFromInput runs an INSERT … SELECT … FROM input('structure'), which
// chDB cannot stream into: the data goes to a temporary file that file()
// reads in its place.
func (e *Engine) insertFromInput(c *conn, sql, format string, data io.Reader) error {
	path := fmt.Sprintf("%s/input-%d", e.tempDir, tempFiles.Add(1))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if _, err := io.Copy(f, data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	sql = inputCall.ReplaceAllStringFunc(sql, func(m string) string {
		structure := inputCall.FindStringSubmatch(m)[1]
		return fmt.Sprintf("file('%s', '%s', %s)", strings.ReplaceAll(path, "'", "\\'"), format, structure)
	})
	return e.exec(c, sql)
}

// URL is the base URL the ClickHouse client is given with this transport;
// requests never leave the process.
const URL = "http://chdb.local"
