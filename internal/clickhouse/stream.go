package clickhouse

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
)

// QueryStreamP runs a SELECT with query parameters in the given output
// format (the statement must not include a FORMAT clause) and returns the
// response body unread, so large results can be decoded as they arrive. The
// request is bound to ctx: cancelling it aborts the query. The caller closes
// the body.
func (c *Client) QueryStreamP(ctx context.Context, sql, format string, p Params) (io.ReadCloser, error) {
	req, err := c.newRequest(ctx, url.Values{"database": {c.database}}, c.rewrite(sql)+" FORMAT "+format, p)
	if err != nil {
		return nil, err
	}
	return c.do(req, "")
}

// RowBinaryReader decodes ClickHouse RowBinary values.
type RowBinaryReader struct {
	r   *bufio.Reader
	buf [8]byte
}

func NewRowBinaryReader(r io.Reader) *RowBinaryReader {
	return &RowBinaryReader{r: bufio.NewReaderSize(r, 1<<18)}
}

// More reports whether another row follows; it is false at a clean end of
// the stream.
func (b *RowBinaryReader) More() bool {
	_, err := b.r.Peek(1)
	return err == nil
}

// String reads a String into dst (reused) and returns it.
func (b *RowBinaryReader) String(dst []byte) ([]byte, error) {
	n, err := binary.ReadUvarint(b.r)
	if err != nil {
		return dst, noEOF(err)
	}
	if n > 1<<30 {
		return dst, fmt.Errorf("rowbinary: string length %d", n)
	}
	if uint64(cap(dst)) < n {
		dst = make([]byte, n)
	}
	dst = dst[:n]
	_, err = io.ReadFull(b.r, dst)
	return dst, noEOF(err)
}

func (b *RowBinaryReader) Int64() (int64, error) {
	if _, err := io.ReadFull(b.r, b.buf[:8]); err != nil {
		return 0, noEOF(err)
	}
	return int64(binary.LittleEndian.Uint64(b.buf[:8])), nil
}

func (b *RowBinaryReader) Float64() (float64, error) {
	v, err := b.Int64()
	return math.Float64frombits(uint64(v)), err
}

// noEOF reports a stream that ends inside a row as truncated.
func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
