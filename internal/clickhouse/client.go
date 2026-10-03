// Package clickhouse is a minimal ClickHouse HTTP client for the ELT load path.
// DDL and scalar queries go in the request body; bulk inserts stream an
// io.Reader as the body while the INSERT … FROM input(…) statement rides in the
// query parameter, so raw TSV is never buffered in memory.
package clickhouse

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Client struct {
	baseURL  string
	database string
	http     *http.Client
	// aliases rewrites references to the default staging and reference
	// databases (cbioportal_raw., 'cbioportal_raw', ...) in SQL text.
	aliases *strings.Replacer
}

// SetDatabaseAliases points SQL that names the default staging/reference
// databases at other ones (empty keeps the default).
func (c *Client) SetDatabaseAliases(rawDB, refDB string) {
	var pairs []string
	if rawDB != "" && rawDB != "cbioportal_raw" {
		pairs = append(pairs, "cbioportal_raw.", rawDB+".", "'cbioportal_raw'", "'"+rawDB+"'")
	}
	if refDB != "" && refDB != "cbioportal_ref" {
		pairs = append(pairs, "cbioportal_ref.", refDB+".", "'cbioportal_ref'", "'"+refDB+"'")
	}
	if len(pairs) > 0 {
		c.aliases = strings.NewReplacer(pairs...)
	}
}

func (c *Client) rewrite(sql string) string {
	if c.aliases == nil {
		return sql
	}
	return c.aliases.Replace(sql)
}

// New returns a client for the ClickHouse HTTP interface at baseURL (e.g.
// http://localhost:18123 or https://<host>:8443). Credentials come from
// CLICKHOUSE_USER / CLICKHOUSE_PASSWORD; without them requests are sent as
// ClickHouse's default user.
func New(baseURL, database string) *Client {
	var rt http.RoundTripper = transport
	if user := os.Getenv("CLICKHOUSE_USER"); user != "" {
		rt = authTransport{base: transport, user: user, password: os.Getenv("CLICKHOUSE_PASSWORD")}
	}
	if comment := os.Getenv("CLICKHOUSE_LOG_COMMENT"); comment != "" {
		rt = commentTransport{base: rt, comment: comment}
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		database: database,
		http:     &http.Client{Timeout: time.Hour, Transport: rt},
	}
}

// authTransport authenticates every request with ClickHouse's user headers.
type authTransport struct {
	base           http.RoundTripper
	user, password string
}

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-ClickHouse-User", a.user)
	r.Header.Set("X-ClickHouse-Key", a.password)
	return a.base.RoundTrip(r)
}

// commentTransport sets ClickHouse's log_comment on every query, so the
// queries of one deployment (e.g. the API mirror) can be told apart in
// system.query_log even when deployments share a ClickHouse user.
type commentTransport struct {
	base    http.RoundTripper
	comment string
}

func (c commentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	q := r.URL.Query()
	q.Set("log_comment", c.comment)
	r.URL.RawQuery = q.Encode()
	return c.base.RoundTrip(r)
}

// transport keeps enough idle connections for a page's burst of parallel API
// requests, each issuing several queries, to reuse them.
var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 256
	t.MaxIdleConnsPerHost = 256
	return t
}()

func (c *Client) Database() string { return c.database }

// Execute runs a statement/query against the client's database and returns the
// text response. Used for DDL and small scalar reads.
func (c *Client) Execute(sql string) (string, error) {
	return c.post(url.Values{"database": {c.database}}, strings.NewReader(c.rewrite(sql)))
}

// ExecuteServerwide runs a statement without selecting a database (for CREATE
// DATABASE, which must not assume the target exists).
func (c *Client) ExecuteServerwide(sql string) (string, error) {
	return c.post(nil, strings.NewReader(sql))
}

// InsertStream streams body as the payload of an INSERT. insertSQL must end with
// the input format, e.g. `… FROM input('a String') FORMAT TabSeparatedRaw`.
func (c *Client) InsertStream(insertSQL string, body io.Reader) error {
	q := url.Values{
		"database": {c.database},
		"query":    {insertSQL},
		"input_format_tsv_allow_variable_number_of_columns": {"1"},
	}
	_, err := c.post(q, body)
	return err
}

func (c *Client) post(query url.Values, body io.Reader) (string, error) {
	u := c.baseURL + "/?" + withSessionSettings(query).Encode()
	req, err := http.NewRequest(http.MethodPost, u, body)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("clickhouse transport: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		head := insertSQLHead(query.Get("query"))
		return "", fmt.Errorf("clickhouse HTTP %d %s: %s", resp.StatusCode, head, string(out))
	}
	return string(out), nil
}

// withSessionSettings adds the settings every request runs with. Queries can
// embed id lists for a whole study (tens of thousands of samples), well past
// ClickHouse's default 256 KiB max_query_size.
func withSessionSettings(query url.Values) url.Values {
	if query == nil {
		query = url.Values{}
	}
	if query.Get("max_query_size") == "" {
		query.Set("max_query_size", "268435456")
	}
	return query
}

func insertSQLHead(q string) string {
	if len(q) > 120 {
		return q[:120] + "…"
	}
	return q
}

// DeleteWhere runs a synchronous ALTER … DELETE (mutations_sync=1) so the rows
// are gone before any subsequent count, making re-ingest idempotent.
func (c *Client) DeleteWhere(table, where string) {
	c.Execute(fmt.Sprintf(
		"ALTER TABLE `%s`.`%s` DELETE WHERE %s SETTINGS mutations_sync = 1",
		c.database, table, where))
}

// CreateDatabase creates the client's database if it does not exist.
func (c *Client) CreateDatabase() error {
	return c.EnsureDatabase(c.database)
}

// EnsureDatabase creates database name unless it already exists. Existing
// databases are left alone without issuing CREATE DATABASE, so a user granted
// only table-level privileges on pre-provisioned databases can run imports.
func (c *Client) EnsureDatabase(name string) error {
	out, err := c.ExecuteServerwide(fmt.Sprintf(
		"SELECT count() FROM system.databases WHERE name = '%s' FORMAT TabSeparated",
		strings.ReplaceAll(name, "'", "\\'")))
	if err == nil && strings.TrimSpace(out) == "1" {
		return nil
	}
	_, err = c.ExecuteServerwide(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", name))
	return err
}

// TableColumns returns the set of column names on a table (empty if absent).
func (c *Client) TableColumns(table string) (map[string]bool, error) {
	sql := fmt.Sprintf(
		"SELECT name FROM system.columns WHERE database = '%s' AND table = '%s' FORMAT TabSeparated",
		c.database, table)
	text, err := c.Execute(sql)
	if err != nil {
		return nil, err
	}
	cols := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		if line != "" {
			cols[line] = true
		}
	}
	return cols, nil
}

// TableExists reports whether a table exists in the client's database.
func (c *Client) TableExists(table string) (bool, error) {
	sql := fmt.Sprintf(
		"SELECT count() FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TabSeparated",
		c.database, table)
	text, err := c.Execute(sql)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(text) != "0", nil
}

// CountForFile returns the rows loaded for a specific study + source file.
func (c *Client) CountForFile(table, studyID, sourceFile string) (uint64, error) {
	sql := fmt.Sprintf(
		"SELECT count() FROM `%s`.`%s` WHERE study_id = '%s' AND source_file = '%s' FORMAT TabSeparated",
		c.database, table, SQLLit(studyID), SQLLit(sourceFile))
	text, err := c.Execute(sql)
	if err != nil {
		return 0, err
	}
	var n uint64
	fmt.Sscan(strings.TrimSpace(text), &n)
	return n, nil
}

// SQLLit escapes a value for a single-quoted ClickHouse string literal.
func SQLLit(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `'`, `\'`)
}
