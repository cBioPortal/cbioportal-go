package api

import (
	"bytes"
	"errors"
	"log"
	"net/http"
)

// arraySplitter cuts a JSON array, fed in pieces of any size, into its
// elements. Input must be valid JSON as encoding/json writes it.
type arraySplitter struct {
	buf   []byte
	state int // splitBefore, splitValue, splitIn, splitAfter, splitDone
	start int // offset of the current element in buf
	pos   int // scan offset in buf
	depth int
	inStr bool
	esc   bool
}

const (
	splitBefore = iota // before '['
	splitValue         // expecting an element or ']'
	splitIn            // inside an element
	splitAfter         // after an element: ',' or ']'
	splitDone          // after ']'
)

var errNotArray = errors.New("response is not a JSON array")

func isJSONSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' }

// feed scans p, calling elem with each element completed by it. The slice
// passed to elem is valid during the call only.
func (a *arraySplitter) feed(p []byte, elem func([]byte) error) error {
	a.buf = append(a.buf, p...)
	b := a.buf
	i := a.pos
	for i < len(b) {
		c := b[i]
		switch a.state {
		case splitBefore:
			if c == '[' {
				a.state = splitValue
			} else if !isJSONSpace(c) {
				return errNotArray
			}
			i++
		case splitValue:
			switch {
			case isJSONSpace(c):
				i++
			case c == ']':
				a.state = splitDone
				i++
			default:
				a.state, a.start, a.depth, a.inStr, a.esc = splitIn, i, 0, false, false
			}
		case splitIn:
			end := -1
			switch {
			case a.inStr:
				switch {
				case a.esc:
					a.esc = false
				case c == '\\':
					a.esc = true
				case c == '"':
					a.inStr = false
					if a.depth == 0 {
						end = i + 1
					}
				}
			case c == '"':
				a.inStr = true
			case c == '{' || c == '[':
				a.depth++
			case c == '}' || c == ']':
				if a.depth == 0 {
					end = i // a scalar ended by the array's ']'
				} else if a.depth--; a.depth == 0 {
					end = i + 1
				}
			case a.depth == 0 && (c == ',' || isJSONSpace(c)):
				end = i
			}
			if end < 0 {
				i++
				continue
			}
			if err := elem(b[a.start:end]); err != nil {
				return err
			}
			a.state = splitAfter
			i = end
		case splitAfter:
			switch {
			case isJSONSpace(c):
			case c == ',':
				a.state = splitValue
			case c == ']':
				a.state = splitDone
			default:
				return errNotArray
			}
			i++
		case splitDone:
			if !isJSONSpace(c) {
				return errNotArray
			}
			i++
		}
	}
	// Keep only the current element's bytes.
	keep := len(b)
	if a.state == splitIn {
		keep = a.start
	}
	n := copy(a.buf, b[keep:])
	a.buf = a.buf[:n]
	a.pos = i - keep
	a.start -= keep
	return nil
}

// complete reports whether the whole array has been fed.
func (a *arraySplitter) complete() bool { return a.state == splitDone }

// elementWriter receives a handler's response. A successful response whose
// body is a JSON array is handed to its consumer element by element as the
// handler writes it, so the array is never held; any other response is kept
// whole for whole.
type elementWriter struct {
	header    http.Header
	status    int
	raw       bytes.Buffer
	streaming bool
	whole     bool // decided: the response is kept in raw
	split     arraySplitter
	// start is called once the response is known to be a successful array;
	// returning false keeps the response whole instead.
	start func(header http.Header) bool
	// elem consumes one element.
	elem func([]byte) error
	err  error
}

func newElementWriter(start func(http.Header) bool, elem func([]byte) error) *elementWriter {
	return &elementWriter{header: http.Header{}, status: http.StatusOK, start: start, elem: elem}
}

func (e *elementWriter) Header() http.Header { return e.header }

func (e *elementWriter) WriteHeader(code int) {
	if e.raw.Len() == 0 && !e.streaming {
		e.status = code
	}
}

func (e *elementWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	if e.streaming {
		if e.err = e.split.feed(p, e.elem); e.err != nil {
			return 0, e.err
		}
		return len(p), nil
	}
	e.raw.Write(p)
	if e.whole {
		return len(p), nil
	}
	t := bytes.TrimLeft(e.raw.Bytes(), " \t\r\n")
	if e.status == http.StatusOK && len(t) == 0 {
		return len(p), nil // undecided until the first byte that is not space
	}
	if e.status != http.StatusOK || t[0] != '[' || !e.start(e.header) {
		e.whole = true
		return len(p), nil
	}
	e.streaming = true
	body := e.raw.Bytes()
	e.raw = bytes.Buffer{}
	if e.err = e.split.feed(body, e.elem); e.err != nil {
		return 0, e.err
	}
	return len(p), nil
}

// finish reports whether the response was streamed; one that ended
// incomplete or failed to split aborts the response.
func (e *elementWriter) finish() bool {
	if !e.streaming {
		return false
	}
	if e.err == nil && !e.split.complete() {
		e.err = errors.New("unterminated JSON array")
	}
	if e.err != nil {
		log.Printf("streamed response aborted: %v", e.err)
		panic(http.ErrAbortHandler)
	}
	return true
}
