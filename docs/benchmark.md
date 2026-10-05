# Benchmark: cbioportal-go vs the Java backend

## Summary

On the same live API requests (2026-10-05, www's non-browser traffic
mirrored 10% to go; details in [Mirrored production traffic](#mirrored-production-traffic)):

- **Latency:** Java is faster at the median (all `/api/`: 34 vs 91 ms in
  window A), mostly on small repeated requests that Redis answers; go has the
  lower tail on most busy routes (all `/api/` p99: 1.2 vs 2.4 s).
- **Weak spot:** `GET …/molecular-data` (a third of the traffic) is 5× slower
  on go (p50 191 vs 40 ms): it reads the raw staging matrix, ≈ 40 MB per
  request against ≈ 3.5 MB for Java.
- **ClickHouse:** go costs 1.5–2.3× Java's ClickHouse CPU per request and
  1.2–2.5× the bytes read.
- **Stability:** go was OOMKilled 12 times before streaming; none since the
  driver and streaming rollouts (21:30 UTC), with pods peaking at 0.4–1 GiB
  of their 2 GiB. Java's pool restarted 6 times on failed liveness probes.
- **Parity gaps:** go answers requests Java rejects with 400: a
  `…/molecular-data` GET without `entrezGeneId` returns the whole profile
  (16 million records, 174 MB, 72 s); `…/mutations/fetch` without exactly
  one of `sampleListId`/`sampleIds` returns 200.
- **Pods:** go runs at a tenth of Java's memory (60–330 MiB vs ≈ 1.5 GiB per
  pod) and comparable CPU per request (0.07–0.38 vs 0.10–0.25 s; go's high
  end is the whole-profile responses).
- **Single uncached requests:** go matches or beats www's first response on
  5 of 6 headline requests; www's repeats come from Redis.

## Contents

- [Mirrored production traffic](#mirrored-production-traffic)
- [Latency by route](#latency-by-route)
- [Errors by status](#errors-by-status)
- [Pods: CPU, memory, restarts](#pods-cpu-memory-restarts)
- [ClickHouse load per request](#clickhouse-load-per-request)
- [Headline requests](#headline-requests)
- [Parity on logged requests](#parity-on-logged-requests)
- [Frontend e2e run](#frontend-e2e-run)
- [Cost estimate](#cost-estimate)
- [Code size](#code-size)

## Mirrored production traffic

| | Java | go |
|---|---|---|
| Deployment | `cbioportal-backend-public-api-green`, 2 pods, `v7.1.2-web-shenandoah` | `cbioportal-go-api`, 2 pods, `cbioportal-go:latest` |
| Memory request / limit | 4 / 7 GiB | 512 MiB / 2 GiB |
| Response cache | Redis, 60 min TTL (`persistence.cache_type=redis`, `enable_persistent_cache=true`) | none (in-process metadata cache) |
| Database | production DB | `dev_cbioportal_public_go_green`: same content, same ClickHouse service |
| Requests | www's non-browser catch-all, plus api.cbioportal.org (≈ 1%) | 10% of the catch-all; responses discarded |

Traefik's `cbioportal-api-mirror-green` serves www's non-browser catch-all from
the Java pool and copies 10% (POST bodies up to 1 MiB) to go, since 16:50 UTC
(before that the mirror sat on api.cbioportal.org, ≈ 1,500 requests a day).
Windows, split at go's rollouts:

| Window (UTC, 2026-10-05) | go image | Java requests | go requests |
|---|---|---|---|
| **A** 16:50–21:30 | `b5539a9`, `6db788f`, `13d2f6b`: responses built in memory | 69,314 | 7,254 (10.5%) |
| **B** 21:30–22:46 | `6419f10`, `5e7d317`: clickhouse-go driver, molecular data streamed | 31,526 | 3,196 (10.1%) |
| **C** 22:46–23:30 | `b7ccfdf`, `186502c`: all large responses streamed | 7,550 | 852 (11.3%) |

Coverage: 10–12% on every busy route, the same GET/POST/HEAD mix (79/18/3%);
3 of 18,769 POSTs exceeded 1 MiB and were not mirrored. Browser traffic
(≈ 15× the catch-all) is not mirrored. Traffic is light (≈ 4 requests/s on
Java, 0.4 on go): this compares cost per request, not behaviour under load.

## Latency by route

Method: Java from Traefik's access log (`@OriginDuration`), go from its own
(`duration_ms`); Datadog log percentiles, all statuses. Window A, ms:

| Route | n Java / go | p50 Java / go | p95 Java / go | p99 Java / go |
|---|---|---|---|---|
| GET …/molecular-profiles/{id}/molecular-data | 21,538 / 2,158 | 40 / 191 | 163 / 686 | 342 / 1,437 |
| GET …/molecular-profiles/{id}/mutations | 8,818 / 943 | 7.7 / 73 | 163 / 361 | 329 / 508 |
| POST …/molecular-profiles/{id}/mutations/fetch | 4,800 / 493 | 52 / 50 | 732 / 302 | 3,552 / 743 |
| GET /api/studies/{id}/molecular-profiles | 4,840 / 489 | 6.8 / 14 | 70 / 73 | 142 / 116 |
| GET /api/studies/{id}/sample-lists | 3,564 / 367 | 18 / 18 | 195 / 54 | 1,387 / 136 |
| GET /api/studies/{id}/clinical-data | 3,205 / 336 | 48 / 125 | 623 / 573 | 3,279 / 821 |
| POST …/molecular-profiles/{id}/molecular-data/fetch | 1,967 / 241 | 119 / 228 | 23,748 / 1,495 | 39,154 / 3,606 |
| GET /api/studies/{id}/samples | 1,858 / 187 | 34 / 36 | 154 / 162 | 462 / 273 |
| POST /api/mutations/fetch | 1,329 / 137 | 91 / 76 | 378 / 233 | 1,049 / 550 |
| POST /api/genes/fetch | 1,258 / 121 | 5.1 / 15 | 58 / 34 | 93 / 45 |
| GET /api/studies | 1,035 / 135 | 21 / 25 | 394 / 518 | 3,481 / 1,620 |
| POST …/discrete-copy-number/fetch | 941 / 105 | 188 / 169 | 2,029 / 2,276 | 8,393 / 6,312 |
| **All /api/, window A** | 65,099 / 6,832 | **34 / 91** | **310 / 508** | **2,429 / 1,224** |
| All /api/, window B | 30,501 / 3,107 | 6.9 / 15 | 216 / 206 | 1,415 / 758 |
| All /api/, window C | 6,947 / 801 | 21 / 29 | 248 / 314 | 2,112 / 5,066 |

Java's medians benefit from Redis; go pays a ClickHouse round trip
(≈ 10–20 ms) on every request outside its metadata cache. go's p99 in window C
is two whole-profile responses (see below). Window B's mix differs (one client
sent ≈ 11,000 malformed `…/mutations` GETs), so compare within a window.

## Errors by status

| Window | 2xx Java / go | 400 Java / go | 404 Java / go | 5xx Java / go |
|---|---|---|---|---|
| A | 94.9 / 95.6% | 2.4 / 2.3% | 2.4 / 2.1% | 35 (502, 500) / 0 |
| B | 54.0 / 61.6% | 37.1 / 30.6% | 7.9 / 7.6% | 0 / 0 |
| C | 73.4 / 74.8% | 11.3 / 11.4% | 14.3 / 13.6% | 0 / 0 |

go's lower 400 rate in B is parity gaps: Java rejected 1,635
`…/mutations/fetch` requests (both or neither of `sampleListId`/`sampleIds`,
or unknown JSON fields) that go answered, and go served whole profiles for
`…/molecular-data` GETs without `entrezGeneId` (Java: 400 in 4 ms; go: 174 MB
in ≈ 72 s, 2.1 GiB of ClickHouse memory). Requests cut off by go's OOMKills
in window A are not in its log.

## Pods: CPU, memory, restarts

Method: Datadog `kubernetes.cpu.usage.total` (sum over pods),
`kubernetes.memory.usage`, `kubernetes_state.container.restarts`.

| Window | CPU-s per request Java / go | Memory per pod, avg Java / go | Max pod Java / go | Restarts Java / go |
|---|---|---|---|---|
| A | 0.25 / 0.07 | 1,529 / 179 MiB | 6,690 / 1,900 MiB | 6 / 12 (OOMKilled) |
| B | 0.10 / 0.12 | 1,371 / 331 MiB | 5,082 / 968 MiB | 0 / 0 |
| C | 0.18 / 0.38 | 1,475 / 56 MiB | 4,110 / 400 MiB | 0 / 0 |

go's OOMKills (17:22–21:25) came from large responses built in memory; with
streaming, a 174 MB response stays below 1 GiB. Java's restarts (20:40–21:13)
were kills after failed liveness probes, not OOM. go's CPU in B and C includes
the whole-profile responses Java rejects; Java's includes probes, GC and the
Datadog profiler.

## ClickHouse load per request

Method: `system.query_log` by `log_comment` (`cbioportal-api`,
`cbioportal-go-api`, both live since 14:01 UTC) divided by each side's
requests; Java's `SELECT 1` connection checks (0.15–0.27 per request) and
go's whole-profile queries (B: 4, C: 2) excluded.

| Window | Queries Java / go | Bytes read Java / go | Query time Java / go | ClickHouse CPU Java / go |
|---|---|---|---|---|
| A | 0.68 / 1.91 | 22.1 / 56.1 MB | 67 / 131 ms | 74 / 172 ms (2.3×) |
| B | 0.22 / 1.16 | 18.9 / 23.3 MB | 23 / 39 ms | 48 / 71 ms (1.5×) |
| C | 0.44 / 1.55 | 27.9 / 55.8 MB | 31 / 100 ms | 78 / 156 ms (2.0×) |

Single-gene `GET …/molecular-data` is most of the gap: in window A go read
110 GB for 2,153 requests, Java 75 GB for 21,449. go's query count includes
its metadata-cache freshness check (≈ 0.3 per request); Java's is below one
because Redis answers repeats and invalid requests never reach ClickHouse.

## Headline requests

Method: one client outside the cluster, 2026-10-05 ≈ 23:05 UTC, go on
`186502c`; each request three times in a row, 1 s apart. Total ms, first /
mean of the repeats:

| Request | www | go |
|---|---|---|
| `GET /api/studies` | 256 / 205 | 222 / 177 |
| `GET …/molecular-data`, 1 gene, brca_tcga_pub2015 | 260 / 178 | 470 / 335 |
| `POST /api/molecular-data/fetch`, 5 genes × 2 profiles | 859 / 223 | 457 / 436 |
| `POST …/mutations/fetch`, 3 genes, msk_impact_2017 | 677 / 313 | 337 / 338 |
| `GET /api/studies/msk_impact_2017/clinical-data` | 3,339 / 1,733 | 956 / 896 |
| `POST …/discrete-copy-number/fetch`, 6 genes, msk_impact_2017 | 261 / 182 | 245 / 220 |

www's repeats come from Redis; go has no response cache, so its first and
repeated times match.

## Parity on logged requests

Method: `test/parity/loggedgen` samples production's request log
(`cbioportal_qc.logged_requests`) and `cmd/paritydiff` replays it against
api.cbioportal.org and go (see [Testing](testing.md)); 2026-10-03, 618
requests over 114 endpoints.

| Identical | Known differences | Unexplained |
|---|---|---|
| 609 | 9 | 0 |

The known differences are www bugs, deployment fields, ids in www's session
store and tied-row order. The log holds successful requests only, which is
why the 400 gaps above did not show up here.

## Frontend e2e run

Method: the frontend's public-origin Playwright suite (598 tests), two
workers, `CI=1`, against www and go.cbioportal.org, 2026-10-02/03, go's image
of that day. Tests that passed on both (182):

| | www | go |
|---|---|---|
| Total time | 2,476 s | 2,312 s (−7%) |
| Median per-test ratio go/www | | 0.86 |
| Faster on go | | 142 of 182 |

go was faster on multi-study expression (≈ 2×) and most views, slower on the
embeddings tab and the gnomAD tooltip. Most of go's failures were
environmental: www's saved sessions and a different frontend build.

## Cost estimate

www's application tier (on-demand, us-east-1 list prices):

| Nodes | Used by | ≈ $/month |
|---|---|---|
| 2 × t4g.xlarge | public-api-green | 195 |
| ≈ 2 of 3 × r7g.xlarge | public-green (browsers) | 310 |
| ≈ ½ of 2 × r7g.large | public Redis | 80 |
| **Total** | | **≈ 590** |

go sized for www with headroom (two replicas, 1 core and 2 GiB each) fits on
one r7g.large, ≈ $80/month: **≈ $500/month saved** on the application tier,
plus Redis and the separate session service. Not included: ClickHouse
Cloud, where go currently costs 1.5–2.3× Java per request, and a load test at
www's request rate, which is still missing.

## Code size

Lines of code without blanks and comments, 2026-10-03; the shared frontend
is not counted.

| | Main code | Tests |
|---|---|---|
| **cbioportal-go** (API + importer) | **≈ 26,500** | ≈ 9,200 |
| cbioportal (Java backend) | ≈ 80,000 | ≈ 60,500 |
| cbioportal-core (Java importer + Python validator) | ≈ 33,300 | ≈ 22,700 |

go is about a quarter of the size, in one language, with three direct
dependencies (`go.mod`): ClickHouse's official driver
`github.com/ClickHouse/clickhouse-go/v2` (14 indirect modules: ch-go,
compression, decimal), a YAML parser and `golang.org/x/sync`. Not yet in go:
the study validator, SAML and a few MSK-only endpoints (≈ 5–10k lines). The
image, frontend included, is ≈ 100 MB against 1.33 GB for
`cbioportal/cbioportal:master`.
