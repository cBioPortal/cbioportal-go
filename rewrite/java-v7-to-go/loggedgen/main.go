// Command loggedgen writes a rewrite/java-v7-to-go/paritydiff corpus sampled from the requests
// production cBioPortal logged (cbioportal_qc.logged_requests): real API
// traffic, GET and POST, from a capture window (-since, -until) and the
// public portal's hosts (-servers), spread over the endpoints in proportion
// to their volume, or to its square root with -alloc sqrt (at least -min per
// endpoint either way). Requests to -api-server form a stratum of their own
// with -api-share of the corpus, so that host is represented whatever its
// share of the traffic. Replaying the corpus against the Java API and this
// backend compares them on what clients actually ask.
//
//	CLICKHOUSE_URL=… CLICKHOUSE_USER=… CLICKHOUSE_PASSWORD=… \
//	  go run ./rewrite/java-v7-to-go/loggedgen -n 500 > .parity/logged.txt
//	go run ./rewrite/java-v7-to-go/paritydiff -corpus .parity/logged.txt -b https://api.cbioportal.org \
//	  -ua cbioportal-go-parity -a http://localhost:18091
//
// The log is a ReplacingMergeTree keyed by a hash of the request, read with
// FINAL: each distinct request counts once, as last seen. Only successful
// requests with complete JSON bodies are taken (not body_truncated, not
// gzip-corrupted), and never requests that write on the server they are
// replayed against (sessions, publishing, cache, data access tokens).
// Records are compared without regard to order, as this backend does not
// keep the Java importer's row order. Requests whose difference is expected
// (known.go: www's session store, deployment-specific fields, www bugs,
// unspecified tie order) are marked with their reason; -annotate marks them
// in an existing corpus:
//
//	go run ./rewrite/java-v7-to-go/loggedgen -annotate < old.txt > .parity/logged.txt
//
// The corpus header records the window ("# since:", "# until:", "#
// window:"), the requests per host ("# server …") and the endpoint coverage
// ("# endpoints:"); entries sampled from -api-server are named "api-…".
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

const timeLayout = "2006-01-02 15:04:05"

func main() {
	url := flag.String("url", os.Getenv("CLICKHOUSE_URL"), "ClickHouse holding the request log")
	table := flag.String("table", "cbioportal_qc.logged_requests", "request log table")
	n := flag.Int("n", 500, "requests in the corpus (about)")
	minPer := flag.Int("min", 2, "requests per endpoint at least")
	maxBody := flag.Int("max-body", 200000, "skip POST bodies larger than this (bytes)")
	seed := flag.Int("seed", 1, "sample seed")
	alloc := flag.String("alloc", "volume", "share of n per endpoint: volume (proportional to its requests) or sqrt (to their square root)")
	since := flag.String("since", "2026-10-07 22:30:00", "window start, UTC (requests last seen at or after it)")
	until := flag.String("until", "", "window end, UTC, exclusive (empty: none)")
	servers := flag.String("servers", "www.cbioportal.org,api.cbioportal.org", "server names sampled, comma-separated")
	apiServer := flag.String("api-server", "api.cbioportal.org", "server name sampled as a stratum of its own")
	apiShare := flag.Float64("api-share", 0.25, "share of n drawn from -api-server's requests (0: no stratum of its own)")
	annotateOnly := flag.Bool("annotate", false, "copy the corpus on stdin to stdout with the expected differences marked")
	flag.Parse()
	if *annotateOnly {
		if err := annotate(os.Stdin, os.Stdout); err != nil {
			fail(err)
		}
		return
	}
	weight := map[string]string{"volume": "count()", "sqrt": "sqrt(count())"}[*alloc]
	if weight == "" {
		usage("-alloc is volume or sqrt")
	}
	if *url == "" {
		usage("set -url or CLICKHOUSE_URL")
	}
	from, err := time.Parse(timeLayout, *since)
	if err != nil {
		usage("-since: want YYYY-MM-DD hh:mm:ss")
	}
	var to int64
	if *until != "" {
		t, err := time.Parse(timeLayout, *until)
		if err != nil {
			usage("-until: want YYYY-MM-DD hh:mm:ss")
		}
		to = t.Unix()
	}
	if *apiShare < 0 || *apiShare > 1 {
		usage("-api-share is between 0 and 1")
	}
	nAPI := int(math.Round(float64(*n) * *apiShare))
	params := ch.Params{
		"since": from.Unix(), "until": to, "servers": strings.Split(*servers, ","),
		"apiServer": *apiServer, "apiStratum": *apiShare > 0, "maxBody": *maxBody,
		"seed": *seed, "minPer": *minPer, "nAPI": nAPI, "nRest": *n - nAPI,
	}

	// The window's replayable requests: successful GETs and POSTs to /api/
	// from the sampled hosts with complete bodies, except those that write
	// on the server they are sent to (sessions, virtual study publishing,
	// cache eviction, data access tokens). api marks the -api-server stratum.
	pool := `
    SELECT endpoint, method, path, query_string, body, server_name, seen,
           toUInt8({apiStratum:Bool} AND server_name = {apiServer:String}) AS api
    FROM ` + *table + ` FINAL
    WHERE seen >= toDateTime({since:Int64}) AND ({until:Int64} = 0 OR seen < toDateTime({until:Int64}))
      AND server_name IN {servers:Array(String)} AND body_truncated = 0
      AND response_status = 200 AND method IN ('GET', 'POST') AND startsWith(path, '/api/')
      AND NOT (startsWith(path, '/api/session') OR startsWith(path, '/api/public_virtual_studies')
               OR startsWith(path, '/api/cache') OR startsWith(path, '/api/data-access-token'))`

	// Within each stratum, endpoints get a share of its requests by their
	// weight (-alloc), at least min; within an endpoint, distinct requests
	// with intact JSON bodies are drawn by a seeded hash.
	sample := `
WITH pool AS (` + pool + `),
clean AS (
    SELECT DISTINCT api, endpoint, method, path, query_string, if(method = 'POST', body, '') AS body
    FROM pool
    WHERE method = 'GET' OR (length(body) BETWEEN 2 AND {maxBody:UInt64}
          AND ((startsWith(body, '{') AND endsWith(body, '}')) OR (startsWith(body, '[') AND endsWith(body, ']')))
          AND position(body, unhex('EFBFBD')) = 0)
),
volume AS (
    SELECT api, endpoint, ` + weight + ` AS w, sum(` + weight + `) OVER (PARTITION BY api) AS total
    FROM pool GROUP BY api, endpoint
)
SELECT api, endpoint, method, path, query_string, body FROM (
    SELECT c.*, row_number() OVER (PARTITION BY c.api, c.endpoint ORDER BY cityHash64(c.path, c.query_string, c.body, {seed:UInt64})) AS rn,
           greatest({minPer:UInt64}, toUInt64(round(v.w * if(c.api = 1, {nAPI:UInt64}, {nRest:UInt64}) / v.total))) AS quota
    FROM clean c JOIN volume v USING (api, endpoint)
) WHERE rn <= quota
ORDER BY api, endpoint, rn`

	type row struct {
		API      uint8  `json:"api"`
		Endpoint string `json:"endpoint"`
		Method   string `json:"method"`
		Path     string `json:"path"`
		Query    string `json:"query_string"`
		Body     string `json:"body"`
	}
	type poolRow struct {
		Server   string `json:"server_name"`
		Endpoint string `json:"endpoint"`
		Requests uint64 `json:"requests"`
		First    string `json:"first"`
		Last     string `json:"last"`
	}
	c := ch.New(*url, "default")
	stats, err := ch.QueryP[poolRow](c, `WITH pool AS (`+pool+`)
SELECT server_name, endpoint, count() AS requests, toString(min(seen)) AS first, toString(max(seen)) AS last
FROM pool GROUP BY server_name, endpoint`, params)
	if err != nil {
		fail(err)
	}
	rows, err := ch.QueryP[row](c, sample, params)
	if err != nil {
		fail(err)
	}

	// The window's extent, and each host's requests and endpoints in it.
	first, last := "", ""
	type hostStats struct {
		requests  uint64
		endpoints map[string]bool
	}
	byHost := map[string]*hostStats{}
	windowEndpoints := map[string]bool{}
	for _, s := range stats {
		if first == "" || s.First < first {
			first = s.First
		}
		if s.Last > last {
			last = s.Last
		}
		h := byHost[s.Server]
		if h == nil {
			h = &hostStats{endpoints: map[string]bool{}}
			byHost[s.Server] = h
		}
		h.requests += s.Requests
		h.endpoints[s.Endpoint] = true
		windowEndpoints[s.Endpoint] = true
	}
	sampledEndpoints := map[string]bool{}
	nFromAPI := 0
	for _, r := range rows {
		sampledEndpoints[r.Endpoint] = true
		nFromAPI += int(r.API)
	}
	hosts := make([]string, 0, len(byHost))
	for h := range byHost {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)

	fmt.Printf("# Requests sampled from %s by rewrite/java-v7-to-go/loggedgen (-n %d -min %d -seed %d -alloc %s -api-share %g).\n",
		*table, *n, *minPer, *seed, *alloc, *apiShare)
	fmt.Println("# Records are compared unordered: this backend does not keep the Java importer's row order.")
	fmt.Printf("# since: %s\n# until: %s\n", *since, *until)
	fmt.Printf("# window: %s to %s UTC (first and last request seen)\n", first, last)
	for _, h := range hosts {
		s := byHost[h]
		fmt.Printf("# server %s: %d read-only requests over %d endpoints\n", h, s.requests, len(s.endpoints))
	}
	fmt.Printf("# sampled: %d requests, %d of them (api-…) from %s\n", len(rows), nFromAPI, *apiServer)
	fmt.Printf("# endpoints: %d of the window's %d read-only endpoints sampled\n", len(sampledEndpoints), len(windowEndpoints))

	count := map[string]int{}
	for _, r := range rows {
		name := strings.Trim(strings.NewReplacer("/api/", "", "/", "-", "{", "", "}", "").Replace(r.Endpoint), "-")
		name = strings.ToLower(r.Method) + "-" + name
		if r.API == 1 {
			name = "api-" + name
		}
		count[name]++
		target := r.Path
		if r.Query != "" {
			target += "?" + r.Query
		}
		header := fmt.Sprintf("%s-%d unordered", name, count[name])
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
	fmt.Fprintf(os.Stderr, "loggedgen: %d requests (%d from %s) over %d of %d endpoints, window %s to %s\n",
		len(rows), nFromAPI, *apiServer, len(sampledEndpoints), len(windowEndpoints), first, last)
}

func usage(msg string) {
	fmt.Fprintln(os.Stderr, "loggedgen:", msg)
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "loggedgen:", err)
	os.Exit(1)
}
