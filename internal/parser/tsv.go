package parser

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
)

// Header is a data file's column header: original names in file order,
// trimmed (Columns) and exactly as written (Raw).
type Header struct {
	Columns []string
	Raw     []string
}

// ReadHeader reads the first non-`#` line of a TSV file as its column header.
func ReadHeader(path string) (*Header, error) {
	f, err := OpenText(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if len(line) == 0 && err != nil {
			return nil, fmt.Errorf("empty data file: %s", path)
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimRight(line, "\r\n")
		raw := strings.Split(line, "\t")
		cols := make([]string, len(raw))
		for i := range raw {
			cols[i] = strings.TrimSpace(raw[i])
		}
		return &Header{Columns: cols, Raw: raw}, nil
	}
}

// ReadSequencedSamples returns the sample ids listed on `#sequenced_samples:`
// lines among a MAF's leading comment lines (whitespace-separated), in file
// order. Lines after the first non-`#` line are not inspected.
func ReadSequencedSamples(path string) ([]string, error) {
	f, err := OpenText(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var out []string
	for {
		line, err := r.ReadString('\n')
		if !strings.HasPrefix(line, "#") {
			return out, nil
		}
		if i := strings.LastIndex(line, "sequenced_samples:"); i >= 0 {
			out = append(out, strings.Fields(line[i+len("sequenced_samples:"):])...)
		}
		if err != nil {
			return out, nil
		}
	}
}

// SanitizeIdent turns a raw column name into a safe ClickHouse identifier:
// lowercase, non-alphanumerics collapsed to `_`, leading digit prefixed.
func SanitizeIdent(raw string) string {
	var b strings.Builder
	for _, ch := range raw {
		switch {
		case (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9'):
			b.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			b.WriteRune(ch + 32)
		default:
			b.WriteByte('_')
		}
	}
	ident := strings.Trim(b.String(), "_")
	if ident == "" {
		ident = "col"
	}
	if ident[0] >= '0' && ident[0] <= '9' {
		ident = "_" + ident
	}
	return ident
}

// SanitizeColumns sanitizes a header's columns, disambiguating collisions.
func SanitizeColumns(columns []string) []string {
	seen := map[string]int{}
	out := make([]string, 0, len(columns))
	for _, c := range columns {
		base := SanitizeIdent(c)
		seen[base]++
		if seen[base] == 1 {
			out = append(out, base)
		} else {
			out = append(out, fmt.Sprintf("%s_%d", base, seen[base]))
		}
	}
	return out
}

// BodyReader is an io.Reader over a data file that drops `#` lines, blank
// lines and, optionally, the header line, yielding the pure data body for a
// FORMAT TabSeparatedRaw insert. Each emitted line is prefixed with its row
// number (1-based, counting data rows only, in file order) as an extra
// leading column. It optionally counts emitted data lines.
type BodyReader struct {
	inner      *bufio.Reader
	f          io.Closer
	skipHeader bool
	headerDone bool
	buf        []byte
	pos        int
	done       bool
	rows       uint64
	emitted    *uint64
}

func OpenBody(path string, skipHeader bool, emitted *uint64) (*BodyReader, error) {
	f, err := OpenText(path)
	if err != nil {
		return nil, err
	}
	return &BodyReader{
		inner:      bufio.NewReaderSize(f, 1<<20),
		f:          f,
		skipHeader: skipHeader,
		emitted:    emitted,
	}, nil
}

func (b *BodyReader) fill() bool {
	for {
		line, err := b.inner.ReadBytes('\n')
		if len(line) == 0 {
			if err != nil {
				b.done = true
				return false
			}
			continue
		}
		if line[0] == '#' {
			if len(line) == 1 && err != nil {
				b.done = true
				return false
			}
			continue
		}
		if isBlank(line) {
			if err != nil {
				b.done = true
				return false
			}
			continue
		}
		if b.skipHeader && !b.headerDone {
			b.headerDone = true
			if err != nil {
				b.done = true
				return false
			}
			continue
		}
		if b.emitted != nil {
			atomic.AddUint64(b.emitted, 1)
		}
		b.rows++
		b.buf = strconv.AppendUint(b.buf[:0], b.rows, 10)
		b.buf = append(append(b.buf, '\t'), line...)
		b.pos = 0
		return true
	}
}

func (b *BodyReader) Read(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		if b.pos >= len(b.buf) {
			if b.done || !b.fill() {
				if total > 0 {
					return total, nil
				}
				b.f.Close()
				return 0, io.EOF
			}
		}
		n := copy(p[total:], b.buf[b.pos:])
		b.pos += n
		total += n
	}
	return total, nil
}

func isBlank(line []byte) bool {
	for _, c := range line {
		if c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}

// MatrixShape is the identifier vs sample split of a wide matrix header.
type MatrixShape struct {
	IDColumns     []string // sanitized
	SampleColumns []string // original sample-id names
}

var reservedFeature = map[string]bool{
	"hugo_symbol": true, "entrez_gene_id": true, "gene_symbol": true,
	"composite_element_ref": true, "entity_stable_id": true, "name": true,
	"description": true, "url": true, "geneset_id": true, "transcript_id": true,
	"cytoband": true, "locus_id": true, "gene_id": true, "cancer_type": true,
	"stable_id": true, "generic_assay_type": true,
}

// Shape splits a matrix header: the leading run of reserved-named columns (at
// least the first) are identifiers; the rest are per-sample value columns.
func Shape(columns []string) MatrixShape {
	return ShapeWithFeatures(columns, nil)
}

// ShapeWithFeatures is Shape with extra identifier column names, matched
// case-insensitively as cbioportal-core matches a generic assay file's
// generic_entity_meta_properties.
func ShapeWithFeatures(columns, features []string) MatrixShape {
	san := SanitizeColumns(columns)
	extra := map[string]bool{}
	for _, f := range features {
		extra[strings.ToLower(strings.TrimSpace(f))] = true
	}
	idCount := 0
	for i, s := range san {
		if i == 0 || reservedFeature[s] || extra[strings.ToLower(strings.TrimSpace(columns[i]))] {
			idCount++
		} else {
			break
		}
	}
	samples := make([]string, 0, len(columns)-idCount)
	for _, c := range columns[idCount:] {
		samples = append(samples, strings.TrimSpace(c))
	}
	return MatrixShape{IDColumns: san[:idCount], SampleColumns: samples}
}

// MatrixUnpivotReader is an io.Reader that unpivots a wide gene×sample matrix
// into long-form TSV rows (`<row_num>\t<id fields…>\t<sample_id>\t<value>`
// per non-empty cell), where row_num numbers the emitted cells from 1 in file
// order (row by row, each row's cells in column order). Comment and header
// lines are consumed at construction.
type MatrixUnpivotReader struct {
	inner       *bufio.Reader
	f           io.Closer
	idCount     int
	sampleNames [][]byte
	out         bytes.Buffer
	num         []byte // the last cell's number, in decimal
	eof         bool
	emitted     *uint64
	seen        map[string]struct{} // identifier fields of the lines read
}

func OpenMatrix(path string, shape MatrixShape, emitted *uint64) (*MatrixUnpivotReader, error) {
	f, err := OpenText(path)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	for { // discard leading `#` lines and the header line
		line, err := r.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			break
		}
		if len(line) > 0 && line[0] == '#' {
			continue
		}
		break
	}
	names := make([][]byte, len(shape.SampleColumns))
	for i, s := range shape.SampleColumns {
		names[i] = []byte(s)
	}
	return &MatrixUnpivotReader{
		inner: r, f: f, idCount: len(shape.IDColumns), sampleNames: names, emitted: emitted, seen: map[string]struct{}{},
	}, nil
}

func (m *MatrixUnpivotReader) fill() bool {
	m.out.Reset()
	for m.out.Len() == 0 {
		line, err := m.inner.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			m.eof = true
			return false
		}
		if len(line) > 0 && line[0] == '#' {
			if err != nil {
				m.eof = true
				return false
			}
			continue
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			if err != nil {
				m.eof = true
				return false
			}
			continue
		}
		fields := bytes.Split(line, []byte{'\t'})
		if len(fields) <= m.idCount {
			if err != nil {
				m.eof = true
				return false
			}
			continue
		}
		prefix := bytes.Join(fields[:m.idCount], []byte{'\t'})
		// A line repeating an earlier line's identifiers is skipped, as
		// ImportTabDelimData ignores a duplicated gene's later rows.
		if _, dup := m.seen[string(prefix)]; dup {
			if err != nil {
				m.eof = true
				return false
			}
			continue
		}
		m.seen[string(prefix)] = struct{}{}
		var count uint64
		for j, sample := range m.sampleNames {
			idx := m.idCount + j
			if idx >= len(fields) || len(fields[idx]) == 0 {
				continue
			}
			m.num = incDecimal(m.num)
			m.out.Write(m.num)
			m.out.WriteByte('\t')
			m.out.Write(prefix)
			m.out.WriteByte('\t')
			m.out.Write(sample)
			m.out.WriteByte('\t')
			m.out.Write(fields[idx])
			m.out.WriteByte('\n')
			count++
		}
		if m.emitted != nil && count > 0 {
			atomic.AddUint64(m.emitted, count)
		}
		if err != nil {
			// last line consumed; if it produced output, serve it now.
			return m.out.Len() > 0
		}
	}
	return true
}

func (m *MatrixUnpivotReader) Read(p []byte) (int, error) {
	if m.out.Len() == 0 {
		if m.eof || !m.fill() {
			m.f.Close()
			return 0, io.EOF
		}
	}
	return m.out.Read(p)
}

// incDecimal increments the decimal number in num (empty is 0) in place.
func incDecimal(num []byte) []byte {
	for i := len(num) - 1; i >= 0; i-- {
		if num[i] < '9' {
			num[i]++
			return num
		}
		num[i] = '0'
	}
	return append([]byte{'1'}, num...)
}
