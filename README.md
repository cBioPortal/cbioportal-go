# cbioportal-go

[![CI](https://github.com/inodb/cbioportal-go/actions/workflows/ci.yml/badge.svg)](https://github.com/inodb/cbioportal-go/actions/workflows/ci.yml)

> [!WARNING]
> **Experimental.** This is an experiment to reimplement
> [cBioPortal](https://www.cbioportal.org) in Go. It is not an official
> cBioPortal release, results may differ from www.cbioportal.org, and nothing
> here should be used for research or clinical decisions. For the real portal
> go to <https://www.cbioportal.org>.

A Go reimplementation of the cBioPortal backend — the study importer and the
REST API — backed by ClickHouse, with the unmodified cBioPortal web frontend
running on top.

**Preview server:** <https://go.cbioportal.org> — a subset of public
[datahub](https://github.com/cBioPortal/datahub) studies, re-imported daily.

```
study files ──cbioportal-import──▶ ClickHouse ◀──cbioportal-api──▶ browser (API + frontend)
```

- **ClickHouse** is the only database.
- **`cbioportal-import`** loads reference data and studies and builds the tables the API reads.
- **`cbioportal-api`** serves the REST API and the web UI from one origin.
- **`frontend/`** is the cbioportal-frontend repo as a git submodule.

## Quick start

Needs Go 1.26, Docker, and Node 22 with pnpm (`corepack enable`), plus the
cBioPortal seed dump and a study from
[cBioPortal/datahub](https://github.com/cBioPortal/datahub)
(`seedDB/seed-cbioportal_hg19_hg38_v2.14.5.sql.gz`, e.g. `public/acbc_mskcc_2015`).

```bash
# 1. ClickHouse
docker run -d --name cbioportal-clickhouse -p 18123:8123 \
  -e CLICKHOUSE_SKIP_USER_SETUP=1 clickhouse/clickhouse-server:25.3

# 2. Build
make build

# 3. Reference data (genes, aliases, cancer types) — once
bin/cbioportal-import load-reference seed-cbioportal_hg19_hg38_v2.14.5.sql.gz --db cbioportal_ref

# 4. Import a study and build the tables
bin/cbioportal-import ingest datahub/public/acbc_mskcc_2015 --db cbioportal_raw
bin/cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref

# 5. Build the frontend — once
make frontend

# 6. Run the portal
make run        # http://localhost:8080
```

To add studies, `ingest` each one and run `transform` again; the running API
picks up the new tables by itself.

## Development

```bash
make test       # unit tests (no services)
make lint       # gofmt + go vet, as CI runs them
make contract   # API contract tests against a running API
make e2e        # the cBioPortal Playwright suite against the API on :18090
make help       # all targets
```

CI runs lint, unit and contract tests on every push; the full e2e suite runs
weekly.

## TODO for a full reimplementation

- **Authentication.** Only an optional single-user development login exists.
  Needed: OAuth2/OIDC sign-in (Google and Microsoft, as on www), SAML/Keycloak
  for private portals, and data access tokens for API clients.
- **Authorization.** Per-study access from study groups and user roles, so
  `readPermission` and every endpoint honour it (today all studies are
  readable).
- **Session service.** Sessions (virtual studies, groups, custom data,
  settings) live in a JSON file in one process. Needed: a durable shared store,
  or compatibility with cBioPortal's session service so sessions — and links —
  carry over from existing portals; per-user saved studies once login exists.
- **Importer coverage.** Tested on a handful of studies; needs validation
  against all of datahub and cbioportal-core's validator rules.
- **API parity.** Remaining Java endpoints and differences (study export
  `.zip`, URL shortener, MatchMiner, newer column-store response shapes); the
  API still reads the staging database for a few per-gene matrices.
- **Performance.** Whole-profile reads of large studies (expression
  enrichments, co-expression on TCGA PanCancer), RowBinary decoding for large
  responses, projections applied in the handlers instead of a middleware.
- **Tests.** One e2e test needs upstream CI's Keycloak login step to pass (see
  [Testing](docs/testing.md)).

## Documentation

- [Configuration](docs/configuration.md) — command-line flags, environment
  variables, portal properties (reuse an existing `portal.properties`).
- [Testing](docs/testing.md) — test tiers, test databases, the e2e harness, CI.
- [Architecture](docs/architecture.md) — data flow, code layout, dependency
  policy.
- [Benchmark](docs/benchmark.md) — go.cbioportal.org vs www.cbioportal.org:
  page speed, resource use, cost estimate.
- [Deployment](docs/deployment.md) — the container image and the daily
  datahub import behind go.cbioportal.org.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
