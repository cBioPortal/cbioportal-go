# How the rewrite was done

cbioportal-go reimplements cBioPortal's Java backend (REST API) and importer
in Go, with the unmodified frontend on top. This page explains the method
used to build it and to check it against the Java system. The numbers it
produced are in [Benchmark](benchmark.md).

## Contents

- [Principle: Java is the specification](#principle-java-is-the-specification)
- [Design choices](#design-choices)
- [Verification layers](#verification-layers)
- [Production traffic](#production-traffic)
- [Running next to production](#running-next-to-production)
- [How the work was organised](#how-the-work-was-organised)
- [Lessons](#lessons)

## Principle: Java is the specification

There is no written spec of the API beyond its OpenAPI description, so the
Java source is the spec: controllers, MyBatis/ClickHouse mappers, services,
Jackson settings, Spring's validation and error handling. Every behaviour is
ported from the code, not guessed from responses: fields and their order,
row order, statistics, paging and sorting, error statuses and bodies.
Production (www.cbioportal.org, api.cbioportal.org) is the second reference
and settles questions the code leaves open, such as which tied row a page
returns. Where Java is nondeterministic or buggy, go matches what production
returns when that is stable, and the difference is documented otherwise.

## Design choices

| Area | Choice | Why |
|---|---|---|
| Importer | ELT: stream study files into a raw ClickHouse database, then build the model with SQL | One code path for small and huge studies; transforms are reviewable SQL |
| Production data | `transform --from-prod` builds go's schema from a zero-copy clone of production | Same data as www without re-importing datahub |
| API | `net/http` with Go 1.22 routing, one handler per Java endpoint | Small, dependency-free; maps one-to-one to Java's controllers |
| Database client | Official clickhouse-go driver, responses streamed row by row | Flat memory on 2–4 million-row responses |
| Statistics | Hand-ported from Java (commons-math, Datumbox) | Results must match to the last digit |
| Dependencies | Standard library first; official vendor libraries when they earn their place | Small attack surface, simple builds |

## Verification layers

Fastest first. Each layer catches what the previous one cannot.

| Layer | What it checks | Where |
|---|---|---|
| Unit tests | Statistics, binning, SQL builders, request parsing | `internal/...`, `make test` |
| Contract tests | Every endpoint's status, fields, order and errors on cBioPortal's e2e fixture studies | `test/`, `make contract`, CI on every push |
| Java oracle | The same requests against Java master on the same fixtures, built with Java's own importer; pins projections, field order and validation | `test/java_oracle.sh` |
| Importer contracts | What the import stores, rule by rule from cbioportal-core (case lists, defaults, ids) | `test/import_parity_test.go` |
| Frontend e2e | The upstream Playwright suite (~365 specs) driving the real frontend against go, with Keycloak login and upstream CI settings | `test/e2e_api.sh`, `test/run_e2e.sh` |
| Before/after replay | Byte comparison of a change against the previous build, on fixture and production corpora | refactors and performance work |
| Production traffic | Real requests replayed against go and Java (next section) | `cmd/paritydiff`, nightly job |
| Mirrored live traffic | A share of real traffic copied to go: load, latency, errors, ClickHouse cost | next-but-one section |

## Production traffic

Real API requests are the best test corpus: they cover the request shapes
clients actually send, including odd and invalid ones.

1. **Capture.** Java's request logger (`request-logging.enabled`) stored
   1.66 million requests (method, path, query, body, status) in
   `cbioportal_qc.logged_requests`, the corpus of the QC harness in
   cBioPortal/cbioportal#12238.
2. **Sample.** `test/parity/loggedgen` draws a corpus stratified by endpoint
   (every endpoint covered, frequent ones more), drops anything that writes
   (sessions, virtual study publishing, tokens, cache), and annotates the
   differences that are known and explained.
3. **Compare.** `cmd/paritydiff` sends each request to go.cbioportal.org and
   to api.cbioportal.org and compares the JSON after normalising what Java
   leaves unspecified (key order, unordered lists, float tolerance).
4. **Every night.** The nightly job replays about 5,000 requests after the
   mirror rebuild and opens or updates a GitHub issue (label `parity`) when a
   new unexplained difference appears ([Testing](testing.md)).

## Running next to production

- **Same data.** go.cbioportal.org reads a mirror of production's database,
  rebuilt within an hour of each production import (and never during one).
- **Mirrored traffic.** Traefik copies 10% of the API traffic Java serves to
  go and discards go's answers, so go sees real load without affecting users.
  Each side tags its ClickHouse queries (`log_comment`), so database cost per
  request can be compared directly.
- **Observability.** Prometheus metrics and a JSON access log per request,
  collected by Datadog, alongside Java's.

## How the work was organised

The code was written with Claude Code: one coordinating session, with
focused agents working in separate git worktrees on one task each (an
endpoint group, a performance fix, a parity investigation). A change reached
`main` only after lint, unit and contract tests passed, and changes that
touch request handling also needed the full frontend e2e suite. Agents
reported measurements (parity results, memory, ClickHouse bytes read) that
were checked before merging. Production access was read-only, except for
go's own databases and deployments.

## Lessons

- **Compare against Java, not against yourself.** Byte-identical replays
  against the previous go build missed two regressions that one run against
  production caught.
- **Ordering is the hardest part.** Many differences were row order: Java
  often has no ORDER BY and inherits ClickHouse's or the importer's order.
  go keeps the importer's order and production's internal ids for this.
- **Stream large responses.** API clients download whole profiles (millions
  of rows); building responses in memory got go's pods OOMKilled.
- **Read by the table's key.** Filters that miss the primary key (a sample
  list by id, a join against all samples) cost more than the data itself.
- **Watch the data pipeline too.** A mirror built by an old image, or copied
  while an import was running, looked like API bugs.
