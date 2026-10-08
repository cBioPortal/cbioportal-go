# Benchmark: cbioportal-go vs the Java backend

This page compares cbioportal-go with cBioPortal's Java backend and Java
importer. It answers four questions: is go as fast, does it give the same
answers, what does it cost to run, and how fast does it load a study. How the
rewrite and the comparison were done is in [Rewrite method](method.md).

Both backends read their data from ClickHouse, a database built for fast
counting and filtering over very large tables. A few terms used below:

- **p50, p95, p99**: the response time that 50%, 95% or 99% of requests beat.
  p50 is the typical request (the median). p99 shows the slow tail.
- **Pod**: one running copy of a server in Kubernetes, the system that runs
  cBioPortal's servers.
- **Mirror**: Traefik, the router in front of www.cbioportal.org, sends a copy
  of some real requests to go. Java answers the user. go's answers are thrown
  away, but its speed and load are measured.
- **Parity**: whether go's response is identical to Java's for the same
  request.

## Summary

These numbers come from real API traffic on 2026-10-06, 05:00–15:20 UTC.
10% of www's non-browser API requests were mirrored to go (see
[Mirrored production traffic](#mirrored-production-traffic)).

- **Speed:** go is as fast as Java for a typical request (31 vs 34 ms) and
  much faster on slow ones (p95 296 vs 746 ms, p99 1.4 vs 6.0 s). Its p99 is
  lower on every busy route.
- **Database load:** over three days, go used 1.47 times Java's ClickHouse
  CPU per request on www's traffic and 0.92 times on api's. Java answers many
  repeat requests from its Redis cache, and go has no response cache yet.
  Most of go's extra cost came from one slow query, which is now fixed
  ([details](#clickhouse-load-per-request)).
- **Errors:** go returns "bad request" (HTTP 400) as often as Java on each
  route. go had no server errors (5xx); Java had 60. Two cases still differ:
  paths ending in `;` (Java 400, go 404) and some
  `POST /api/studies/{id}/clinical-data/fetch` bodies.
- **Stability:** go never restarted or ran out of memory. Its pods peaked at
  131 MiB of the 2 GiB they are allowed. Java's pods restarted 7 times
  because they stopped answering health checks.
- **Resources:** a go pod uses 48 MiB of memory on average, a Java pod
  1,838 MiB ([why](#why-go-uses-less-memory)). go uses a third of Java's
  CPU per request (0.11 vs 0.34 s).
- **Cost:** the 20 Java backend pods cost about $1,280 a month in servers, or
  about $1,500 with their Redis caches. go would need about $160
  ([details](#cost-estimate)). www and api, the portals go can replace today,
  account for about $500 of that.
- **Import:** go loads a study 2.6 to 3.6 times faster than Java's importer.
  The 50,000-sample MSK-IMPACT study takes 80 s instead of 289 s, with 0.2 GB
  of memory instead of 3.9 GB ([Import](#import)).
- **Same answers:** 613 of 618 logged production requests gave identical
  responses, and the other 5 differ for known reasons. The nightly check's
  first run found 65 unexplained differences out of 5,009 requests. These are
  tracked in cBioPortal/cbioportal-go#1.

## Contents

- [Mirrored production traffic](#mirrored-production-traffic)
- [Latency by route](#latency-by-route)
- [Errors by status](#errors-by-status)
- [Pods: CPU, memory, restarts](#pods-cpu-memory-restarts)
- [Why go uses less memory](#why-go-uses-less-memory)
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
| Response cache | Redis, kept 60 min | none (only study metadata is cached in memory) |
| Database | production database | the same data on the same ClickHouse service (`dev_cbioportal_public_go_green`, from 14:10 `dev_cbioportal_public_go_blue`, rebuilt with current code) |
| Requests | www's non-browser requests, plus 2% of api.cbioportal.org | 10% of www's non-browser requests; responses thrown away |

Browser traffic is not mirrored. Java handled about 3 requests a second, so
this compares the cost of each request, not how the servers behave under heavy
load.

| Window (UTC) | Java requests | go requests |
|---|---|---|
| **2026-10-06 05:00–15:20** | 114,938 | 11,924 (10.6%) |

The window ends at the weekly switch between the blue and green Java pods,
which moved traffic to new pods. Switching go's database at 14:10 made no
visible difference, so the window is not split.

## Latency by route

Java's times come from Traefik's access log and only count requests that
reached Java. go's times come from its own log. The percentiles were computed
in Datadog, a monitoring service, over all response codes. Times are in ms.

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

Java has the faster median on small requests that repeat often, because its
Redis cache answers them. Examples are the mutations and molecular-data GETs
and the profile lists. go has the faster median on clinical data, samples and
the fetch routes. go has the faster p99 on every busy route.

## Errors by status

| Window | 2xx Java / go | 400 Java / go | 404 Java / go | 5xx Java / go |
|---|---|---|---|---|
| 2026-10-06 | 90.7 / 91.1% | 3.5 / 2.9% | 4.8 / 5.4% | 60 (502, 500) / 0 |

On each route the share of 400 errors is about the same. For example, the
molecular-data GET is 1.3% on Java and 1.6% on go, and the mutations GET is
19.3% and 20.0%. The overall numbers differ for two reasons:

- Paths ending in `;`, such as `/api/genes/lookup/ACSL1;` (722 requests).
  Java's firewall rejects them with 400. go answers 404.
- `POST /api/studies/{id}/clinical-data/fetch`. Java rejected 55 of 202. go
  answered all 22 that were mirrored to it.

## Pods: CPU, memory, restarts

The numbers come from Datadog's Kubernetes metrics. go's request count leaves
out health checks.

| Window | CPU-s per request Java / go | Memory per pod, avg Java / go | Max pod Java / go | Restarts Java / go |
|---|---|---|---|---|
| 2026-10-06 | 0.34 / 0.11 | 1,838 / 48 MiB | 6,810 / 131 MiB | 7 / 0 |

Java's restarts happened because its pods stopped answering Kubernetes'
health checks, not because they ran out of memory. go's CPU includes four
deployments of new versions during the window.

## Why go uses less memory

A Java API pod used about 1.8 GB of memory on average and a go pod about
50 MB. The importers differ just as much: 3–4 GB for Java and 35–215 MB for
go. Four things explain most of the gap.

**1. Java reserves memory ahead of time.** A Java program runs inside the
Java virtual machine (JVM), which manages memory for it. The production API
pods start the JVM with a maximum of 6 GB (`-Xmx6g`). The JVM lets its memory
grow toward that maximum before it cleans up thoroughly, and it gives freed
memory back to the system slowly. So a Java pod's memory shows how much it is
*allowed* to use more than how much it *needs*. go's runtime cleans up a
little at a time while the program runs, so its memory stays close to what
it is actually using.

**2. Java brings a lot of machinery along.** The Java backend is built on
Spring Boot. Before it answers its first request, it loads thousands of
classes from many libraries, plus the JVM's own compiler, which turns hot
code into machine code while the program runs. That takes hundreds of
megabytes. go's API is one program that was compiled to machine code ahead
of time. The whole program is a single file of about 25 MB.

**3. go sends large answers row by row.** When a client asks for a large
answer, for example a whole data profile with millions of values, Java first
reads all of it from the database into a list in memory, then turns the list
into JSON and sends it. Its memory grows with the size of the answer. go
writes each row to the client as soon as it arrives from ClickHouse, reusing
the same small buffers, so its memory stays about the same whether the answer
has ten rows or ten million. (Before go worked this way, the biggest
downloads made its pods run out of memory too.)

**4. The go importer lets the database do the work.** Java's importer reads
every record into Java objects, checks and converts it, and keeps lookup
tables in memory, such as every gene. It also starts a new JVM for each
file. go's importer copies each file into ClickHouse almost unchanged, in
batches, and then builds the final tables with SQL that runs *inside*
ClickHouse. ClickHouse uses memory for that work, but it is the database
server both importers already use, and its memory is not counted in either
importer's number above.

Two cautions when reading these numbers:

- go received about a tenth of Java's requests (the mirrored share), at
  about 3 requests a second in total. At that rate both servers are mostly
  idle, so the gap mostly shows the cost of simply running each one. go
  has not yet been tested at www's full request rate.
- Java's memory could be lowered by giving the JVM a smaller maximum, at the
  risk of more frequent cleanups and out-of-memory errors on large answers.
  go needs no such setting to stay small.

## ClickHouse load per request

ClickHouse logs every query along with a tag that says which server sent it.
The totals for each server were divided by its number of requests. Java's
`SELECT 1` connection checks are left out.

| Window | Traffic | ClickHouse CPU per request, Java / go |
|---|---|---|
| 2026-10-06, 10 hours | www and api together | 131 / 99 ms (0.75×) |
| 2026-10-06 to 10-08, 3 days | www | 1.47× |
| 2026-10-06 to 10-08, 3 days | api | 0.92× |

The 3-day numbers are the better guide: a 10% sample of a 10-hour window
catches the rare heavy requests unevenly. Two things made go cost more on
www's traffic:

- **No response cache.** Java's Redis cache answers 65–75% of its lookups
  without asking ClickHouse. go asks ClickHouse every time.
- **One slow query.** Counting a pan-cancer molecular-data request
  (`projection=META`) read 9.8 GiB and took 32 s of CPU, because the protein
  part of the query could not use the table's index. It now reads 127 MiB
  and takes 3.5 s of CPU. It was go's most expensive query.

go also sends more queries per request than Java (1.66 vs 0.65 on
2026-10-06), because about 0.6 per request check whether its metadata cache
is still fresh.

One example: the query for a single gene's molecular data reads 3.7 MB on go
(57 ms CPU) and 21 MB on Java (48 ms CPU).

## Headline requests

One client outside the cluster sent each request three times, one second
apart, on 2026-10-06 around 17:24 UTC. The table shows total time in ms for
the first request, then the average of the two repeats.

| Request | www | go |
|---|---|---|
| `GET /api/studies` | 385 / 168 | 607 / 280 |
| `GET …/molecular-data`, 1 gene, brca_tcga_pub2015 | 465 / 207 | 472 / 348 |
| `POST /api/molecular-data/fetch`, 5 genes × 2 profiles | 908 / 382 | 738 / 491 |
| `POST …/mutations/fetch`, 3 genes, msk_impact_2017 | 739 / 464 | 444 / 345 |
| `GET /api/studies/msk_impact_2017/clinical-data` | 3,524 / 1,966 | 734 / 708 |
| `POST …/discrete-copy-number/fetch`, 6 genes, msk_impact_2017 | 294 / 214 | 530 / 310 |

go is faster on the three largest first responses. It is about the same on
the molecular-data GET and slower on the two small requests. www's repeats are
fast because Redis answers them.

## Parity

The `paritydiff` tool sends the same logged production requests to
api.cbioportal.org and to go and compares the answers (see
[Parity with Java](parity.md)). The "logged" set ran against go on a fresh copy of
production's data. The [nightly job](parity.md#nightly-parity-job) runs
against go.cbioportal.org.

| Request set, 2026-10-06 | Requests | Identical | Known differences | Unexplained |
|---|---|---|---|---|
| Logged | 618 | 613 | 5 | 0 |
| Nightly, first run | 5,009 | 4,918 | 26 | 65 |

Known differences have a reason. Examples are bugs in www, fields that
describe the server itself, saved sessions that exist only on www, and rows
that tie in sort order. The nightly run's unexplained differences are tracked
in cBioPortal/cbioportal-go#1.

## Frontend e2e run

The frontend's end-to-end (e2e) tests click through the real web pages in a
browser. This run used the 598 tests written for public portals, with two
workers and `CI=1`, against www and go.cbioportal.org on 2026-10-02 and 03.
The table covers the 182 tests that passed on both.

| | www | go |
|---|---|---|
| Total time | 2,476 s | 2,312 s (−7%) |
| Median per-test ratio go/www | | 0.86 |
| Faster on go | | 142 of 182 |

go was about twice as fast on multi-study expression and faster on most
views. It was slower on the embeddings tab and the gnomAD tooltip. Most tests
that failed on go failed for setup reasons: they needed www's saved sessions,
or go served a different frontend build.

## Cost estimate

This lists the Java cBioPortal backends running on 2026-10-06 in both AWS
accounts, and the servers ("nodes") they run on. For portals with blue and
green copies, only the copy serving traffic is counted. Prices are AWS
on-demand list prices in us-east-1.

| Account | Portals (pods) | Memory request / limit | Node pools | ≈ $/month |
|---|---|---|---|---|
| 203403084713 | www (2), api (2), GENIE public (3) and private (3), containerized, master, public beta (1 each) | 78 / 142 GiB | 3 × r7g.xlarge, 3 × r7g.large, 2 × t4g.xlarge | 900 |
| 666628074417 | MSK (2), MSK beta, private, SCLC, triage, staging (1 each) | 32 / 48 GiB | 3 × r7g.large; 3 × t4g.large (SCLC, triage, dev) | 380 |
| **Total** | 20 pods | 110 / 190 GiB | | **≈ 1,280** |

The Redis response caches add 2 × r7g.large (about $155) and 2 × t3.medium
(about $60).

go used 48 MiB per pod on average (131 MiB at most) on a tenth of the same
traffic. Memory hardly grows with the request rate (see
[Why go uses less memory](#why-go-uses-less-memory)), but go has not been
load-tested at full rate.
Two copies of every portal would fit on two r7g.large nodes, about $160 a
month. That saves about $1,100 a month ($13,000 a year) on the servers, or
about $1,300 a month ($15,000 a year) counting the Redis caches. This does not
include ClickHouse Cloud, where go used 0.92 to 1.47 times Java's CPU per
request. go has also not been load-tested at www's full request rate.

Most of these portals need features go does not have yet:

- SAML login (GENIE private, MSK, private).
- Per-user study access with users stored in the database, and data access
  tokens of type `uuid` (GENIE public).

The portals go can replace today are www and api, about $500 a month of the
total.

## Import

The import benchmark loads two large studies with each importer and times
it. RSS ("resident set size") is the memory a process actually uses; the
table shows the peak of the largest process.

How it was run: `scripts/import-benchmark.sh` on 2026-10-08, against one
local ClickHouse 25.3, with the studies as they are on datahub's master
branch. Each run started from empty databases with genes, cancer types and
gene panels already loaded.

- **Java:** cbioportal-core from `cbioportal/cbioportal:v7.1.2`
  (`cbioportalImporter.py`, `-Xmx6g`, container limited to 8 GiB), then
  `populate_derived_tables.sql`.
- **go:** `ingest`, then `transform` (container limited to 4 GiB).

Each was run twice, and both results are shown.

| Study (rows, MB) | Importer | Import time | Derived/transform time | Peak RSS |
|---|---|---|---|---|
| msk_chord_2024 (21.7M, 346) | Java | 125 / 123 s | 28 / 26 s | 3.2 / 2.8 GB |
| | go | 17 / 21 s | 34 / 44 s | 35 / 37 MB |
| msk_impact_50k_2026 (49.2M, 589) | Java | 281 / 256 s | 45 / 54 s | 3.9 / 3.7 GB |
| | go | 39 / 39 s | 58 / 50 s | 213 MB |

go uses little memory because it copies each file into ClickHouse in batches
without changing it. It then builds the tables the API reads with SQL inside
ClickHouse. Java reads every record into its own memory and starts a new Java
process for each file.

Java also runs a validator, which go does not have. It is not counted above.
It adds 56–71 s and 0.6 GB on msk_chord_2024, and 132–141 s and 1.2 GB on
msk_impact_50k_2026.

Both importers produced the same samples, patients, clinical data, mutations,
structural variants, alterations and generic-assay rows. Two small
differences remain:

- Java's `clinical_data_derived` table also keeps a row for each empty value.
- Java skips copy-number segments whose start is not before their end (4 on
  chord, 10 on 50k). go keeps them.

## Code size

Lines of code, not counting blank lines and comments, on 2026-10-03. The
frontend, which both use, is not counted.

| | Main code | Tests |
|---|---|---|
| **cbioportal-go** (API + importer) | **≈ 26,500** | ≈ 9,200 |
| cbioportal (Java backend) | ≈ 80,000 | ≈ 60,500 |
| cbioportal-core (Java importer + Python validator) | ≈ 33,300 | ≈ 22,700 |

go is about a quarter of the size and uses one language. It has three direct
dependencies: ClickHouse's official Go driver, a YAML parser and
`golang.org/x/sync`. go does not yet have the study validator, SAML login and
a few MSK-only endpoints, which would add roughly 5,000–10,000 lines. The
container image, frontend included, is about 100 MB. The Java image
`cbioportal/cbioportal:master` is 1.33 GB.
