# How the rewrite was done

cbioportal-go rewrites cBioPortal's Java backend (the REST API) and its
importer in Go. The cBioPortal website runs on top without changes. This
page explains how the rewrite was built, and how we check that it behaves
the same as the Java version. The measured results are in
[Benchmark](benchmark.md).

## Contents

- [Principle: Java is the specification](#principle-java-is-the-specification)
- [Design choices](#design-choices)
- [Verification layers](#verification-layers)
- [Production traffic](#production-traffic)
- [Running next to production](#running-next-to-production)
- [How the work was organised](#how-the-work-was-organised)
- [Lessons](#lessons)

## Principle: Java is the specification

A specification ("spec") is a written description of exactly how a program
must behave. The cBioPortal API has no full spec. There is only its OpenAPI
description, which lists the endpoints and fields but leaves out many
details. So the Java source code serves as the spec.

Every behavior in go is copied from reading the Java code, not guessed from
its answers. That includes:

- which fields an answer has, and in what order,
- the order of rows,
- statistics,
- paging and sorting, and
- error codes and error messages.

The production site (www.cbioportal.org and api.cbioportal.org) is the
second reference. It settles questions the code leaves open. For example,
when two rows tie in a sort, production shows which one comes first.

Where Java is plainly wrong, go fixes it instead of copying it:

- Java answers a valid request with an error (often a 500). go answers with
  the data.
- Java answers with wrong data, such as another study's rows or a wrong
  count. go answers with the right data.
- Java accepts a paging or sorting parameter and then ignores it. go
  follows it.

Row order only has to match when the request asks for a sort. Without one,
go may list rows in any order that stays the same from one request to the
next. When Java gives a different answer each time, go cannot match it.
[Java quirks](java-quirks.md) lists every fix and every such difference.

## Design choices

| Area | Choice | Why |
|---|---|---|
| Importer | Copy study files into the database first, then build the tables with SQL (a pattern called ELT: extract, load, transform). | One code path works for small and huge studies, and the SQL is easy to review. |
| Production data | `transform --from-prod` builds go's tables from a copy of production's database. ClickHouse can make this copy without duplicating the data on disk. | go gets the same data as www without re-importing every study. |
| API | Go's standard `net/http` server, with one handler per Java endpoint. | It is small and needs no extra libraries. Each handler matches one Java endpoint, so the two are easy to compare. |
| Database client | ClickHouse's official Go driver. Answers are sent row by row as they are read. | Memory stays flat even for answers with 2 to 4 million rows. |
| Statistics | Copied by hand from the Java libraries commons-math and Datumbox. | Results must match Java down to the last digit. |
| Dependencies | The standard library first. Official, well-maintained libraries, and a few small and widely used ones, only when they are worth it. Seven in all, listed in [Architecture](../architecture.md#dependencies). | Fewer outside packages means fewer security risks and simpler builds. |

## Verification layers

We check the rewrite in several layers, from fastest to slowest. Each layer
catches problems the earlier ones cannot.

| Layer | What it checks | Where |
|---|---|---|
| Unit tests | Small pieces of code: statistics, chart binning, SQL builders and request parsing. | `make test` |
| Contract tests | Every endpoint's status code, fields, order and errors. They run on cBioPortal's small test studies (its "fixture" studies). | `test/`, `make contract`, CI on every push |
| Java oracle | The same requests sent to the latest Java version, loaded with the same test studies by Java's own importer. This settles which fields appear, their order, and how bad input is rejected. | `test/java_oracle.sh` |
| Importer contracts | What the importer stores, checked rule by rule against cbioportal-core (case lists, default values, ids). | `test/import_parity_test.go` |
| Website e2e tests | cBioPortal's own Playwright test suite (about 90 test files). It drives the real website in a browser against go, logs in through Keycloak, and uses the same settings as cBioPortal's CI. | `test/e2e_api.sh`, `test/run_e2e.sh` |
| Before/after replay | The same requests sent to a changed build and to the previous build, and the answers compared byte by byte. Used on test data and on production requests. | Refactoring and speed work |
| Production traffic | Real requests sent to both go and Java, and the answers compared. See [the next section](#production-traffic). | `rewrite/java-v7-to-go/paritydiff`, nightly job |
| Mirrored live traffic | A share of real traffic is copied to go to measure load, speed, errors and database cost. See [Running next to production](#running-next-to-production). | go.cbioportal.org |

## Production traffic

Real API requests make the best test set. They cover the kinds of requests
people actually send, including strange and invalid ones. We use them in
four steps:

1. **Record.** The Java portal can log each request it gets (the setting
   `request-logging.enabled`). It recorded 1.66 million requests, with
   their method, path, query, body and status. They are stored in the
   `cbioportal_qc.logged_requests` table. This is the same data used by the
   quality-check tool in cBioPortal/cbioportal#12238.
2. **Pick a sample.** `rewrite/java-v7-to-go/loggedgen` picks a set of requests that
   covers every endpoint, with more requests for the busy ones. It leaves
   out anything that changes data, such as saving sessions, publishing
   virtual studies, tokens or clearing the cache. It also marks the
   differences we already know about and have explained.
3. **Compare.** `rewrite/java-v7-to-go/paritydiff` sends each request to go.cbioportal.org and
   to api.cbioportal.org. It then compares the two JSON answers. First it
   evens out things Java does not promise to keep the same, such as the
   order of keys, the order of unsorted lists, and tiny differences in
   decimal numbers.
4. **Repeat every night.** A nightly job replays about 5,000 requests
   against go.cbioportal.org, which reads a copy of production's data. When it finds a new
   difference that has no explanation yet, it opens or updates a GitHub
   issue with the label `parity`. ("Parity" means go and Java give the same
   answer.) See [Parity with Java](parity.md).

## Running next to production

go.cbioportal.org runs beside the real site, so we can see how it behaves
under real use.

- **Same data.** go.cbioportal.org reads a copy (a "mirror") of
  production's database. The mirror is rebuilt within an hour after each
  import on production, and never while an import is still running.
- **Copied traffic.** Traefik, the program that routes web traffic to the
  servers, copies 10% of the API requests that Java gets and sends them to
  go too. go's answers are thrown away, so users never see them. This puts
  real load on go without any risk to users. Both sides label their
  database queries (with ClickHouse's `log_comment` setting). That lets us
  compare the database cost of each request directly.
- **Monitoring.** go reports Prometheus metrics and writes one JSON log line
  per request. Datadog collects both, next to Java's.

## How the work was organised

The code was written with Claude Code, an AI coding assistant. One main
session coordinated the work. It handed single tasks to helper agents, each
working in its own copy of the repository (a git worktree). A task was, for
example, one group of endpoints, one speed fix, or one difference to track
down.

A change went into `main` only after the format checks, unit tests and
contract tests passed. Changes to how requests are handled also had to pass
the full website e2e suite. Agents reported their measurements, such as
parity results, memory use and how much data ClickHouse read. Those numbers
were checked before a change was merged. Access to production was
read-only, except for go's own databases and deployments.

## Lessons

- **Compare against Java, not against yourself.** Comparing a new go build
  with the previous go build missed two bugs. A single comparison against
  production caught them.
- **Row order is the hardest part.** Many differences came down to the
  order of rows. Java often does not sort its results. It just returns them
  in whatever order ClickHouse or the importer left them. So go keeps the
  importer's order and production's internal ids.
- **Send large answers row by row.** Some API users download whole data
  profiles with millions of rows. Building those answers fully in memory
  used up so much memory that Kubernetes killed go's servers.
- **Read tables by their key.** Every ClickHouse table is sorted by a key
  (its "primary key"). A filter that does not use that key forces ClickHouse
  to scan much more data. For example, looking up a sample list by its id,
  or joining against all samples, could cost more than the data itself.
- **Check the data pipeline too.** Some problems that looked like API bugs
  were really data problems. For example, a mirror built by an old version
  of the program, or one copied while an import was still running, gave
  wrong answers.
