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

## Documentation

- [Configuration](docs/configuration.md) — command-line flags, environment
  variables, portal properties (reuse an existing `portal.properties`).
- [Testing](docs/testing.md) — test tiers, test databases, the e2e harness, CI.
- [Architecture](docs/architecture.md) — data flow, code layout, dependency
  policy.
- [Deployment](docs/deployment.md) — the container image and the daily
  datahub import behind go.cbioportal.org.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
