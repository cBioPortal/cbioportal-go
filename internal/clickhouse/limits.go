package clickhouse

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// ClickHouse error codes of the per-query limits.
const (
	codeTimeoutExceeded     = 159 // max_execution_time
	codeTooSlow             = 160 // estimated to run past max_execution_time
	codeMemoryLimitExceeded = 241 // max_memory_usage, or a server-wide limit
)

// LimitExceeded reports which per-query limit stopped the query that failed
// with err: "memory" (max_memory_usage) or "time" (max_execution_time); ""
// for any other error. A server-wide memory limit (all queries together) is
// not a limit of the query.
func LimitExceeded(err error) string {
	if err == nil {
		return ""
	}
	var ex *clickhouse.Exception
	if errors.As(err, &ex) {
		return limitOf(int(ex.Code), ex.Message)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "(TIMEOUT_EXCEEDED)"):
		return limitOf(codeTimeoutExceeded, msg)
	case strings.Contains(msg, "(TOO_SLOW)"):
		return limitOf(codeTooSlow, msg)
	case strings.Contains(msg, "(MEMORY_LIMIT_EXCEEDED)"):
		return limitOf(codeMemoryLimitExceeded, msg)
	}
	return ""
}

func limitOf(code int, msg string) string {
	switch code {
	case codeTimeoutExceeded, codeTooSlow:
		return "time"
	case codeMemoryLimitExceeded:
		// "Query memory limit exceeded" (25.x), "Memory limit (for query)
		// exceeded" (earlier); "(total)" and "(for user)" are shared limits.
		m := strings.ToLower(msg)
		if strings.Contains(m, "query memory limit exceeded") || strings.Contains(m, "memory limit (for query) exceeded") {
			return "memory"
		}
	}
	return ""
}

// isExceptionLine reports whether a line of a JSONEachRow response is
// ClickHouse's report of the query failing mid-stream: the plain-text
// marker, or an {"exception": ...} row where the server writes errors in
// the output format.
func isExceptionLine(line []byte) bool {
	t := bytes.TrimSpace(line)
	return bytes.Equal(t, []byte("__exception__")) || bytes.HasPrefix(t, []byte(`{"exception"`))
}

// streamException returns the error a mid-stream exception reports, read
// from its line on.
func streamException(sc *bufio.Scanner) error {
	msg := strings.TrimSpace(sc.Text())
	for i := 0; i < 4 && !strings.Contains(msg, "DB::Exception") && sc.Scan(); i++ {
		if t := strings.TrimSpace(sc.Text()); t != "" && t != "__exception__" {
			msg = t
		}
	}
	return fmt.Errorf("clickhouse: query failed mid-stream: %s", msg)
}
