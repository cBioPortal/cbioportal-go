package chdb

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Engine is a chDB engine on one data directory, with a pool of
// connections to it. Each connection runs one statement at a time and keeps
// its own current database and settings.
type Engine struct {
	l       *lib
	path    string
	tempDir string
	pool    chan *conn
	all     []*conn
	closing sync.Once
}

type conn struct {
	ptr uintptr // chdb_connection *, for chdb_close_conn
	h   uintptr // chdb_connection
	db  string  // current database ("" until set)
}

// Open loads the library at libPath and starts the engine on the data
// directory dataPath with size connections (at least 1). Only one engine
// can run in a process.
func Open(libPath, dataPath string, size int) (*Engine, error) {
	l, err := load(libPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "cbioportal-chdb-")
	if err != nil {
		return nil, err
	}
	e := &Engine{l: l, path: dataPath, tempDir: tmp, pool: make(chan *conn, max(size, 1))}
	args := []string{"chdb", "--path=" + dataPath}
	argv := make([]*byte, len(args))
	for i, a := range args {
		argv[i] = cString(a)
	}
	for range max(size, 1) {
		ptr := l.connect(int32(len(argv)), &argv[0])
		if ptr == 0 {
			e.Close()
			return nil, fmt.Errorf("chdb: could not open %s", dataPath)
		}
		c := &conn{ptr: ptr, h: *cPointer[uintptr](ptr)}
		e.all = append(e.all, c)
		e.pool <- c
	}
	return e, nil
}

// Path is the engine's data directory.
func (e *Engine) Path() string { return e.path }

// Close closes every connection and stops the engine; it cannot be opened
// again in this process.
func (e *Engine) Close() error {
	var err error
	e.closing.Do(func() {
		for _, c := range e.all {
			e.l.closeConn(c.ptr)
		}
		if e.l.shutdown() != 0 {
			err = errors.New("chdb: the engine did not stop cleanly")
		}
		os.RemoveAll(e.tempDir)
	})
	return err
}

func (e *Engine) acquire() *conn  { return <-e.pool }
func (e *Engine) release(c *conn) { e.pool <- c }

// exec runs a statement that returns no rows of interest on c.
func (e *Engine) exec(c *conn, sql string) error {
	r := e.l.query(c.h, sql, "TabSeparated")
	defer e.l.destroyResult(r)
	if msg := e.l.resultError(r); msg != "" {
		return errors.New(strings.TrimSpace(msg))
	}
	return nil
}

// Query runs sql on any connection and returns its output in format.
func (e *Engine) Query(sql, format string) ([]byte, error) {
	c := e.acquire()
	defer e.release(c)
	r := e.l.query(c.h, sql, format)
	defer e.l.destroyResult(r)
	if msg := e.l.resultError(r); msg != "" {
		return nil, errors.New(strings.TrimSpace(msg))
	}
	return e.l.bytes(r), nil
}

// use makes db the current database of c ("default" when db is empty).
func (e *Engine) use(c *conn, db string) error {
	if db == "" {
		db = "default"
	}
	if c.db == db {
		return nil
	}
	if err := e.exec(c, "USE `"+strings.ReplaceAll(db, "`", "``")+"`"); err != nil {
		return err
	}
	c.db = db
	return nil
}

// setAll sets settings and query parameters (param_<name>) on c, and
// returns a function that resets them.
func (e *Engine) setAll(c *conn, values map[string]string) (func(), error) {
	var set []string
	reset := func() {
		for _, k := range set {
			e.l.set(c.h, k, nil)
		}
	}
	for k, v := range values {
		if e.l.set(c.h, k, cString(v)) != 0 {
			reset()
			return nil, fmt.Errorf("Code: 115. DB::Exception: chdb: cannot set %s. (UNKNOWN_SETTING)", k)
		}
		set = append(set, k)
	}
	return reset, nil
}
