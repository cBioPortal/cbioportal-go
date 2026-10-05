package api

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// redisClient is a minimal Redis client speaking RESP2 over TCP (or TLS):
// enough for the login store's GET, SET … PX, GETDEL and DEL. Connections
// authenticate (AUTH) and select the database on dial and are reused through
// a small idle pool; one that fails mid-command is dropped.
type redisClient struct {
	addr     string
	username string
	password string
	db       int
	tls      *tls.Config
	timeout  time.Duration
	idle     chan *redisConn
}

type redisConn struct {
	net.Conn
	r *bufio.Reader
}

// redisError is an error reply; the connection stays usable.
type redisError string

func (e redisError) Error() string { return "redis: " + string(e) }

func (c *redisClient) do(ctx context.Context, args ...string) (any, error) {
	conn, err := c.conn(ctx)
	if err != nil {
		return nil, err
	}
	v, err := conn.do(ctx, c.timeout, args...)
	var replyErr redisError
	if err != nil && !errors.As(err, &replyErr) {
		conn.Close()
		return nil, err
	}
	select {
	case c.idle <- conn:
	default:
		conn.Close()
	}
	return v, err
}

func (c *redisClient) conn(ctx context.Context) (*redisConn, error) {
	select {
	case conn := <-c.idle:
		return conn, nil
	default:
	}
	d := &net.Dialer{Timeout: c.timeout}
	var nc net.Conn
	var err error
	if c.tls != nil {
		nc, err = (&tls.Dialer{NetDialer: d, Config: c.tls}).DialContext(ctx, "tcp", c.addr)
	} else {
		nc, err = d.DialContext(ctx, "tcp", c.addr)
	}
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	conn := &redisConn{Conn: nc, r: bufio.NewReader(nc)}
	var setup [][]string
	switch {
	case c.password != "" && c.username != "":
		setup = append(setup, []string{"AUTH", c.username, c.password})
	case c.password != "":
		setup = append(setup, []string{"AUTH", c.password})
	}
	if c.db != 0 {
		setup = append(setup, []string{"SELECT", strconv.Itoa(c.db)})
	}
	for _, args := range setup {
		if _, err := conn.do(ctx, c.timeout, args...); err != nil {
			nc.Close()
			return nil, fmt.Errorf("redis %s: %w", args[0], err)
		}
	}
	return conn, nil
}

func (conn *redisConn) do(ctx context.Context, timeout time.Duration, args ...string) (any, error) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	buf := make([]byte, 0, 64)
	buf = append(buf, '*')
	buf = strconv.AppendInt(buf, int64(len(args)), 10)
	buf = append(buf, '\r', '\n')
	for _, a := range args {
		buf = append(buf, '$')
		buf = strconv.AppendInt(buf, int64(len(a)), 10)
		buf = append(buf, '\r', '\n')
		buf = append(buf, a...)
		buf = append(buf, '\r', '\n')
	}
	if _, err := conn.Write(buf); err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	return readRESP(conn.r)
}

// readRESP reads one reply: a simple string, error, integer, bulk string
// ([]byte, nil when absent) or array ([]any).
func readRESP(r *bufio.Reader) (any, error) {
	line, err := r.ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	if len(line) < 3 || line[len(line)-2] != '\r' {
		return nil, errors.New("redis: malformed reply")
	}
	kind, body := line[0], string(line[1:len(line)-2])
	switch kind {
	case '+':
		return body, nil
	case '-':
		return nil, redisError(body)
	case ':':
		n, err := strconv.ParseInt(body, 10, 64)
		if err != nil {
			return nil, errors.New("redis: malformed integer reply")
		}
		return n, nil
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil || n < -1 || n > 512<<20 {
			return nil, errors.New("redis: malformed bulk reply")
		}
		if n == -1 {
			return nil, nil
		}
		b := make([]byte, n+2)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, fmt.Errorf("redis: %w", err)
		}
		return b[:n], nil
	case '*':
		n, err := strconv.Atoi(body)
		if err != nil || n < -1 || n > 1<<20 {
			return nil, errors.New("redis: malformed array reply")
		}
		if n == -1 {
			return nil, nil
		}
		out := make([]any, n)
		for i := range out {
			if out[i], err = readRESP(r); err != nil {
				var replyErr redisError
				if !errors.As(err, &replyErr) {
					return nil, err
				}
			}
		}
		return out, nil
	}
	return nil, errors.New("redis: unknown reply type")
}
