# Benchmark: go.cbioportal.org vs www.cbioportal.org

A first comparison of the Go backend with the Java backend in production,
run on 2026-10-02/03. Both serve the same data: go.cbioportal.org mirrors
www's ClickHouse database daily (see [Deployment](deployment.md)). Treat the
numbers as indicative — one run each, from one client, on shared
infrastructure.

## Method

The frontend repo's public-origin Playwright suite
(`end-to-end-test-playwright/tests/*.spec.ts`, 598 tests — the suite upstream
CI runs against www) was run from one machine against each origin, two
workers, `CI=1` (one retry):

```bash
cd frontend/end-to-end-test-playwright
CI=1 LOCALDEV=0 CBIOPORTAL_URL=https://www.cbioportal.org corepack pnpm exec playwright test --workers=2
CI=1 LOCALDEV=0 CBIOPORTAL_URL=https://go.cbioportal.org  corepack pnpm exec playwright test --workers=2
```

The www run went first and wrote the screenshot baselines, so the go run
compares its screenshots against www's.

Resource use comes from `kubectl top` (go pod, every 15 s during the run) and
Datadog (`kubernetes.cpu.usage.total`, `kubernetes.memory.working_set`,
`trace.servlet.request.hits` over the preceding 7 days for www).

## Page speed

Only tests that passed on both origins are compared (182 tests), using each
test's first passing attempt:

| | www | go |
|---|---|---|
| Total time of the 182 tests | 2,476 s | 2,312 s (−7%) |
| Median per-test time ratio (go/www) | | 0.86 |
| Tests faster on go | | 142 of 182 |

Largest differences:

- **Faster on go:** expression availability in multi-study queries
  (`expression-comparison.spec.ts`) took about half the time (≈20 s → ≈9 s
  per test); home page, results view, study view and plots tests were
  10–40% faster.
- **Slower on go:** the embeddings tab (`embeddings.spec.ts`, +17% overall,
  some tests +30–40%) and the gnomAD tooltip in the mutation table
  (15 s → 33 s). These are follow-ups.

go serves a cold, uncached API (no Redis, one pod); www has a warm Redis cache
and its own traffic.

## Resource use

### go.cbioportal.org during the run (68 min, 2 browsers)

| | Average | p95 | Max |
|---|---|---|---|
| CPU | 0.08 cores | 0.30 cores | 0.71 cores |
| Memory | 184 MiB | | 1,073 MiB |

About 335 CPU-seconds for the whole suite. Idle: 1 mCPU and 15 MiB.

### www.cbioportal.org, normal traffic (7-day averages)

www serves about 1.4 million API requests a day (10.2 million a week) from two
Java pools: `cbioportal-backend-public-green` (browsers) and
`cbioportal-backend-public-api-green` (API clients and headless browsers —
the www e2e run landed here).

| Component | Replicas | CPU / pod | Memory / pod | Memory request / limit |
|---|---|---|---|---|
| public-green (Java) | 1–2 | 0.39 cores | 2.8 GB | 8 / 12 GiB |
| public-api-green (Java) | 1–2 | 0.52 cores | 1.9 GB (p95 4.1 GB) | 4 / 7 GiB |
| Redis (public, 4 pods) | | 0.1 cores total | ~2.1 GB total | |
| Session service + MongoDB (shared with other portals) | 2 + 1 | | MongoDB ≈ 19 GB | |

## Cost estimate

**www's application tier** runs on dedicated on-demand nodes (us-east-1 list
prices, approximate):

| Nodes | Used by | ≈ $/month |
|---|---|---|
| 2 × t4g.xlarge (pool `cbio-api`) | public-api-green | 195 |
| ≈ 2 of 3 × r7g.xlarge (pool `cbioportal`) | public-green | 310 |
| ≈ ½ of 2 × r7g.large (pool `redis`) | public Redis | 80 |
| **Total** | | **≈ 590** |

Not included: the session service's r7i.xlarge (≈ $190/month, shared with
other portals) and ClickHouse Cloud, which both backends query.

**go's application tier** today is one pod requesting 250m CPU / 512 MiB on a
shared spot node — a few dollars a month. Sized for www's traffic with
headroom (two replicas for availability, 1 core and 2 GiB each), it fits on a
single r7g.large: ≈ $80/month on demand.

**Estimated saving: ≈ $500/month (≈ $6,000/year)** on the application tier,
plus Redis and the separate session service going away.

Caveats:

- go has not served www-scale traffic; the estimate extrapolates from one
  e2e run and from idle use. A load test at www's request rate is needed.
- **ClickHouse may cost more.** Without Redis, go sends more queries to
  ClickHouse per request than www (see [ClickHouse load](#clickhouse-load));
  a ClickHouse Cloud cost increase could offset part of the saving.
- go lacks features www has (data access tokens, SAML, the shared session
  service), see the README's TODO list.

## Parity on production traffic

Production logs the requests it serves (`cbioportal_qc.logged_requests`).
`test/parity/loggedgen` samples them into a corpus — GETs and POSTs, every
endpoint, in proportion to its volume — and `cmd/paritydiff` replays it
against api.cbioportal.org and against go reading the production mirror
(see [Testing](testing.md)). Requests that write on the server (sessions,
publishing, cache) are never replayed.

On 2026-10-03, 618 logged requests over 114 endpoints:

| | Requests |
|---|---|
| Identical | 609 |
| Known differences | 9 |
| Unexplained differences | 0 |

The known differences: www's own bugs (a resource join across studies,
`allSampleCount: 1`), deployment fields (`/api/info`, the API documents'
host), ids that live in www's session store (custom data), and three cases
where Java's order of tied rows is its import order, which go does not keep.

Reaching this fixed, among others: Java's paging applying only to the
endpoints that declare it, mRNA percentiles ranking ties at their maximum,
copy-number and variant counts using the profile's matrix samples, and
expression enrichments on the production mirror.

## ClickHouse load

ClickHouse's `system.query_log` per user, last 24 hours (2026-10-03):

| User | Queries | CPU | Read |
|---|---|---|---|
| `cbio_user` (www and api.cbioportal.org, Java) | 1.91 M | 176,000 s (≈ 2 cores) | 68 TiB |
| `go_portal` (go.cbioportal.org) | 12 k | 1,600 s | 452 GiB |

www and api.cbioportal.org served about 2.0 million requests that day
(Datadog APM), so Java costs ClickHouse about 0.94 queries, 0.09 CPU
seconds and 36 MB read per request, with Redis answering repeats.

Replaying the logged corpus, go needed about 2.5 queries, 0.25 CPU seconds
and 68 MB per request. That overstates go's cost: the corpus takes at least
two requests from every endpoint, so rare, heavy requests (enrichments,
study-view aggregations) weigh far more than in real traffic, and every
request is new (no cache). A like-for-like figure needs the same traffic on
both: the mirrored api.cbioportal.org traffic, with each deployment's
queries tagged by `log_comment` (`-clickhouse-log-comment`; for the Java
portal, `custom_settings=log_comment%3D…` in its JDBC URL).

## Code size

What there is to maintain, counted on 2026-10-03 (cbioportal `f4cedba`,
cbioportal-core `47890bb`, cbioportal-go `1331b57`): lines of code without
blank and comment lines; the frontend, which both use unchanged, is not
counted.

| | Main code | Tests |
|---|---|---|
| **cbioportal-go** (API + importer) | **≈ 26,500** — Go 26.1k (SQL inline), scripts 0.4k | ≈ 9,200 |
| cbioportal (Java backend) | ≈ 80,000 — Java 63.9k, MyBatis mapper XML 11.9k, SQL 1.3k, other 2.7k | ≈ 60,500 |
| cbioportal-core (Java importer + Python validator) | ≈ 33,300 — Java 23.5k, Python 7.9k, shell 1.5k, other 0.5k | ≈ 22,700 |
| **Java stack total** | **≈ 113,000** | ≈ 83,000 |

The Go code is about a quarter of the size, in one language and with two
small dependencies, a YAML parser and `golang.org/x/sync` (the Java stack builds on Spring Boot, MyBatis,
Jackson, commons-math and a Redis cache). Part of the difference is scope,
part is structure:

- **Not yet in Go:** the study validator (cbioportal-core's Python
  `validateData.py`, ≈ 8k lines), SAML login, data access tokens and a few
  MSK-only endpoints (see [API coverage](api-coverage.md)). Adding them is
  estimated at 5–10k lines, which keeps Go at roughly a third of the size.
- **Two backends in Java:** the Java portal carries both the legacy
  MySQL/MyBatis implementation and the ClickHouse column-store one, each with
  controllers, services, repositories, mapper XML and DTOs. Go has one path:
  a handler per endpoint with its SQL.
- **Tests:** the Java stack has ≈ 9× more test code. Go's behaviour is pinned
  mostly by API contract tests, the frontend's e2e suite and the parity
  comparison with www (`cmd/paritydiff`) rather than per-class unit tests.

For maintenance this means one ≈ 26k-line codebase in place of two
repositories and three languages, with one build (`go build`) and two small dependencies to track besides the
Go toolchain. The container image, frontend included, is ≈ 100 MB, against
1.33 GB for `cbioportal/cbioportal:master`.

## Why go is fast without Redis

- **An in-process cache** for the metadata the frontend asks for many times
  per page (studies, molecular profiles, sample ids). It is dropped when the
  tables change (checked at most once a second), so it needs no separate
  cache server and is never stale after an import.
- **Direct ClickHouse queries** against tables sorted for the API's filters
  (study, profile, gene), without an ORM or object mapping.
- **gzip** on all responses, and a small Go binary that starts in under a
  second and holds little memory.

## Not comparable in this run

The suite targets www, so many failures on go come from the environment, not
the backend:

- **Sessions.** Group comparison and some results-view tests load saved
  sessions from www's session service (e.g. `5cf89323e4b0ab413787436c`); go's
  built-in session store does not have them. These timeouts (~55 tests) and
  the serial tests skipped after them account for most of go's 129 failed and
  201 not-run tests.
- **Frontend version.** www loads its frontend from frontend.cbioportal.org;
  go serves the bundle built from the `frontend/` submodule (a different
  commit, Font Awesome 6 instead of 4). Most of the 69 screenshot
  differences, including the Oncoprinter tool, which barely uses the
  backend, come from this.
- **www's own run** had 209 failures, nearly all missing screenshot
  baselines on that first run.

A parity run would need the test sessions copied into go and both origins on
the same frontend build.
