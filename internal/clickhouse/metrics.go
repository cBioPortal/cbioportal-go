package clickhouse

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/metrics"
)

var (
	queriesTotal = metrics.NewCounterVec("cbioportal_clickhouse_queries_total",
		"ClickHouse HTTP requests by outcome (ok, error: ClickHouse answered an error, transport: no answer).", "outcome")
	queryDuration = metrics.NewHistogramVec("cbioportal_clickhouse_query_duration_seconds",
		"ClickHouse request time, until the response body is read.", metrics.DurationBuckets)
	readRows = metrics.NewCounterVec("cbioportal_clickhouse_read_rows_total",
		"Rows ClickHouse read, from X-ClickHouse-Summary (where the server reports it).")
	jsonDecodes = metrics.NewCounterVec("cbioportal_clickhouse_json_decoded_total",
		"Struct-row queries answered through JSONEachRow, by reason (columns: a result shape the driver path does not reproduce exactly, request: parameters too large for its request, error: a failed query).", "reason")
	readBytes = metrics.NewCounterVec("cbioportal_clickhouse_read_bytes_total",
		"Bytes ClickHouse read, from X-ClickHouse-Summary (where the server reports it).")
)

// observedTransport records every ClickHouse request in the metrics: its
// outcome, its duration until the body is closed (results stream), and the
// rows and bytes the server reports reading.
type observedTransport struct {
	base http.RoundTripper
}

func (o observedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := o.base.RoundTrip(r)
	if err != nil {
		queriesTotal.Inc("transport")
		queryDuration.Observe(time.Since(start).Seconds())
		return nil, err
	}
	outcome := "ok"
	if resp.StatusCode != http.StatusOK {
		outcome = "error"
	}
	recordSummary(resp.Header.Get("X-ClickHouse-Summary"))
	resp.Body = &observedBody{ReadCloser: resp.Body, done: func() {
		queriesTotal.Inc(outcome)
		queryDuration.Observe(time.Since(start).Seconds())
	}}
	return resp, nil
}

// observedBody reports once, when the body is closed.
type observedBody struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (b *observedBody) Close() error {
	b.once.Do(b.done)
	return b.ReadCloser.Close()
}

// recordSummary adds the read_rows / read_bytes of a summary header, e.g.
// {"read_rows":"5","read_bytes":"40",...}.
func recordSummary(h string) {
	if h == "" {
		return
	}
	var s struct {
		ReadRows  string `json:"read_rows"`
		ReadBytes string `json:"read_bytes"`
	}
	if json.Unmarshal([]byte(h), &s) != nil {
		return
	}
	if n, err := strconv.ParseUint(s.ReadRows, 10, 64); err == nil {
		readRows.Add(n)
	}
	if n, err := strconv.ParseUint(s.ReadBytes, 10, 64); err == nil {
		readBytes.Add(n)
	}
}
