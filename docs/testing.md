# Testing

This page explains how cbioportal-go is tested and how to run each kind of
test. There are three kinds of local tests, from fastest to slowest. There are
also tools that compare go's answers with those of the Java backend it
replaces, both on production (www.cbioportal.org) and on a local copy of Java.

## Contents

- [Test tiers](#test-tiers)
- [Unit tests](#unit-tests)
- [Transform golden tests](#transform-golden-tests)
- [API contract tests](#api-contract-tests)
- [Frontend e2e tests](#frontend-e2e-tests)
- [CI](#ci)
- [API parity with www.cbioportal.org](#api-parity-with-wwwcbioportalorg)
- [Logged production requests](#logged-production-requests)
- [Nightly parity job](#nightly-parity-job)
- [Java oracle](#java-oracle)

## Test tiers

| Tier | What it checks | Command | Needs | Time |
|---|---|---|---|---|
| Unit | Single functions and packages | `go test ./internal/...` | nothing (some tests also use ClickHouse) | seconds |
| Contract | Each API endpoint gives exactly the answer the frontend expects | `make contract` | a running API on the test databases | ~5 s |
| Frontend e2e | The real cBioPortal web pages work, clicked through in a browser | `test/run_e2e.sh` | the API, the built frontend, Docker | 15–20 min for all |

ClickHouse is the database go reads from. The test databases come from
cBioPortal's own test studies, loaded by `test/setup_test_db.sh` (see
[API contract tests](#api-contract-tests)).

## Unit tests

`go test ./internal/...` runs without any other service. Among other things,
it checks go's copy of Java's data binning (how chart values are grouped into
bars) against about 1,800 cases recorded from Java.

Set `CLICKHOUSE_TEST_URL=http://localhost:18123` to also run the tests that
need a live ClickHouse. These check that:

- query parameters reach ClickHouse correctly, including escaping;
- both ways of reading results from ClickHouse return the same values for
  every column type;
- a small study can be loaded, transformed and exported. Genes are read from
  `cbioportal_ref`, or from the database named in `CLICKHOUSE_TEST_REF_DB`;
- the transform golden tests below pass.

## Transform golden tests

The transform step builds the tables the API reads. A golden test runs it on
small sample studies and compares every table and view it builds with a saved
copy of the expected result, called a dump.

```bash
CLICKHOUSE_TEST_URL=http://localhost:18123 go test -run Golden ./internal/transform
```

This takes about 10 s. It creates temporary databases named
`cbioportal_go_test_tx_<pid>_*` and drops them afterwards. Inputs and dumps
are in `internal/transform/testdata/golden/`.

| Test | Input | Dump |
|---|---|---|
| `TestLiveGoldenStaged` | `studies/golden_a`, which has every file type (clinical, MAF, discrete CNA wide and long, log2, mRNA, methylation, segments, SV, gene panels, timeline, generic assay, resources, GISTIC, MutSig, case lists, tags, namespaces), and `studies/golden_b`. Genes come from `reference/seed.sql` (11 genes). | `expected/staged` |
| `TestLiveGoldenSchemaEvolution` | `evolution/v1`, loaded again with `v2`'s files, which add columns (MAF drivers and namespace, clinical and resource columns, `LICENSE`). The result must equal a fresh load of v2. | `expected/evolution` (the objects that read those columns) |
| `TestLiveGoldenFromProd` | `prod/`: the Java importer's schema with a slice of the Java test database `java_e2e` (7 patients of 3 studies, about 20 genes) | `expected/fromprod/{portal,raw}` |

A dump has one TSV file per table or view: the column names, the column
types, then the rows in sorted order. `_objects.tsv` lists the kind of each
object. When you change the transform on purpose, write new dumps and review
the diff:

```bash
CLICKHOUSE_TEST_URL=http://localhost:18123 go test -run Golden ./internal/transform -update
```

To cut a new `prod/` slice from the Java test database, add
`-extract-prod java_e2e` to a `-run GoldenFromProd` run. It only reads that
database. `test/java_oracle.sh db` builds it (see [Java oracle](#java-oracle)).

## API contract tests

A contract test sends a request to a running API and checks the exact
answer. These tests cover the behaviour the frontend and the e2e tests rely
on: study-view filters, chart counts and bins, sessions, gene tables,
enrichments, the fields Java returns, and Java's error responses. When you
fix how an endpoint behaves, add a case.

The tests run against `API=http://host:port` (default
`http://localhost:18090`) and are skipped when nothing answers there. The API
must serve the test databases. These are built from cBioPortal's e2e test
studies and gene sets:

```bash
git clone https://github.com/cBioPortal/cbioportal-test
test/setup_test_db.sh cbioportal-test seed-cbioportal_hg19_hg38_v2.14.5.sql.gz
KEYCLOAK=false test/e2e_api.sh     # the API on :18090, no login
make contract                      # go test ./test/
```

`setup_test_db.sh` also loads the gene sets from `data/genesets`. On an older
reference database, `cbioportal-import load-genesets` adds them.

## Frontend e2e tests

End-to-end (e2e) tests drive the real cBioPortal web pages in a browser using
Playwright, a browser automation tool. `test/run_e2e.sh [WORKERS] [spec...]`
runs cBioPortal's Playwright tests (`frontend/end-to-end-test-playwright`)
against the go API, which serves the web pages itself, as a cBioPortal server
does.

`frontend/` is the cbioportal-frontend repository, included as a git
submodule. This ties each version of go to the exact frontend and tests it
was checked with. Build the frontend once, and again after updating the
submodule, the same way upstream CI does:

```bash
git submodule update --init
(cd frontend && pnpm install --frozen-lockfile && pnpm run buildAll)
(cd frontend/end-to-end-test-playwright && pnpm install --frozen-lockfile --ignore-workspace)
test/e2e_api.sh                    # Keycloak + the API on :18090
test/run_e2e.sh 3 tests/local/annotation-filter-menu.spec.ts
```

The production build loads faster than the frontend's dev server and uses
less memory, which leaves room for more test workers. To test against a dev
server instead, set `FRONTEND_URL=http://localhost:3000`.

Run single test files while you work. Run the full suite only at milestones.

The runner uses the same settings as cBioPortal's own CI:

- `CI=1`: a failed test is retried once. A test that passes on retry is
  reported as flaky.
- `LOCALDEV=1`, and the 120 s test timeout from the Playwright config.
- 3 workers locally, which is the most a 4-core machine shared with
  ClickHouse can handle. The GitHub workflow uses 2, because its runner has
  2 CPUs and 3 workers make tests miss their 5 s waits there. The
  study-view download-controls test, which loads IGV, can still be slow on
  that runner.

`go run ./cmd/e2ereport --fails` summarizes the failures of the last run.

### Login with Keycloak

Keycloak is an open-source login server. As in upstream CI, the tests log in
through it. `test/e2e_api.sh` starts Keycloak 16.1.1 on port 8084 with the
realm `cbio` and the user `testuser` / `P@ssword1`, who has access to every
test study. It then runs the API with `authenticate=oauth2`. Docker must be
available. With `KEYCLOAK=false` the API runs without login.

Two more login features have their own tests against this Keycloak. Each is
skipped unless its variable is set.

**Data access tokens** (`dat.method=oauth2`). The test logs in as `datuser`,
who can see one study, downloads a token and calls the API with it:

```bash
DAT=true PORT=18096 test/e2e_api.sh
DAT_KEYCLOAK=1 API=http://localhost:18096 go test -run DATKeycloak ./test/
```

**Optional sign-in** (`authenticate=optional_oauth2`). The test browses
without logging in, signs in as `testuser` through `/login`, checks that the
same studies are visible, saves a virtual study and logs out. With `REDIS`
set, the API keeps login sessions in Redis, a small key-value database:

```bash
docker run -d --name cbioportal-go-redis -p 127.0.0.1:16379:6379 redis:7-alpine
OPTIONAL=true REDIS=localhost:16379 PORT=18096 test/e2e_api.sh
OPTIONAL_KEYCLOAK=1 API=http://localhost:18096 go test -run OptionalOAuth2Keycloak ./test/
```

The Redis session store has its own live test:
`REDIS_TEST_ADDR=… go test -run RedisLoginStoreLive ./internal/api`.

## CI

On every push and pull request, GitHub Actions (`.github/workflows/ci.yml`)
checks formatting with gofmt and vet, runs the unit tests, and runs the
contract tests against a ClickHouse loaded with the test studies. The full
frontend e2e suite (`.github/workflows/e2e.yml`) runs once a week and when
started by hand.

## API parity with www.cbioportal.org

"Parity" means go gives the same answer as Java. `cmd/paritydiff` sends a
list of API requests to two servers and compares the responses.

- The order of keys in JSON does not matter.
- Numbers match if they differ by less than one part in a billion (1e-9).
- Options on each request can sort arrays whose order Java does not define
  (`unordered`, `unordered=key`, `ties=field`), drop keys (`ignore=`), or
  mark a difference as expected with its reason (`known="..."`).

Responses from the reference server are cached in `.parity/cache`, which git
ignores. They are fetched at most twice a second with a browser User-Agent,
so running the check again does not load www.

`test/parity/corpus.txt` holds about 275 requests the frontend makes. They
cover studies, samples, clinical data, profiles, mutations, copy number,
structural variants, molecular and generic assay data, study view, results
view, group comparison and patient view, on four studies
(acc_tcga_pan_can_atlas_2018, msk_impact_2017, acbc_mskcc_2015 and
lgg_ucsf_2014). Run it against an API serving a copy of production's data
(see [Deployment](deployment.md#mirroring-production)):

```bash
go run ./cmd/paritydiff -a http://localhost:18091 -b https://www.cbioportal.org \
  -corpus test/parity/corpus.txt -q -dump .parity/dump
```

| Flag | Effect |
|---|---|
| `-q` | list only requests whose answers differ |
| `-run REGEXP` | run only matching requests |
| `-dump DIR` | save both responses of each difference |
| `-refresh` | fetch www's responses again instead of using the cache |

The command fails (non-zero exit status) when a request differs without a
`known` reason.

On 2026-10-03, 257 of 275 requests were identical and the other 18 were known
differences. These are listed in
[Deployment](deployment.md#mirroring-production).

### Logged production requests

Production logs the API requests it receives in the ClickHouse table
`cbioportal_qc.logged_requests`. `test/parity/loggedgen` picks a sample of
them, GET and POST, in proportion to how often each endpoint is called.

It also marks requests whose difference is expected. The list is in
`test/parity/loggedgen/known.go`:

- **Saved sessions:** sessions and custom data exist only in www's session
  store.
- **Server-specific fields:** version numbers in `/api/info` and the address
  in the API documentation.
- **Bugs in www:** the study list shows resource definitions of other
  studies, and `studies/fetch` reports `allSampleCount: 1`.
- **Tied rows:** a page boundary falls among rows that Java leaves in no
  fixed order, such as samples with the same id in several studies.

`-annotate` adds these marks to an existing list. Compare against
api.cbioportal.org, with go serving a copy of production built by
`transform --from-prod`:

```bash
go run ./test/parity/loggedgen -n 500 > .parity/logged.txt      # needs CLICKHOUSE_URL/USER/PASSWORD
go run ./cmd/paritydiff -corpus .parity/logged.txt -b https://api.cbioportal.org \
  -ua cbioportal-go-parity -a http://localhost:18091 -cache .parity/cache-api -q
```

On 2026-10-03, 620 of 636 requests were identical and 16 were known
differences. One more request failed on www rather than differing: for a
study without clinical events, www can time out (HTTP 502), because Java then
reads the whole `clinical_event_data` table.

### Nightly parity job

`test/parity/nightly.sh` compares go.cbioportal.org with api.cbioportal.org
on real production traffic every day. For each request it reports whether the
answers are identical, a known difference, or unexplained.

**The request list.** `test/parity/logged_corpus.sh` builds a list of about
5,000 logged requests:

- They come from a capture window of `cbioportal_qc.logged_requests` set by
  `CORPUS_SINCE` (default 2026-10-07 22:30 UTC) and `CORPUS_UNTIL`. Each
  distinct request counts once.
- Only requests to www.cbioportal.org and api.cbioportal.org, only read-only
  endpoints, only those that returned 200 with a complete body.
- Busy endpoints get more requests, growing with the square root of their
  volume, with at least 3 each.
- A quarter (`CORPUS_API_SHARE`) comes from api.cbioportal.org, since that is
  the site go aims to replace first. Those entries are named `api-…`.
- The list changes once a week. It is saved to `.parity/nightly/corpus.txt`,
  which git ignores and only the owner can read, because it is real user
  traffic. Its header records the window, the requests per host and which
  endpoints are covered.
- Each run marks the list again with `loggedgen -annotate`, so new entries in
  `known.go` apply right away.

**How requests are sent.** Every request goes to api.cbioportal.org twice
(`-self-check`), at most 4 requests a second, with no cache. If Java's two
answers differ, the request is reported as *Java nondeterministic*, never as
unexplained. Requests that fail to connect or get 502, 503 or 504 are sent
once more after 2 minutes, so a server restart is not reported as a bug.

**The report.** Each run writes `runs/<date>/report.md` with:

- both servers' versions from `/api/info`, the window, traffic per host and
  endpoint coverage;
- counts of identical, known, Java nondeterministic, unexplained, status
  mismatch and error, overall and per host;
- the number of clean nights in a row (stored in `nights.txt` in
  `PARITY_STATE`). Seven clean nights are the bar for go to take over
  api.cbioportal.org;
- each unexplained request with its body (first 200 bytes) and the first
  lines of the difference, marking those that are new since the last run.

`runs/<date>/dump` keeps both responses of each unexplained request, up to
1 MB each. Runs are kept for 14 days. The script exits with status 1 when
anything is unexplained.

**The GitHub issue.** While anything is unexplained, the job keeps one issue
open in this repository, "Nightly parity: unexplained differences", with the
label `parity`. It opens the issue with the report, adds each later failing
report as a comment, and closes it after the first clean night.

| Option | Effect |
|---|---|
| `--dry-run` | print the GitHub actions instead of doing them, and do not count the night |
| `--regen` | rebuild the request list first (also done when the list has no window) |

**Where it runs.** On the parity host, cron starts it at 15:17 UTC, after the
mirror is rebuilt at 14:00. `~/.local/share/cbioportal-go-parity/run.sh`
checks out `origin/main` (or the ref in `state/ref`) into `src/`, keeps the
record of nights in `state/` (`PARITY_STATE`), rebuilds the request list on
Sundays, and logs to `logs/<date>.log`. A lock stops two runs from
overlapping.

## Java oracle

An oracle is a trusted source of correct answers. Here it is the Java backend
itself, run locally on the same test studies as go. It covers cases that
production traffic rarely reaches, such as gene panels, generic and
patient-level assays, hg38, allele-specific copy number, namespaces,
timelines and structural variants.

`test/java_oracle.sh db` builds the ClickHouse database `java_e2e` with the
tools inside the `cbioportal/cbioportal:master` image: its schema and seed
data, the Java importer for the gene panels, gene sets and five test studies,
and its derived-table script. This takes about 2 minutes.
`test/java_oracle.sh start` runs the Java backend on port 18095
(`clickhouse_mode=true`, `authenticate=false`, 2 GB memory).

```bash
test/java_oracle.sh db cbioportal-test      # needs ClickHouse on :18123 (container ch-dge)
test/java_oracle.sh start
go run ./cmd/paritydiff -a http://localhost:18090 -b http://localhost:18095 \
  -corpus test/parity/fixtures.txt -cache '' -rate 0 -q -dump .parity/oracle
API=http://localhost:18095 go test ./test/ -run Oracle   # the oracle cases hold for Java too
test/java_oracle.sh stop
```

`test/parity/fixtures.txt` holds about 1,450 requests built from the Java
backend's own answers: each study's profiles, sample lists, attributes and
the chart values its filters use. Rebuild it with
`go run ./test/parity/fixturegen > test/parity/fixtures.txt` rather than
editing it. The go API must serve databases built by `test/setup_test_db.sh`
from the same cbioportal-test checkout.

The `TestOracle*` contract tests lock in what this comparison fixed. You can
also run the whole contract suite against Java to see where a test expects
something different from Java. The session and configuration tests fail
there on purpose, because the local Java has no session service.

On 2026-10-04, 1,123 of 1,447 requests were identical. Most of the rest fall
into known groups:

| Difference | Why |
|---|---|
| Row order (about 180 requests) | The order comes from ClickHouse, not the data: the output of a `GROUP BY` or a join (study-view counts, gene panel data), ties in a sort (mutations of a gene in one sample, a sample's clinical attributes), or a Java `HashMap`. |
| Study `status` | Java's importer stores 1 (available). Production and go report 0. |
| Study `groups` order | It comes from a Java `HashSet`. |
| Numeric filter on a patient-level generic assay profile | Java ignores it (bug). |
| `/clinical-events-meta/fetch`, `/survival-data/fetch` | Java answers 500: a database query calls a function that does not exist (bug). |
| Categorical enrichments of a patient-level profile | Java answers 500 (bug). |
| `/clinical-data-table/fetch` with a `sortBy` that is not an attribute | Java answers 500 (bug). |

paritydiff ignores the order of keys in JSON. The order Java writes fields in
is checked separately by the `TestOracleProjection*` contract tests. Java
writes a model's fields in the order they are declared, parent class first,
and renamed fields last.

[Java quirks](java-quirks.md) lists every Java oddity that go copies, leaves
out or cannot match. These Java behaviours are left as they are:

- Generic assay data answers 500 for `projection=ID`.
- Some endpoints ignore `projection=META` and return a body instead of only a
  total count: `/sample-lists/fetch`, `/gene-panels/fetch`,
  `/resource-definitions/fetch`, the generic assay meta endpoints,
  `/mutation-data-counts/fetch` and `/treatments/sample-counts/fetch`.
- `/genes` answers 500 for `sortBy=cytoband` or `sortBy=length`, because the
  gene table has no such columns. It also re-sorts every page by symbol, so
  `sortBy` only decides which genes land on the page.
- Studies share mutation records in the importer's `mutation_event` table. A
  mutation therefore reports the genome build of the study that first
  imported it.
