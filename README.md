# cbioportal-go

[![CI](https://github.com/inodb/cbioportal-go/actions/workflows/ci.yml/badge.svg)](https://github.com/inodb/cbioportal-go/actions/workflows/ci.yml)

A Go reimplementation of the cBioPortal backend: the study importer and the
REST API, backed by ClickHouse. The unmodified cBioPortal web frontend runs on
top of it. Built on the Go standard library (see [Dependencies](#dependencies)).

## How it fits together

```
 study files (datahub-format TSVs)
        │  cbioportal-import ingest          load raw files as-is
        ▼
 ClickHouse  cbioportal_raw  (staging)  +  cbioportal_ref  (genes, cancer types)
        │  cbioportal-import transform       SQL → normalized + derived tables
        ▼
 ClickHouse  cbioportal
        ▲
        │  SQL over HTTP
 cbioportal-api  ──  /api/*  cBioPortal REST API
        │          ──  everything else: the frontend build (frontend/dist)
        ▼
 browser  →  http://localhost:8080
```

| Piece | What it is |
|---|---|
| ClickHouse | the only database (HTTP interface, port 18123 below) |
| `cbioportal-import` | CLI: loads reference genes and studies, builds the queryable tables |
| `cbioportal-api` | one server for the API and the web UI (same origin, like a cBioPortal deployment) |
| `frontend/` | the cbioportal-frontend repo as a git submodule, built once into `frontend/dist` |

## Quick start

Needs Go 1.24, Docker, and Node 22 with pnpm (`corepack enable`) for the
frontend build, plus the cBioPortal seed dump and a study in datahub format
(both from [cBioPortal/datahub](https://github.com/cBioPortal/datahub):
`seedDB/seed-cbioportal_hg19_hg38_v2.14.5.sql.gz` and e.g.
`public/acbc_mskcc_2015`).

```bash
# 1. ClickHouse
docker run -d --name cbioportal-clickhouse -p 18123:8123 \
  -e CLICKHOUSE_SKIP_USER_SETUP=1 clickhouse/clickhouse-server:25.3

# 2. Build the two binaries
go build -o bin/cbioportal-import ./cmd/cbioportal-import
go build -o bin/cbioportal-api ./cmd/cbioportal-api

# 3. Reference data (genes, aliases, cancer types) — once
bin/cbioportal-import load-reference seed-cbioportal_hg19_hg38_v2.14.5.sql.gz --db cbioportal_ref

# 4. Import a study, then build the queryable tables
bin/cbioportal-import ingest datahub/public/acbc_mskcc_2015 --db cbioportal_raw
bin/cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref

# 5. Build the frontend — once (and after updating the submodule)
git submodule update --init
(cd frontend && pnpm install --frozen-lockfile && pnpm run buildAll)

# 6. Run the portal
FRONTEND_DIR=frontend/dist PORT=8080 CBIOPORTAL_DB=cbioportal bin/cbioportal-api
```

Open <http://localhost:8080>. The API is under the same origin, e.g.
<http://localhost:8080/api/studies>.

`make build`, `make frontend` and `make run` wrap steps 2, 5 and 6;
`make help` lists the other targets.

To add more studies, repeat step 4 (`ingest` for each study, then one
`transform`; re-ingesting a study replaces it) and restart nothing — the API
picks up changed tables within a second.

### Importer commands

```bash
bin/cbioportal-import plan <study-dir>                       # show what would be loaded
bin/cbioportal-import ingest <study-dir> --db cbioportal_raw
bin/cbioportal-import ingest-case-lists <study-dir>... --db cbioportal_raw   # restage only case_lists/
bin/cbioportal-import load-reference <seed.sql.gz> --db cbioportal_ref
bin/cbioportal-import load-gene-panels <panels-dir> --db cbioportal_raw
bin/cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref
```

`ingest` is fast (msk_impact_2017, ~11k samples, loads in seconds) because
files are streamed into ClickHouse as-is; `transform` then rebuilds every
derived table with SQL, which also takes seconds.

## API configuration

All settings are environment variables; only `CBIOPORTAL_DB` is usually needed.


| Variable | Meaning |
|---|---|
| `CLICKHOUSE_URL` | ClickHouse HTTP endpoint (default `http://localhost:18123`) |
| `CBIOPORTAL_DB` | database built by `transform` (e.g. `cbioportal`) |
| `CBIOPORTAL_RAW_DB`, `CBIOPORTAL_REF_DB` | staging/reference databases if not named `cbioportal_raw` / `cbioportal_ref` |
| `PORT` | listen port (binds 0.0.0.0) |
| `FRONTEND_DIR` | serve this frontend build for every non-`/api/` path (single origin) |
| `FRONTEND_URL` | reverse-proxy every non-`/api/` path to this frontend dev server instead |
| `ONCOKB_URL`, `ONCOKB_TOKEN` | OncoKB API behind the frontend's `/proxy/...` endpoint |
| `SESSION_STORE_FILE` | persist sessions (virtual studies, groups, custom data) to this JSON file |
| `AUTH_ENABLED`, `AUTH_JWT_SECRET` | HS256 bearer-token auth |
| `AUTH_DEFAULT_USER` | single-user development login for requests without a token |
| `PUBLISHER_API_KEY` | key required by `/api/public_virtual_studies` publish/unpublish |

## Code layout

- `cmd/cbioportal-import`, `cmd/cbioportal-api` — the two binaries;
  `cmd/apibench` times heavy API requests; `cmd/e2ereport` summarizes e2e runs.
- `internal/parser`, `internal/ingest` — study file parsing and streaming into
  staging tables.
- `internal/reference` — genes, aliases, cancer types and reference-genome
  genes from the seed dump.
- `internal/transform` — the SQL that builds the normalized model and the
  study-view `*_derived` tables, following cbioportal-core's import rules.
- `internal/api` — HTTP handlers; `internal/databin`, `internal/stats` — ports
  of cBioPortal's data binning and statistics.
- `internal/clickhouse` — minimal HTTP client.

## Testing

Three tiers, fastest first:

1. **Unit tests** — `go test ./internal/...` (no services needed). Includes the
   `internal/databin` port of Java's DataBinner, checked against ~1,800
   Java-recorded scenarios in `internal/databin/testdata`.
2. **API contract tests** — `make contract` (`go test ./test/`, ~5s) against a
   running API (`API=http://host:port`, default `http://localhost:18090`;
   skipped when unreachable). They pin the exact endpoint behaviour the
   frontend and e2e suite rely on — study-view filters, chart counts, bins,
   sessions, gene tables, enrichments, Java's projections and error contract.
   Add a case whenever a contract is fixed. They expect the API to serve the
   test databases built from cBioPortal's e2e fixture studies:

   ```bash
   git clone https://github.com/cBioPortal/cbioportal-test
   test/setup_test_db.sh cbioportal-test seed-cbioportal_hg19_hg38_v2.14.5.sql.gz
   AUTH_DEFAULT_USER=testuser PUBLISHER_API_KEY=SECRETKEY SESSION_STORE_FILE=.sessions.json \
     PORT=18090 CBIOPORTAL_DB=cbioportal_e2e CBIOPORTAL_RAW_DB=cbioportal_e2e_raw bin/cbioportal-api
   make contract
   ```
3. **Frontend e2e** — `test/run_e2e.sh [WORKERS] [spec...]` drives the
   cBioPortal Playwright suite (`frontend/end-to-end-test-playwright`)
   through the API origin, which serves the portal UI as a cBioPortal
   deployment does. `frontend/` is the cbioportal-frontend repo as a git
   submodule, so the backend and the frontend/e2e suite it is tested against
   are pinned together. Build it once (and after bumping the submodule) as
   upstream CI does:

   ```bash
   git submodule update --init
   (cd frontend && pnpm install --frozen-lockfile && pnpm run buildAll)
   (cd frontend/end-to-end-test-playwright && pnpm install --frozen-lockfile --ignore-workspace)
   FRONTEND_DIR=frontend/dist AUTH_DEFAULT_USER=testuser PUBLISHER_API_KEY=SECRETKEY \
     SESSION_STORE_FILE=.sessions.json PORT=18090 CBIOPORTAL_DB=cbioportal_e2e \
     CBIOPORTAL_RAW_DB=cbioportal_e2e_raw bin/cbioportal-api
   test/run_e2e.sh 3 tests/local/annotation-filter-menu.spec.ts
   ```

   The production build loads faster than the dev server, has no dev error
   overlay, and leaves memory for more Playwright workers.
   (`FRONTEND_URL=http://localhost:3000` proxies a dev server instead.)

   Run single specs while iterating; the full suite only at milestones.

   The runner mirrors the reference CI job: `CI=1` (one retry per test;
   tests that pass on retry are reported as flaky), `LOCALDEV=1`, and the
   config's 120s test timeout. 3 workers matches CI; on a 4-core box shared
   with ClickHouse, expect occasional load-induced flakes above that.
   `go run ./cmd/e2ereport --fails` summarizes the last run.

   Known environment gap: `patient-view.spec.ts` "toggles gene panel modal
   from sample icon in copy number table" fails here but not in upstream CI.
   CI logs in through Keycloak, which leaves the mouse on the login button;
   without a login step the mouse stays at (0,0), where the tooltip first
   renders before it is positioned, and Chrome (RenderDocument is re-enabled
   because the LOCALDEV `--disable-features` flag replaces Playwright's
   defaults) treats the move away as a mouseleave and closes the tooltip.
   Not a backend issue. Leave it out with
   `E2E_SKIP='toggles gene panel modal from sample icon in copy number' test/run_e2e.sh 3`.

CI (`.github/workflows/ci.yml`) runs gofmt, vet and the unit tests, then the
contract tests against a ClickHouse service loaded with the fixture studies,
on every push and pull request. The full frontend e2e suite
(`.github/workflows/e2e.yml`) runs weekly and on demand.

## Dependencies

Standard library first: HTTP serving and routing, JSON, gzip, the OncoKB
reverse proxy and profiling all come from it, so a build is a single
`go build`. Official, well-maintained dependencies (vendor libraries such as
ClickHouse's Go driver, Go-team `golang.org/x/...` packages) are fine when they
earn their place and bring few transitive dependencies; community packages
for what the stdlib already covers are avoided. Statistics stay hand-ported:
results must match the Java backend (commons-math, Datumbox) to the last
digit. CI runs `govulncheck` on every push.

## Notes learned porting

- Go's stdlib `flag` stops parsing at the first positional arg, so `ingest <dir>
  --db X` would silently ignore `--db`; the CLI reorders flags ahead of
  positionals to fix this.
- ClickHouse `ALTER … DELETE` is asynchronous; re-ingest uses
  `SETTINGS mutations_sync = 1` so a study's old rows are gone before the count.
- An `io.Reader` that returns one line per `Read` makes the HTTP transport do
  tiny writes; `BodyReader.Read` fills the whole buffer (12× faster on the seg
  file).

## License

Apache License 2.0 — see [LICENSE](LICENSE).
