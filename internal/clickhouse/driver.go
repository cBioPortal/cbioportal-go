package clickhouse

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/ext"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Reads that decode rows into structs go through clickhouse-go: the result
// arrives in ClickHouse's columnar Native format over the same HTTP endpoint,
// transport, credentials and settings as every other request.

// maxDriverURLParams bounds the encoded size of the parameters the driver
// sends in the request URL, within ClickHouse's 1 MiB http_max_uri_size.
// Arrays longer than a form field (maxFieldParam) go as external tables, as
// on the multipart path; past this total, so do the largest of the rest.
const maxDriverURLParams = 512 << 10

// nativeReads is false when CLICKHOUSE_DECODE=json, which decodes every
// result from JSONEachRow instead.
var nativeReads = os.Getenv("CLICKHOUSE_DECODE") != "json"

// driverConn returns the client's clickhouse-go connection pool, opened on
// first use.
func (c *Client) driverConn() (driver.Conn, error) {
	c.drvOnce.Do(func() {
		c.drv, c.drvErr = c.openDriver()
	})
	return c.drv, c.drvErr
}

func (c *Client) openDriver() (driver.Conn, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		port := "8123"
		if u.Scheme == "https" {
			port = "8443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	opt := &clickhouse.Options{
		Protocol:    clickhouse.HTTP,
		Addr:        []string{host},
		HttpUrlPath: u.Path,
		Auth:        clickhouse.Auth{Database: c.database},
		Settings:    clickhouse.Settings{"max_query_size": "268435456"},
		// Credentials, log_comment and metrics come from the client's own
		// transport chain, shared with the other requests.
		TransportFunc: func(*http.Transport) (http.RoundTripper, error) {
			return driverTransport{c.http.Transport}, nil
		},
		DialTimeout:     30 * time.Second,
		MaxOpenConns:    1024,
		MaxIdleConns:    256,
		ConnMaxLifetime: time.Hour,
	}
	if u.Scheme == "https" {
		opt.TLS = &tls.Config{ServerName: u.Hostname()}
	}
	return clickhouse.Open(opt)
}

// driverTransport drops the driver's own authentication (its default user,
// basic auth or certificate-auth header), so requests authenticate exactly as
// the client's other requests do.
type driverTransport struct {
	base http.RoundTripper
}

func (d driverTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.User = nil
	for _, h := range []string{"Authorization", "X-ClickHouse-User", "X-ClickHouse-Key", "X-ClickHouse-SSL-Certificate-Auth"} {
		r.Header.Del(h)
	}
	return d.base.RoundTrip(r)
}

// driverQuery prepares sql and its parameters for the driver: parameters in
// the URL while they are small, the largest array parameters as external
// tables otherwise. It reports false when the request is better sent by
// newRequest (a large scalar parameter, or a statement too long for the
// form field external tables need).
func driverQuery(sql string, p Params) (string, []clickhouse.QueryOption, bool) {
	if len(p) == 0 {
		return sql, nil, true
	}
	type enc struct {
		name, value string
		size        int
	}
	encs := make([]enc, 0, len(p))
	total := 0
	for name, v := range p {
		if !identRE.MatchString(name) {
			return "", nil, false
		}
		e, err := EncodeParam(v)
		if err != nil {
			return "", nil, false
		}
		size := len(url.QueryEscape(e)) + len(name) + 8
		encs = append(encs, enc{name, e, size})
		total += size
	}
	// Largest first, so the fewest parameters move to external tables.
	sort.Slice(encs, func(i, j int) bool {
		if encs[i].size != encs[j].size {
			return encs[i].size > encs[j].size
		}
		return encs[i].name < encs[j].name
	})
	params := clickhouse.Parameters{}
	var tables []*ext.Table
	for _, e := range encs {
		if len(e.value) > maxFieldParam || total > maxDriverURLParams {
			t, ok := tableParam(e.name, p[e.name])
			if !ok {
				return "", nil, false
			}
			et, err := driverTable(t, p[e.name])
			if err != nil {
				return "", nil, false
			}
			tables = append(tables, et)
			sql = substituteTable(sql, e.name, t)
			total -= e.size
			continue
		}
		params[e.name] = e.value
	}
	if total > maxDriverURLParams || len(tables) > 0 && len(sql) > maxFormQuery {
		return "", nil, false
	}
	opts := []clickhouse.QueryOption{clickhouse.WithParameters(params)}
	if len(tables) > 0 {
		opts = append(opts, clickhouse.WithExternalTable(tables...))
	}
	return sql, opts, true
}

// driverTable builds the Native-format external table for an array
// parameter, with the layout tableParam describes.
func driverTable(t extTable, v any) (*ext.Table, error) {
	cols := []func(*ext.Table) error{ext.Column("i", "UInt64")}
	switch v.(type) {
	case []string:
		cols = append(cols, ext.Column("v", "String"))
	case []int, []int64:
		cols = append(cols, ext.Column("v", "Int64"))
	case [][2]string:
		cols = append(cols, ext.Column("c1", "String"), ext.Column("c2", "String"))
	case [][3]string:
		cols = append(cols, ext.Column("c1", "String"), ext.Column("c2", "String"), ext.Column("c3", "String"))
	default:
		return nil, fmt.Errorf("clickhouse: no external table for %T", v)
	}
	tbl, err := ext.NewTable(t.name, cols...)
	if err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case []string:
		for i, s := range x {
			err = tbl.Append(uint64(i), s)
			if err != nil {
				return nil, err
			}
		}
	case []int:
		for i, n := range x {
			if err = tbl.Append(uint64(i), int64(n)); err != nil {
				return nil, err
			}
		}
	case []int64:
		for i, n := range x {
			if err = tbl.Append(uint64(i), n); err != nil {
				return nil, err
			}
		}
	case [][2]string:
		for i, r := range x {
			if err = tbl.Append(uint64(i), r[0], r[1]); err != nil {
				return nil, err
			}
		}
	case [][3]string:
		for i, r := range x {
			if err = tbl.Append(uint64(i), r[0], r[1], r[2]); err != nil {
				return nil, err
			}
		}
	}
	return tbl, nil
}

// queryRows runs sql through the driver. An error means the request should
// go through the JSON path instead.
func (c *Client) queryRows(ctx context.Context, sql string, p Params) (driver.Rows, error) {
	conn, err := c.driverConn()
	if err != nil {
		return nil, err
	}
	q, opts, ok := driverQuery(c.rewrite(sql), p)
	if !ok {
		return nil, errLargeRequest
	}
	return conn.Query(clickhouse.Context(ctx, opts...), q)
}

var errLargeRequest = errors.New("parameters need the multipart request")
