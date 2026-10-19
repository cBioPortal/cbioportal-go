# Testing

This page explains how cbioportal-go is tested and how to run each kind of
test. There are three kinds of local tests, from fastest to slowest. The tools
that compare go with the Java backend are on a separate page,
[Parity with Java](rewrite/parity.md).

## Contents

- [Test tiers](#test-tiers)
- [Unit tests](#unit-tests)
- [Transform golden tests](#transform-golden-tests)
- [API contract tests](#api-contract-tests)
- [Frontend e2e tests](#frontend-e2e-tests)
- [CI](#ci)
- [Comparing with Java](#comparing-with-java)

## Test tiers

| Tier | What it checks | Command | Needs | Time |
|---|---|---|---|---|
| Unit | Single functions and packages | `make test` | nothing (some tests also use ClickHouse) | seconds |
| Contract | Each API endpoint gives exactly the answer the frontend expects | `make contract` | a running API on the test databases | ~5 s |
| Frontend e2e | The real cBioPortal web pages work, clicked through in a browser | `test/run_e2e.sh` | the API, the built frontend, Docker | 15–20 min for all |

ClickHouse is the database go reads from. The test databases come from
cBioPortal's own test studies, loaded by `test/setup_test_db.sh` (see
[API contract tests](#api-contract-tests)).

## Unit tests

`make test` runs the unit tests of every package, as CI does. It needs no
other service. Among other things,
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
database. `test/java_oracle.sh db` builds it (see [Java oracle](rewrite/parity.md#java-oracle)).

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
- 3 workers, as in upstream CI. This is also what the GitHub workflow
  uses on its 4-CPU runner.

`go run ./cmd/e2ereport --fails` summarizes the failures of the last run.

### Login with Keycloak

Keycloak is an open-source login server. As in upstream CI, the tests log in
through it. `test/e2e_api.sh` starts Keycloak 16.1.1 on port 8084 with the
realm `cbio` and the user `testuser` / `P@ssword1`, who has access to every
test study. It then runs the API with `authenticate=oauth2`. Docker must be
available. With `KEYCLOAK=false` the API runs without login.

Some login and export features have their own tests. Each is skipped
unless its variable is set. Two of them use this Keycloak.

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

**Basic login** (a shared user name and password, set with
`--login-basic-user`). The test signs in as `basicuser` at
`/j_spring_security_check` and checks that it sees only the study its role
allows:

```bash
BASIC=true test/e2e_api.sh
BASIC_LOGIN=1 API=http://localhost:18090 go test -run BasicLogin ./test/
```

**Study export** (`feature.study.export=true`). The test downloads study
exports from an API started with the feature turned on, and checks the
files in them:

```bash
bin/cbioportal-api --portal-properties=… --feature.study.export=true   # e.g. PORT=18096
EXPORT_API=http://localhost:18096 go test -run Export ./test/
```

The Redis session store has its own live test:
`REDIS_TEST_ADDR=… go test -run RedisLoginStoreLive ./internal/api/auth`.

## CI

On every push and pull request, GitHub Actions (`.github/workflows/ci.yml`)
checks formatting with gofmt and vet, runs the unit tests, and runs the
contract tests against a ClickHouse loaded with the test studies. The full
frontend e2e suite (`.github/workflows/e2e.yml`) runs once a week and when
started by hand.

## Comparing with Java

Tools that compare go's answers with the Java backend (on production and on a
local Java server) are described in [Parity with Java](rewrite/parity.md).
