# Benchmark: cbioportal-go vs the Java backend

How the comparison and the rewrite were done: [Rewrite method](rewrite-method.md).

## Summary

On the same live API requests (2026-10-06 05:00–15:20 UTC, www's non-browser
traffic mirrored 10% to go; details in [Mirrored production traffic](#mirrored-production-traffic)):

- **Latency:** go matches Java at the median (all `/api/`: 31 vs 34 ms) and
  has the lower tail (p95 296 vs 746 ms, p99 1.4 vs 6.0 s); its p99 is lower
  on every busy route.
- **ClickHouse:** per request go uses 0.75× Java's ClickHouse CPU and 0.71×
  the bytes read, without a response cache.
- **Errors:** per-route 400 rates match Java's; go had no 5xx (Java 60).
  Open: Java's 400 for paths ending in `;` (go: 404) and for some
  `POST /api/studies/{id}/clinical-data/fetch` bodies.
- **Stability:** no go restarts or OOMKills; go pods peaked at 131 MiB of
  their 2 GiB. Java's pool restarted 7 times on failed liveness probes.
- **Pods:** go uses 48 vs 1,838 MiB per pod on average and a third of Java's
  CPU per request (0.11 vs 0.34 s).
- **Cost:** the 20 Java backend pods across both AWS accounts take ≈ $1,280/month
  of nodes (≈ $1,500 with their Redis caches); go would need ≈ $160 ([estimate](#cost-estimate)). www and api,
  which go can replace today, are ≈ $500 of that.
- **Import:** go loads a study 2.6–3.6× faster than Java's importer (50k
  MSK-IMPACT: 80 vs 289 s with derived tables) at 0.2 vs 3.9 GB peak RSS
  ([Import](#import)).
- **Parity:** 613 of 618 logged requests identical, 0 unexplained; the
  nightly job's first run had 65 unexplained of 5,009, being fixed.

## Contents

- [Mirrored production traffic](#mirrored-production-traffic)
- [Latency by route](#latency-by-route)
- [Errors by status](#errors-by-status)
- [Pods: CPU, memory, restarts](#pods-cpu-memory-restarts)
- [ClickHouse load per request](#clickhouse-load-per-request)
- [Headline requests](#headline-requests)
- [Parity](#parity)
- [Frontend e2e run](#frontend-e2e-run)
- [Cost estimate](#cost-estimate)
- [Import](#import)
- [Code size](#code-size)

## Mirrored production traffic

| | Java | go |
|---|---|---|
| Deployment | `cbioportal-backend-public-api-green`, 2 pods, `v7.1.2-web-shenandoah` | `cbioportal-go-api`, 2 pods, `cbioportal-go:latest` |
| Memory request / limit | 4 / 7 GiB | 512 MiB / 2 GiB |
| Response cache | Redis, 60 min TTL | none (in-process metadata cache) |
| Database | production DB | same content, same ClickHouse service: `dev_cbioportal_public_go_green`, from 14:10 `dev_cbioportal_public_go_blue` (rebuilt with current code) |
| Requests | www's non-browser catch-all, plus api.cbioportal.org (2%) | 10% of the catch-all; responses discarded |

Traefik's `cbioportal-api-mirror-green` serves www's non-browser catch-all from
the Java pool and copies 10% to go. Browser traffic is not mirrored; at ≈ 3
requests/s on Java this compares cost per request, not behaviour under load.

| Window (UTC) | go image | Java requests | go requests |
|---|---|---|---|
| **2026-10-06 05:00–15:20** | `c41452c`…`b17328c`: all fixes live | 114,938 | 11,924 (10.6%) |

The window ends at the weekly blue/green swap (15:20), which moved the
catch-all to fresh `…-api-blue` pods. The mirror DB switch at 14:10 did not
show in the numbers, so the window is not split.

## Latency by route

Method: Java from Traefik's access log (`@OriginDuration`, requests that
reached the pool), go from its own (`duration_ms`); Datadog log
percentiles, all statuses; ms.

| Route | n Java / go | p50 Java / go | p95 Java / go | p99 Java / go |
|---|---|---|---|---|
| GET …/molecular-profiles/{id}/mutations | 13,500 / 1,377 | 28 / 36 | 121 / 141 | 675 / 296 |
| GET …/molecular-profiles/{id}/molecular-data | 9,071 / 894 | 61 / 70 | 275 / 191 | 987 / 408 |
| GET /api/studies/{id}/clinical-data | 8,409 / 777 | 116 / 39 | 858 / 314 | 6,095 / 573 |
| GET /api/studies/{id}/molecular-profiles | 6,802 / 732 | 7.5 / 16 | 86 / 38 | 1,729 / 76 |
| GET /api/genes/{id} | 6,160 / 679 | 8.0 / 7.6 | 60 / 32 | 139 / 66 |
| POST …/molecular-profiles/{id}/mutations/fetch | 4,870 / 523 | 90 / 54 | 2,850 / 529 | 13,839 / 1,653 |
| GET /api/studies/{id}/samples | 4,802 / 492 | 35 / 25 | 154 / 89 | 1,415 / 296 |
| GET /api/molecular-profiles | 3,890 / 418 | 39 / 43 | 79 / 67 | 5,091 / 107 |
| POST …/molecular-profiles/{id}/molecular-data/fetch | 3,823 / 426 | 192 / 183 | 26,245 / 12,712 | 39,945 / 22,704 |
| GET /api/studies/{id}/patients/{id}/clinical-data | 3,532 / 350 | 72 / 19 | 253 / 81 | 808 / 99 |
| GET /api/studies | 3,013 / 367 | 24 / 27 | 234 / 327 | 3,925 / 789 |
| POST …/discrete-copy-number/fetch | 2,290 / 237 | 88 / 87 | 2,154 / 659 | 7,011 / 2,187 |
| **All /api/** | 105,695 / 11,209 | **34 / 31** | **746 / 296** | **5,974 / 1,437** |

Java keeps the lower median on small repeated requests that Redis answers
(mutations and molecular-data GETs, profile lists); go has the lower median
on clinical data, samples and the fetch routes, and the lower p99 on every
busy route.

## Errors by status

| Window | 2xx Java / go | 400 Java / go | 404 Java / go | 5xx Java / go |
|---|---|---|---|---|
| 2026-10-06 | 90.7 / 91.1% | 3.5 / 2.9% | 4.8 / 5.4% | 60 (502, 500) / 0 |

Per route the 400 rates match (molecular-data GET 1.3 / 1.6%, mutations
GET 19.3 / 20.0%, mutations fetch 4.8 / 4.6%). The overall gap is paths
ending in `;` (`/api/genes/lookup/ACSL1;`, 722 requests), which Java's
firewall rejects with 400 and go answers 404, and `POST
/api/studies/{id}/clinical-data/fetch`, where Java rejected 55 of 202 and go
answered all 22 mirrored.

## Pods: CPU, memory, restarts

Method: Datadog `kubernetes.cpu.usage.total` (sum over pods),
`kubernetes.memory.usage`, `kubernetes_state.container.restarts`; go's
requests exclude probes.

| Window | CPU-s per request Java / go | Memory per pod, avg Java / go | Max pod Java / go | Restarts Java / go |
|---|---|---|---|---|
| 2026-10-06 | 0.34 / 0.11 | 1,838 / 48 MiB | 6,810 / 131 MiB | 7 / 0 |

Java's restarts were kills after failed liveness probes, not OOM. go's CPU
includes four rollouts in the window.

## ClickHouse load per request

Method: `system.query_log` by `log_comment` (`cbioportal-api`,
`cbioportal-go-api`) divided by each side's requests; Java's `SELECT 1`
connection checks excluded.

| Window | Queries Java / go | Bytes read Java / go | Query time Java / go | ClickHouse CPU Java / go |
|---|---|---|---|---|
| 2026-10-06 | 0.65 / 1.66 | 54.7 / 39.1 MB | 83 / 93 ms | 131 / 99 ms (0.75×) |

Up to 11:40 the two cost the same (CPU 0.96×, bytes 1.0×); Java's
afternoon had more heavy study-view and segment queries, which a 10% sample
catches less evenly. The single-gene molecular-data query now reads 3.7 MB
on go (57 ms CPU, 742 queries) against 21 MB on Java (48 ms, 7,682); go's
count includes its metadata-cache freshness check (≈ 0.6 per request).

## Headline requests

Method: one client outside the cluster, 2026-10-06 ≈ 17:24 UTC, go on
`f08983a`; each request three times in a row, 1 s apart. Total ms, first /
mean of the repeats:

| Request | www | go |
|---|---|---|
| `GET /api/studies` | 385 / 168 | 607 / 280 |
| `GET …/molecular-data`, 1 gene, brca_tcga_pub2015 | 465 / 207 | 472 / 348 |
| `POST /api/molecular-data/fetch`, 5 genes × 2 profiles | 908 / 382 | 738 / 491 |
| `POST …/mutations/fetch`, 3 genes, msk_impact_2017 | 739 / 464 | 444 / 345 |
| `GET /api/studies/msk_impact_2017/clinical-data` | 3,524 / 1,966 | 734 / 708 |
| `POST …/discrete-copy-number/fetch`, 6 genes, msk_impact_2017 | 294 / 214 | 530 / 310 |

go is faster on the three larger first responses, level on the
molecular-data GET and slower on the two small ones; www's repeats come from
Redis.

## Parity

Method: `cmd/paritydiff` replays logged production requests against
api.cbioportal.org and go (see [Testing](testing.md)); the logged corpus
(`test/parity/loggedgen`) against the rebuilt mirror DB, the
[nightly job](testing.md#nightly-parity-job) against go.cbioportal.org.

| Corpus, 2026-10-06 | Requests | Identical | Known differences | Unexplained |
|---|---|---|---|---|
| Logged | 618 | 613 | 5 | 0 |
| Nightly, first run | 5,009 | 4,918 | 26 | 65 |

Known differences are www bugs, deployment fields, ids in www's session
store and tied-row order; the nightly run's unexplained ones are tracked in
cBioPortal/cbioportal-go#1.

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

Java cBioPortal backends running on 2026-10-06 (blue/green: the serving color
only) and the nodes they run on, from both clusters, at on-demand us-east-1
list prices.

| Account | Portals (pods) | Memory request / limit | Node pools | ≈ $/month |
|---|---|---|---|---|
| 203403084713 | www (2), api (2), GENIE public (3) and private (3), containerized, master, public beta (1 each) | 78 / 142 GiB | 3 × r7g.xlarge, 3 × r7g.large, 2 × t4g.xlarge | 900 |
| 666628074417 | MSK (2), MSK beta, private, SCLC, triage, staging (1 each) | 32 / 48 GiB | 3 × r7g.large; 3 × t4g.large (SCLC, triage, dev) | 380 |
| **Total** | 20 pods | 110 / 190 GiB | | **≈ 1,280** |

Redis response caches add 2 × r7g.large (≈ $155) and 2 × t3.medium (≈ $60).

go serves the same traffic at 48 MiB per pod on average (131 MiB peak, 2 GiB
limit), so two replicas per portal for all of them fit on two r7g.large,
≈ $160/month: **≈ $1,100/month (≈ $13,000/year) less** on the application
tier, ≈ $1,300/month (≈ $15,000/year) with the Redis caches. Not included: ClickHouse Cloud, where go
costs 0.75–1.0× Java's CPU per request, and a load test at www's request rate.

Most of these portals need features go does not have yet: SAML login (GENIE
private, MSK, private), `authorization=true` with users from the database and
`dat.method=uuid` tokens (GENIE public). What go can replace today is www and
api, about $500/month of the total.

## Import

Method: `scripts/import-benchmark.sh`, 2026-10-08, one local ClickHouse 25.3;
the studies as on datahub master (18763f8), LFS files from datahub's own
store (`fetch`); each run into empty databases with genes, cancer types and
gene panels preloaded; Java is cbioportal-core from
`cbioportal/cbioportal:v7.1.2` (`cbioportalImporter.py`, `-Xmx6g`, container
capped at 8 GiB) then `populate_derived_tables.sql`, go is `ingest` then
`transform` (4 GiB cap); peak RSS is GNU time's, the largest process. Two
runs each, both shown.

| Study (rows, MB) | Importer | Import time | Derived/transform time | Peak RSS |
|---|---|---|---|---|
| msk_chord_2024 (21.7M, 346) | Java | 125 / 123 s | 28 / 26 s | 3.2 / 2.8 GB |
| | go | 17 / 21 s | 34 / 44 s | 35 / 37 MB |
| msk_impact_50k_2026 (49.2M, 589) | Java | 281 / 256 s | 45 / 54 s | 3.9 / 3.7 GB |
| | go | 39 / 39 s | 58 / 50 s | 213 MB |

go's importer streams each file into ClickHouse in batches, as is, and builds
the model and the study-view `*_derived` tables as SQL inside ClickHouse, so
its own memory stays small; Java parses every record in the JVM, one JVM per
file. Java's validator, which go does not have, adds 56–71 s (0.6 GB) and
132–141 s (1.2 GB) and is not counted. Both produced the same samples,
patients, clinical, mutation, structural-variant, alteration and
generic-assay rows; Java's `clinical_data_derived` also keeps a row for each
empty value, and Java skips segments whose start is not below their end (4
on chord, 10 on 50k), which go keeps. On
ClickHouse Cloud's replicated tables go now adds a file's new staging columns
in one `ALTER` per table instead of one per column (07a5caa).

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
