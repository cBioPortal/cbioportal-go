# cbioportal-go

## Goal

Go reimplementation of cBioPortal — the **import pipeline** and the **REST
API** — backed by ClickHouse, with the unmodified cBioPortal frontend
(`frontend/` submodule) running on top. Long term this is meant to replace
`cBioPortal/cbioportal` and `cBioPortal/cbioportal-frontend` with one repo.

The Java backend (`cBioPortal/cbioportal`) and importer
(`cBioPortal/cbioportal-core`) are the reference: match their behaviour
exactly (fields, ordering, statistics, error contract), checking their source
rather than guessing. Production (www.cbioportal.org) is a useful second
reference for shapes; the source wins when they disagree.

## Architecture

- `cmd/cbioportal-import` — ELT importer: `ingest` streams study files into a
  raw staging DB (schema-on-read), `transform` builds the normalized model and
  the study-view `*_derived` tables with SQL in the target DB,
  `load-reference` loads genes/aliases/cancer types/reference genomes.
- `cmd/cbioportal-api` — the REST API plus the frontend build (single origin),
  `net/http` with Go 1.22 routing.
- `internal/api` handlers; `internal/transform` the SQL model;
  `internal/databin`, `internal/stats` ports of Java's binning and statistics;
  `internal/clickhouse` HTTP client.

See README.md for the stack diagram and quick start.

## Build & test

```bash
make build            # bin/cbioportal-import, bin/cbioportal-api
make test             # unit tests, no services
make lint             # gofmt + vet (CI)
make contract         # contract tests against a running API (API=...)
test/e2e_api.sh       # Keycloak + the fixture-database API on :18090
test/run_e2e.sh 3     # frontend Playwright suite against the API on :18090
```

Test tiers, fastest first: unit → contract (`go test ./test/`, seconds) →
e2e (full suite ~15–20 min; run single specs while iterating). The test
databases come from `test/setup_test_db.sh` (cBioPortal's fixture studies).
The e2e runner mirrors upstream CI (`CI=1` retries, `LOCALDEV=1`, the
config's 120s timeout, 3 workers) and logs in through Keycloak
(`test/e2e_api.sh` starts it and the API with `authenticate=oauth2`).

CI (GitHub Actions) runs lint, unit and contract tests on every push; the
full e2e weekly/on demand.

## Dependencies

Standard library first. Official, well-maintained dependencies are fine when
they earn their place — vendor libraries (e.g. ClickHouse's own Go driver)
and Go-team `golang.org/x/...` packages — provided they bring few transitive
dependencies. Avoid bloat and unmaintained or community-only packages for
things the stdlib already covers (routers, ORMs, validation, logging).
Statistics stay hand-ported: exact Java parity requires it. CI runs
`govulncheck` on every push.

## GitHub

Repo: <https://github.com/cBioPortal/cbioportal-go> (`origin`, default
branch `main`). Commit directly to `main`.

## Writing docs

Docs are for people: new contributors, the cBioPortal team, and users who
are not Go or ClickHouse experts. Write so an 8th grader could follow.

- Plain words, short sentences, one idea per sentence. Say what something
  does before how it does it.
- Explain a term or acronym the first time it appears, or link to where
  it is explained. Prefer the everyday word ("copy", "check", "list") over
  jargon ("materialize", "assert", "enumerate").
- Full sentences with verbs and articles. No telegraphic fragments
  ("deviates: kept"), arrows, or semicolon chains.
- Start long docs with a few sentences on what the page is for, then a
  table of contents.
- Tables for lists of facts; prose for reasons. Give an example when an
  idea is abstract.
- Current state only. History is in git, not in the docs.
- No commit hashes, internal ids or file paths in running text unless the
  reader needs them to act.

## No "prior state" comments in code

Comments describe current intent only, not what was changed from.
