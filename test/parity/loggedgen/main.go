// Command loggedgen writes a cmd/paritydiff corpus sampled from the requests
// production cBioPortal logged (cbioportal_qc.logged_requests): real API
// traffic, GET and POST, spread over the endpoints in proportion to their
// volume, or to its square root with -alloc sqrt (at least -min per endpoint
// either way). Replaying it against the Java API and this backend compares
// them on what clients actually ask.
//
//	CLICKHOUSE_URL=… CLICKHOUSE_USER=… CLICKHOUSE_PASSWORD=… \
//	  go run ./test/parity/loggedgen -n 500 > .parity/logged.txt
//	go run ./cmd/paritydiff -corpus .parity/logged.txt -b https://api.cbioportal.org \
//	  -ua cbioportal-go-parity -a http://localhost:18091
//
// Only successful requests with intact JSON bodies are taken (the largest
// logged bodies were captured gzip-corrupted), and never requests that write
// on the server they are replayed against (sessions, publishing, cache, data
// access tokens). Records are compared without
// regard to order, as this backend does not keep the Java importer's row
// order. Requests whose difference is expected (known.go: www's session
// store, deployment-specific fields, www bugs, unspecified tie order) are
// marked with their reason; -annotate marks them in an existing corpus:
//
//	go run ./test/parity/loggedgen -annotate < old.txt > .parity/logged.txt
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

func main() {
	url := flag.String("url", os.Getenv("CLICKHOUSE_URL"), "ClickHouse holding the request log")
	table := flag.String("table", "cbioportal_qc.logged_requests", "request log table")
	n := flag.Int("n", 500, "requests in the corpus (about)")
	minPer := flag.Int("min", 2, "requests per endpoint at least")
	maxBody := flag.Int("max-body", 200000, "skip POST bodies larger than this (bytes)")
	seed := flag.Int("seed", 1, "sample seed")
	alloc := flag.String("alloc", "volume", "share of n per endpoint: volume (proportional to its requests) or sqrt (to their square root)")
	annotateOnly := flag.Bool("annotate", false, "copy the corpus on stdin to stdout with the expected differences marked")
	flag.Parse()
	if *annotateOnly {
		if err := annotate(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "loggedgen:", err)
			os.Exit(1)
		}
		return
	}
	weight := map[string]string{"volume": "count()", "sqrt": "sqrt(count())"}[*alloc]
	if weight == "" {
		fmt.Fprintln(os.Stderr, "loggedgen: -alloc is volume or sqrt")
		os.Exit(2)
	}
	if *url == "" {
		fmt.Fprintln(os.Stderr, "loggedgen: set -url or CLICKHOUSE_URL")
		os.Exit(2)
	}

	type row struct {
		Endpoint string `json:"endpoint"`
		Method   string `json:"method"`
		Path     string `json:"path"`
		Query    string `json:"query_string"`
		Body     string `json:"body"`
	}
	// Endpoints get a share of n by their weight (-alloc), at least min;
	// within an endpoint, distinct requests are drawn by a seeded hash.
	// Requests that write on the server they are sent to (sessions, virtual
	// study publishing, cache eviction, data access tokens) are never
	// replayed, and do not take a share.
	const replayable = `response_status = 200 AND method IN ('GET', 'POST') AND startsWith(path, '/api/')
      AND NOT (startsWith(path, '/api/session') OR startsWith(path, '/api/public_virtual_studies')
               OR startsWith(path, '/api/cache') OR startsWith(path, '/api/data-access-token'))`
	sql := `
WITH clean AS (
    SELECT DISTINCT endpoint, method, path, query_string, if(method = 'POST', body, '') AS body
    FROM ` + *table + `
    WHERE ` + replayable + `
      AND (method = 'GET' OR (length(body) BETWEEN 2 AND {maxBody:UInt64}
           AND ((startsWith(body, '{') AND endsWith(body, '}')) OR (startsWith(body, '[') AND endsWith(body, ']')))
           AND position(body, unhex('EFBFBD')) = 0))
),
volume AS (
    SELECT endpoint, ` + weight + ` AS w, sum(` + weight + `) OVER () AS total
    FROM ` + *table + ` WHERE ` + replayable + ` GROUP BY endpoint
)
SELECT endpoint, method, path, query_string, body FROM (
    SELECT c.*, row_number() OVER (PARTITION BY c.endpoint ORDER BY cityHash64(c.path, c.query_string, c.body, {seed:UInt64})) AS rn,
           greatest({minPer:UInt64}, toUInt64(round(v.w * {n:UInt64} / v.total))) AS quota
    FROM clean c JOIN volume v USING (endpoint)
) WHERE rn <= quota
ORDER BY endpoint, rn`
	c := ch.New(*url, "default")
	rows, err := ch.QueryP[row](c, sql, ch.Params{
		"maxBody": *maxBody, "seed": *seed, "minPer": *minPer, "n": *n,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "loggedgen:", err)
		os.Exit(1)
	}

	fmt.Printf("# Requests sampled from %s by test/parity/loggedgen (-n %d -min %d -seed %d -alloc %s).\n", *table, *n, *minPer, *seed, *alloc)
	fmt.Println("# Records are compared unordered: this backend does not keep the Java importer's row order.")
	seen := map[string]int{}
	for _, r := range rows {
		name := strings.Trim(strings.NewReplacer("/api/", "", "/", "-", "{", "", "}", "").Replace(r.Endpoint), "-")
		seen[name]++
		target := r.Path
		if r.Query != "" {
			target += "?" + r.Query
		}
		header := fmt.Sprintf("%s-%s-%d unordered", strings.ToLower(r.Method), name, seen[name])
		fmt.Println()
		if opts, comment := expect(r.Method, target, r.Body); opts != "" {
			if comment != "" {
				fmt.Println("# " + comment)
			}
			header += " " + opts
		}
		fmt.Printf("## %s\n%s %s\n", header, r.Method, target)
		if r.Method == "POST" {
			fmt.Println(r.Body)
		}
	}
	fmt.Fprintf(os.Stderr, "loggedgen: %d requests over %d endpoints\n", len(rows), len(seen))
}
