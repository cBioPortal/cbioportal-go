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
- **ClickHouse is not measured.** Without Redis, go may send more queries to
  ClickHouse than www does; a ClickHouse Cloud cost increase could offset
  part of the saving. Measuring it needs `system.query_log` per user
  (the import user cannot read it).
- go lacks features www has (login, authorization, the shared session
  service), see the README's TODO list.

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
