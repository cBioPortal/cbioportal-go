# cbioportal-go

[![CI](https://github.com/cBioPortal/cbioportal-go/actions/workflows/ci.yml/badge.svg)](https://github.com/cBioPortal/cbioportal-go/actions/workflows/ci.yml)

> [!WARNING]
> **Experimental.** This is an experiment to rebuild
> [cBioPortal](https://www.cbioportal.org) in the Go programming language. It
> is not an official cBioPortal release. Its results may differ from
> www.cbioportal.org, so do not use it for research or clinical decisions.
> For the real portal, go to <https://www.cbioportal.org>.

cBioPortal is a website for exploring cancer genomics data. Its server side
is written in Java. This project rewrites two parts of that server in Go:

- the **importer**, which reads study data files and loads them into a
  database, and
- the **REST API**, which the website calls to get data.

The data lives in ClickHouse, a database built for fast counting and
summarizing over very large tables. The cBioPortal website itself (the
frontend) runs on top without any changes.

**Preview server:** <https://go.cbioportal.org> has the same data as
www.cbioportal.org. It reads a copy (a "mirror") of the production database,
which is rebuilt within an hour after each new import on production.

## Contents

- [How it fits together](#how-it-fits-together)
- [Quick start](#quick-start)
- [Development](#development)
- [What is still missing](#what-is-still-missing)
- [Documentation](#documentation)
- [License](#license)

## How it fits together

```
study files ──cbioportal-import──▶ ClickHouse ◀──cbioportal-api──▶ browser (API + website)
```

| Part | What it does |
|---|---|
| ClickHouse | The only database. It holds all study data. |
| `cbioportal-import` | Loads reference data (such as genes and cancer types) and studies, then builds the tables the API reads. |
| `cbioportal-api` | Answers API requests and serves the website, both from the same address. |
| `frontend/` | The unchanged cbioportal-frontend repository, included as a git submodule (a pinned copy of another repository). |

## Quick start

You need Go 1.27, Docker, and Node 22 with pnpm (run `corepack enable` to
get pnpm). You also need two downloads from
[cBioPortal/datahub](https://github.com/cBioPortal/datahub):

- the seed file `seedDB/seed-cbioportal_hg19_hg38_v2.14.5.sql.gz`, which
  holds the reference data, and
- at least one study folder, for example `public/acbc_mskcc_2015`.

```bash
# 1. Start ClickHouse
docker run -d --name cbioportal-clickhouse -p 18123:8123 \
  -e CLICKHOUSE_SKIP_USER_SETUP=1 clickhouse/clickhouse-server:25.3

# 2. Build the programs (into bin/)
make build

# 3. Load the reference data (genes, gene aliases, cancer types). Do this once.
bin/cbioportal-import load-reference seed-cbioportal_hg19_hg38_v2.14.5.sql.gz --db cbioportal_ref

# 4. Load a study, then build the tables the API reads
bin/cbioportal-import ingest datahub/public/acbc_mskcc_2015 --db cbioportal_raw
bin/cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref

# 5. Build the website. Do this once.
make frontend

# 6. Start the portal
make run        # http://localhost:8080
```

Loading a study takes two steps. `ingest` copies the study's files into the
database as they are. `transform` then turns them into the tables the API
uses. To add more studies, run `ingest` for each one and then run
`transform` again. The running API notices the new tables on its own.

## Development

```bash
make test       # unit tests (no database needed)
make lint       # code format and checks, the same as CI
make contract   # API contract tests against a running API
make e2e        # the cBioPortal website test suite against the API on port 18090
make help       # list all targets
```

Contract tests send requests to every API endpoint and check the answers.
The e2e ("end-to-end") tests drive the real website in a browser with
Playwright. CI (GitHub's automatic checks) runs the format checks, unit
tests and contract tests on every push. It runs the full e2e suite once a
week. See [Testing](docs/testing.md) for details.

## What is still missing

| Area | What works | What is still needed |
|---|---|---|
| Login | OAuth2/OpenID Connect login (`--login=required`, for example Keycloak). Optional sign-in on an open portal (`--login=optional`, for example Google and Microsoft, as on www). Access to studies based on the user's roles. Data access tokens for API clients (`dat.method=oauth2`). Login sessions stored in Redis (`--login-session-store=redis`). The Java portal's properties for all of these (`authenticate=oauth2`, `spring.security.oauth2.client.*`, `spring.session.store-type=redis`) work too; see [Configuration](docs/configuration.md#login-and-study-authorization). | SAML login, `authorization=true` (users listed in the database), and login providers that do not use OpenID (GitHub, Facebook). |
| Saved sessions | Saved pages, virtual studies and custom data are kept in cBioPortal's session service (`session.service.url`). That service can be shared with existing portals and between copies of the API. A simpler built-in store also works for a single server. See [Configuration](docs/configuration.md#sessions). | When you read one virtual study or custom data set by its id, go returns every stored field. Java leaves out the owner, users, version and timestamps, and adds the filter's `uniqueStudyIds`. |
| Importer | Tested on a handful of studies. | Testing against all of datahub, and against the checks in cbioportal-core's validator. |
| API | 179 of the Java backend's 193 endpoints work (see [API coverage](docs/rewrite/api-coverage.md)). On real production traffic, every answer matches www or has a written reason why not (see [Benchmark](docs/rewrite/benchmark.md)). | Not served: the SAML login page, MatchMiner, the URL shortener, old proxy endpoints the website does not use, an MSK-internal id lookup, and `getexternalpage.json` (left out on purpose, because it would fetch any URL for anyone). The main remaining difference is the order of rows that tie when sorted. Java returns those in the order its importer inserted them. |
| Speed and memory | Large answers (clinical data, mutations, molecular and generic-assay data, copy number, structural variants, clinical events) are sent one row at a time. So a request for a whole 50,000-sample study stays under 100 MB of memory. | Some answers are still built fully in memory: pages sorted with `sortBy`, and the lists of samples, sample lists and profiles. Reading a whole profile of a very large study takes several seconds. Examples are expression enrichments and co-expression on TCGA PanCancer. |

## Documentation

How go works and how to run it ([index](docs/README.md)):

| Page | What it covers |
|---|---|
| [Architecture](docs/architecture.md) | How data moves through the system, how the code is organized, and which libraries it uses. |
| [Configuration](docs/configuration.md) | Command-line flags, environment variables, and portal properties. You can reuse an existing `portal.properties`. |
| [Command-line conventions](docs/cli.md) | JSON output, dry runs, exit codes and suggestions, shared by every program. |
| [Deployment](docs/deployment.md) | The container image, Kubernetes, the production mirror behind go.cbioportal.org, hub.cbioportal.org and pull request previews. |
| [Testing](docs/testing.md) | The kinds of tests, the test databases, the e2e setup, and CI. |

The move from Java to go ([index](docs/rewrite/README.md)):

| Page | What it covers |
|---|---|
| [Method](docs/rewrite/method.md) | How the rewrite was built and checked against the Java portal. |
| [Parity with Java](docs/rewrite/parity.md) | The tools that compare go's answers with Java's, including the nightly job. |
| [Java quirks](docs/rewrite/java-quirks.md) | Bugs and oddities in the Java portal, and whether go copies them, fixes them, or cannot match them. |
| [API coverage](docs/rewrite/api-coverage.md) | Every Java endpoint, whether go serves it, and which tests cover it. |
| [Benchmark](docs/rewrite/benchmark.md) | go compared with Java: page speed, resource use, import speed, and cost. |

## License

Apache License 2.0. See [LICENSE](LICENSE).
