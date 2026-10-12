package main

import (
	"bufio"
	"bytes"
	"container/heap"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
)

// Large responses (a whole study's copy-number calls can be 100 MB, and
// nothing bounds what an endpoint returns) are compared element by element
// in bounded memory: decoding both documents whole, normalizing and sorting
// them takes about ten times their size. compareStream reads top-level
// arrays from their (spooled) bodies one element at a time, keeping per
// element only a record of its position in the body, its sort keys and a
// hash of its canonical form. Records are sorted in memory up to sortBudget
// bytes and on disk past it (an external merge sort), and the elements whose
// canonical forms differ are decoded again from the body. The outcome is the
// whole comparison's: the same differences, in the same order.

// sortBudget is the memory in bytes one sort of element records may hold
// before it spills sorted runs to disk.
var sortBudget = 64 << 20

// spoolDir holds spooled bodies and sort runs (empty: the system's).
var spoolDir string

// sizedReaderAt is a response body: bytes in memory or a spooled file.
type sizedReaderAt interface {
	io.ReaderAt
	Size() int64
}

// record is a top-level array element of a response body.
type record struct {
	start, end int64    // its bytes in the body
	sum        [16]byte // hash of its canonical form
	run        int64    // the run of equal Ties fields it is in (Ties only)
	id         string   // canonical form of its identity fields
	seq        int64    // its position in the order compared
}

func (r *record) size() int { return 80 + len(r.id) }

// byIdentity orders records by tie run, identity, then content, as sortKey
// does (with a hash standing in for the canonical form).
func byIdentity(x, y *record) bool {
	if x.run != y.run {
		return x.run < y.run
	}
	if x.id != y.id {
		return x.id < y.id
	}
	return bytes.Compare(x.sum[:], y.sum[:]) < 0
}

// bySum orders records by content, then position.
func bySum(x, y *record) bool {
	if c := bytes.Compare(x.sum[:], y.sum[:]); c != 0 {
		return c < 0
	}
	return x.seq < y.seq
}

// recordStream is a sequence of records.
type recordStream interface {
	next() (record, bool, error)
}

// sliceStream streams records held in memory.
type sliceStream struct {
	recs []record
	i    int
}

func (s *sliceStream) next() (record, bool, error) {
	if s.i >= len(s.recs) {
		return record{}, false, nil
	}
	s.i++
	return s.recs[s.i-1], true, nil
}

// fileStream streams records from a run file.
type fileStream struct {
	r *bufio.Reader
}

func (s *fileStream) next() (record, bool, error) {
	var rec record
	var err error
	readInt := func() int64 {
		if err != nil {
			return 0
		}
		var v int64
		v, err = binary.ReadVarint(s.r)
		return v
	}
	rec.start = readInt()
	if errors.Is(err, io.EOF) {
		return record{}, false, nil
	}
	rec.end, rec.run, rec.seq = readInt(), readInt(), readInt()
	n := readInt()
	if err != nil {
		return record{}, false, err
	}
	if _, err := io.ReadFull(s.r, rec.sum[:]); err != nil {
		return record{}, false, err
	}
	id := make([]byte, n)
	if _, err := io.ReadFull(s.r, id); err != nil {
		return record{}, false, err
	}
	rec.id = string(id)
	return rec, true, nil
}

func writeRecord(w *bufio.Writer, rec *record) error {
	var b [binary.MaxVarintLen64]byte
	for _, v := range []int64{rec.start, rec.end, rec.run, rec.seq, int64(len(rec.id))} {
		if _, err := w.Write(b[:binary.PutVarint(b[:], v)]); err != nil {
			return err
		}
	}
	w.Write(rec.sum[:])
	_, err := w.WriteString(rec.id)
	return err
}

// sorter sorts records in memory up to sortBudget bytes, spilling sorted
// runs to temporary files past it.
type sorter struct {
	less  func(x, y *record) bool
	buf   []record
	bytes int
	runs  []*os.File
}

func (s *sorter) add(rec record) error {
	s.buf = append(s.buf, rec)
	s.bytes += rec.size()
	if s.bytes >= sortBudget {
		return s.spill()
	}
	return nil
}

func (s *sorter) sortBuf() {
	sort.Slice(s.buf, func(i, j int) bool { return s.less(&s.buf[i], &s.buf[j]) })
}

func (s *sorter) spill() error {
	s.sortBuf()
	f, err := os.CreateTemp(spoolDir, "paritydiff-run-*")
	if err != nil {
		return err
	}
	os.Remove(f.Name())
	s.runs = append(s.runs, f)
	w := bufio.NewWriterSize(f, 64<<10)
	for i := range s.buf {
		if err := writeRecord(w, &s.buf[i]); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	s.buf, s.bytes = nil, 0
	return nil
}

// sorted returns the records in order; close releases the runs.
func (s *sorter) sorted() (recordStream, error) {
	if len(s.runs) == 0 {
		s.sortBuf()
		return &sliceStream{recs: s.buf}, nil
	}
	if len(s.buf) > 0 {
		if err := s.spill(); err != nil {
			return nil, err
		}
	}
	m := &mergeStream{less: s.less}
	for _, f := range s.runs {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		in := &fileStream{bufio.NewReaderSize(f, 64<<10)}
		rec, ok, err := in.next()
		if err != nil {
			return nil, err
		}
		if ok {
			m.heads = append(m.heads, mergeHead{rec, in})
		}
	}
	heap.Init(m)
	return m, nil
}

func (s *sorter) close() {
	for _, f := range s.runs {
		f.Close()
	}
	s.runs, s.buf = nil, nil
}

// mergeStream merges sorted runs (a heap of their heads).
type mergeStream struct {
	less  func(x, y *record) bool
	heads []mergeHead
}

type mergeHead struct {
	rec record
	in  *fileStream
}

func (m *mergeStream) Len() int           { return len(m.heads) }
func (m *mergeStream) Less(i, j int) bool { return m.less(&m.heads[i].rec, &m.heads[j].rec) }
func (m *mergeStream) Swap(i, j int)      { m.heads[i], m.heads[j] = m.heads[j], m.heads[i] }
func (m *mergeStream) Push(x any)         { m.heads = append(m.heads, x.(mergeHead)) }
func (m *mergeStream) Pop() any {
	h := m.heads[len(m.heads)-1]
	m.heads = m.heads[:len(m.heads)-1]
	return h
}

func (m *mergeStream) next() (record, bool, error) {
	if len(m.heads) == 0 {
		return record{}, false, nil
	}
	rec := m.heads[0].rec
	nxt, ok, err := m.heads[0].in.next()
	if err != nil {
		return record{}, false, err
	}
	if ok {
		m.heads[0].rec = nxt
		heap.Fix(m, 0)
	} else {
		heap.Pop(m)
	}
	return rec, true, nil
}

// errNotArray marks a body that is not a JSON array.
var errNotArray = errors.New("not a JSON array")

// scanElements reads the elements of the top-level array in body, each
// normalized as compareJSON normalizes it, into s in body order (seq is the
// position). It returns the number of elements.
func scanElements(body sizedReaderAt, o *compareOptions, s *sorter) (int64, error) {
	d := json.NewDecoder(bufio.NewReaderSize(io.NewSectionReader(body, 0, body.Size()), 256<<10))
	d.UseNumber()
	if t, err := d.Token(); err != nil || t != json.Delim('[') {
		return 0, errNotArray
	}
	// Runs of equal Ties fields keep their order unless the whole array
	// is unordered; a non-object's Ties fields are empty.
	ties := len(o.Ties) > 0 && !o.Unordered && !o.UnorderedKeys[""]
	var n, run int64
	var lastTie string
	for d.More() {
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return 0, errNotArray
		}
		end := d.InputOffset()
		v, err := decodeJSON(raw)
		if err != nil {
			return 0, errNotArray
		}
		v = normalizeIn(v, "", o)
		rec := record{start: end - int64(len(raw)), end: end, seq: n}
		full := canonical(v)
		sum := sha256.Sum256([]byte(full))
		copy(rec.sum[:], sum[:16])
		tie := ""
		if m, isObj := v.(map[string]any); isObj {
			id := map[string]any{}
			for k, x := range m {
				if isIdentityField(k) {
					if _, nested := x.(map[string]any); !nested {
						id[k] = x
					}
				}
			}
			rec.id = canonical(id)
			if ties {
				t := map[string]any{}
				for _, f := range o.Ties {
					t[f] = m[f]
				}
				tie = canonical(t)
			}
		} else {
			rec.id = full
		}
		if ties && n > 0 && tie != lastTie {
			run++
		}
		lastTie = tie
		rec.run = run
		if err := s.add(rec); err != nil {
			return 0, err
		}
		n++
	}
	if t, err := d.Token(); err != nil || t != json.Delim(']') {
		return 0, errNotArray
	}
	if d.More() {
		return 0, errNotArray
	}
	if _, err := d.Token(); err != io.EOF {
		return 0, errNotArray
	}
	return n, nil
}

// compareStream compares two top-level JSON arrays as compareJSON does, in
// bounded memory; ok is false when either body is not an array.
func compareStream(a, b sizedReaderAt, o compareOptions) (r diffResult, ok bool) {
	r, err := compareStreamErr(a, b, &o)
	if errors.Is(err, errNotArray) {
		return diffResult{}, false
	}
	if err != nil {
		r = diffResult{}
		r.add(&o, "", "comparison failed: %v", err)
	}
	return r, true
}

func compareStreamErr(a, b sizedReaderAt, o *compareOptions) (r diffResult, err error) {
	unordered := o.Unordered || o.UnorderedKeys[""]
	less := byIdentity
	if !unordered && len(o.Ties) == 0 {
		less = nil // body order
	}
	order := func(body sizedReaderAt) (recordStream, int64, func(), error) {
		s := &sorter{less: less}
		if less == nil {
			// Body order needs no sort: spill everything past the budget
			// as one run, already in order.
			s.less = func(x, y *record) bool { return x.seq < y.seq }
		}
		n, err := scanElements(body, o, s)
		if err != nil {
			s.close()
			return nil, 0, nil, err
		}
		st, err := s.sorted()
		if err != nil {
			s.close()
			return nil, 0, nil, err
		}
		return st, n, s.close, nil
	}
	xs, nx, closeX, err := order(a)
	if err != nil {
		return r, err
	}
	defer closeX()
	ys, ny, closeY, err := order(b)
	if err != nil {
		return r, err
	}
	defer closeY()

	decode := func(body sizedReaderAt, rec record) (any, error) {
		raw := make([]byte, rec.end-rec.start)
		if _, err := body.ReadAt(raw, rec.start); err != nil {
			return nil, err
		}
		v, _ := decodeJSON(raw)
		return normalizeIn(v, "", o), nil
	}
	if nx == ny {
		var sub diffResult
		for i := int64(0); ; i++ {
			x, okX, err := xs.next()
			if err != nil {
				return r, err
			}
			y, _, err := ys.next()
			if err != nil {
				return r, err
			}
			if !okX {
				break
			}
			if x.sum != y.sum {
				va, err := decode(a, x)
				if err != nil {
					return r, err
				}
				vb, err := decode(b, y)
				if err != nil {
					return r, err
				}
				walk(va, vb, "["+strconv.FormatInt(i, 10)+"]", o, &sub)
			}
		}
		closeX()
		closeY()
		xs, ys = nil, nil
		if sub.Count == 0 {
			return r, nil
		}
		if !o.Unordered {
			onlyA, onlyB, err := sumDiff(a, b, o, less)
			if err != nil {
				return r, err
			}
			if onlyA.n == 0 && onlyB.n == 0 {
				r.add(o, "", "same %d elements in a different order", nx)
				return r, nil
			}
		}
		r.Count += sub.Count
		for _, d := range sub.Diffs {
			if len(r.Diffs) < o.MaxDiffs {
				r.Diffs = append(r.Diffs, d)
			}
		}
		return r, nil
	}
	closeX()
	closeY()
	xs, ys = nil, nil
	r.add(o, "", "length a=%d b=%d", nx, ny)
	onlyA, onlyB, err := sumDiff(a, b, o, less)
	if err != nil {
		return r, err
	}
	r.Lengths, r.OnlyA, r.OnlyB = true, onlyA.n, onlyB.n
	for _, side := range []struct {
		only firstOnly
		body sizedReaderAt
		name string
	}{{onlyA, a, "a"}, {onlyB, b, "b"}} {
		for _, rec := range side.only.first {
			v, err := decode(side.body, rec)
			if err != nil {
				return r, err
			}
			r.add(o, "[]", "only in %s: %s", side.name, short(v))
		}
		r.Count += side.only.n - len(side.only.first)
	}
	return r, nil
}

// firstOnly holds the number of unmatched elements of one side and the
// first maxDiffs of them in the order compared.
type firstOnly struct {
	n     int
	first []record
}

func (f *firstOnly) add(rec record, max int) {
	f.n++
	if max <= 0 {
		return
	}
	if len(f.first) < max {
		f.first = append(f.first, rec)
	} else if rec.seq < f.first[len(f.first)-1].seq {
		f.first[len(f.first)-1] = rec
	} else {
		return
	}
	sort.Slice(f.first, func(i, j int) bool { return f.first[i].seq < f.first[j].seq })
}

// sumDiff returns the elements of each array whose canonical form the other
// lacks (counting repeats), the later ones of a repeated form first left
// unmatched; seq is an element's position in the order compared (less, or
// the body's when nil).
func sumDiff(a, b sizedReaderAt, o *compareOptions, less func(x, y *record) bool) (onlyA, onlyB firstOnly, err error) {
	bySumOf := func(body sizedReaderAt) (recordStream, func(), error) {
		ordered := &sorter{less: less}
		if less == nil {
			ordered.less = func(x, y *record) bool { return x.seq < y.seq }
		}
		if _, err := scanElements(body, o, ordered); err != nil {
			ordered.close()
			return nil, nil, err
		}
		st, err := ordered.sorted()
		if err != nil {
			ordered.close()
			return nil, nil, err
		}
		s := &sorter{less: bySum}
		for seq := int64(0); ; seq++ {
			rec, ok, err := st.next()
			if err != nil || !ok {
				ordered.close()
				if err != nil {
					s.close()
					return nil, nil, err
				}
				break
			}
			rec.seq = seq
			if err := s.add(rec); err != nil {
				ordered.close()
				s.close()
				return nil, nil, err
			}
		}
		out, err := s.sorted()
		if err != nil {
			s.close()
			return nil, nil, err
		}
		return out, s.close, nil
	}
	xs, closeX, err := bySumOf(a)
	if err != nil {
		return onlyA, onlyB, err
	}
	defer closeX()
	ys, closeY, err := bySumOf(b)
	if err != nil {
		return onlyA, onlyB, err
	}
	defer closeY()
	x, okX, err := xs.next()
	if err != nil {
		return onlyA, onlyB, err
	}
	y, okY, err := ys.next()
	if err != nil {
		return onlyA, onlyB, err
	}
	for okX || okY {
		var c int
		switch {
		case !okY:
			c = -1
		case !okX:
			c = 1
		default:
			c = bytes.Compare(x.sum[:], y.sum[:])
		}
		switch {
		case c == 0:
			x, okX, err = xs.next()
			if err == nil {
				y, okY, err = ys.next()
			}
		case c < 0:
			onlyA.add(x, o.MaxDiffs)
			x, okX, err = xs.next()
		default:
			onlyB.add(y, o.MaxDiffs)
			y, okY, err = ys.next()
		}
		if err != nil {
			return onlyA, onlyB, err
		}
	}
	return onlyA, onlyB, nil
}
