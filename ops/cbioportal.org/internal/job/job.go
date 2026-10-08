// Package job holds what cBioPortal.org's import jobs share: settings from
// the environment, logging, ClickHouse access, the Markdown summary posted
// to GitHub, and the PR-description lists that configure a job.
package job

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// Env returns the environment variable k, or def when unset or empty.
func Env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// EnvSeconds reads a duration given in seconds, as the job manifests set
// them.
func EnvSeconds(k string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a number of seconds, not %q", k, v)
	}
	return time.Duration(n) * time.Second, nil
}

// EnvInt reads a non-negative integer.
func EnvInt(k string, def int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a number, not %q", k, v)
	}
	return n, nil
}

// Logf prints a progress line to stderr with a UTC time stamp.
func Logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// ClickHouseURL is CLICKHOUSE_URL, else a local server.
func ClickHouseURL() string { return Env("CLICKHOUSE_URL", "http://localhost:18123") }

// Query runs a statement and returns its answer without the final newline.
func Query(c *ch.Client, sql string) (string, error) {
	out, err := c.ExecuteServerwide(sql)
	return strings.TrimRight(out, "\n"), err
}

// TableExists reports whether db.name exists.
func TableExists(c *ch.Client, db, name string) (bool, error) {
	out, err := Query(c, fmt.Sprintf("SELECT count() FROM system.tables WHERE database = '%s' AND name = '%s' FORMAT TSV", ch.SQLLit(db), ch.SQLLit(name)))
	return out == "1", err
}

// Summary is the Markdown summary of a job, one list item per line.
type Summary struct{ b strings.Builder }

// Add appends a line.
func (s *Summary) Add(format string, args ...any) {
	fmt.Fprintf(&s.b, format, args...)
	s.b.WriteByte('\n')
}

func (s *Summary) String() string { return s.b.String() }

// BodyList returns the "- <item>" lines that follow a line starting with
// "<key>:" in text, up to the first line that is not such an item.
func BodyList(text, key string) []string {
	var out []string
	found := false
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r", ""), "\n") {
		if !found {
			found = strings.HasPrefix(line, key+":")
			continue
		}
		if len(line) < 2 || line[0] != '-' || (line[1] != ' ' && line[1] != '\t') {
			break
		}
		if item := strings.TrimSpace(line[1:]); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Intersect returns the items of a that are also in b, in a's order.
func Intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if in[x] {
			out = append(out, x)
		}
	}
	return out
}
