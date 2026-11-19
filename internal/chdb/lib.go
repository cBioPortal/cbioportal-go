// Package chdb runs ClickHouse inside the process with chDB, the embedded
// ClickHouse engine, so cbioportal-local needs no ClickHouse server. The
// engine is the shared library libchdb, loaded at run time (no cgo): Load
// binds its C functions, Open starts the engine on a data directory, and
// Engine.Transport answers the HTTP requests of the ClickHouse client
// (internal/clickhouse) from it, so the importer and the API run unchanged.
package chdb

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// lib holds the C functions of libchdb (chdb.h) the package calls.
type lib struct {
	setSignalHandlers func(enabled int32)
	connect           func(argc int32, argv **byte) uintptr
	closeConn         func(conn uintptr)
	shutdown          func() int32
	set               func(conn uintptr, key string, value *byte) int32

	query         func(conn uintptr, query, format string) uintptr
	streamQuery   func(conn uintptr, query, format string) uintptr
	streamFetch   func(conn, result uintptr) uintptr
	streamCancel  func(conn, result uintptr)
	destroyResult func(result uintptr)
	resultBuffer  func(result uintptr) uintptr
	resultLength  func(result uintptr) uintptr
	resultError   func(result uintptr) string

	streamInsert        func(conn uintptr, query, format string) uintptr
	streamAppend        func(stream uintptr, data unsafe.Pointer, n uintptr) int32
	streamDone          func(stream uintptr) uintptr
	streamCancelInsert  func(stream uintptr)
	streamInsertError   func(stream uintptr) string
	destroyInsertStream func(stream uintptr)
}

var (
	loadMu sync.Mutex
	loaded = map[string]*lib{}
)

// load opens the library at path once per process and binds its functions.
func load(path string) (l *lib, err error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	if l := loaded[path]; l != nil {
		return l, nil
	}
	handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	// RegisterLibFunc panics on a missing symbol, as with an older library.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s is not a usable libchdb: %v", path, r)
		}
	}()
	l = &lib{}
	for name, fn := range map[string]any{
		"chdb_set_signal_handlers_enabled": &l.setSignalHandlers,
		"chdb_connect":                     &l.connect,
		"chdb_close_conn":                  &l.closeConn,
		"chdb_shutdown":                    &l.shutdown,
		"chdb_set":                         &l.set,
		"chdb_query":                       &l.query,
		"chdb_stream_query":                &l.streamQuery,
		"chdb_stream_fetch_result":         &l.streamFetch,
		"chdb_stream_cancel_query":         &l.streamCancel,
		"chdb_destroy_query_result":        &l.destroyResult,
		"chdb_result_buffer":               &l.resultBuffer,
		"chdb_result_length":               &l.resultLength,
		"chdb_result_error":                &l.resultError,
		"chdb_stream_insert":               &l.streamInsert,
		"chdb_stream_append":               &l.streamAppend,
		"chdb_stream_done":                 &l.streamDone,
		"chdb_stream_cancel_insert":        &l.streamCancelInsert,
		"chdb_stream_insert_error":         &l.streamInsertError,
		"chdb_destroy_insert_stream":       &l.destroyInsertStream,
	} {
		purego.RegisterLibFunc(fn, handle, name)
	}
	// The Go runtime handles signals itself; the engine must not take
	// them over.
	l.setSignalHandlers(0)
	loaded[path] = l
	return l, nil
}

// cString is s as a NUL-terminated C string, kept alive by the caller.
func cString(s string) *byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return &b[0]
}

// bytes copies a result's buffer.
func (l *lib) bytes(result uintptr) []byte {
	n := l.resultLength(result)
	if n == 0 {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(cPointer[byte](l.resultBuffer(result)), n)...)
}

// cPointer is the address p (memory the C library owns) as a Go pointer.
func cPointer[T any](p uintptr) *T {
	return *(**T)(unsafe.Pointer(&p))
}
