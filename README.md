# cbioportal-go

A Go port of the cBioPortal ELT import pipeline (a rewrite of the Rust
`cbioportal-import` in the parent directory). Streams raw datahub study TSVs
straight into ClickHouse staging tables (schema-on-read), then builds the
normalized model as SQL views.

Stdlib-only (no external dependencies): `net/http` for the ClickHouse HTTP
interface, `compress/gzip` for the seed dump.

## Binaries

- `cbioportal-import` — the ELT loader (ingest / transform / load-reference / load-gene-panels).
- `cbioportal-api` — a cBioPortal-compatible REST API served from the transform
  views (`net/http` + Go 1.22 routing, stdlib only). Serves the same endpoint
  surface as the Rust version — studies/samples/patients/clinical/molecular-
  profiles/mutations/gene-panels/cancer-types, POST fetch variants, config
  bootstrap, session service, and the study-view aggregations (clinical-data-
  counts, mutated-genes, cna-genes, structuralvariant-genes, molecular-profile-
  sample-counts). The real cBioPortal frontend (rspack dev server) renders its
  homepage and full study view against it unchanged.

  ```bash
  go build -o bin/cbioportal-api ./cmd/cbioportal-api
  PORT=8080 CBIOPORTAL_DB=cbioportal bin/cbioportal-api
  # point the frontend at it: env/custom.sh -> CBIOPORTAL_URL=http://<host>:8080
  ```

  The `e2e/` Playwright suite (in the parent repo) passes against this backend:
  studies list, study view, and the real frontend homepage + study view.

## Import layout

- `internal/clickhouse` — HTTP client (Execute, InsertStream, JSON query helper, schema helpers, synchronous DeleteWhere).
- `internal/parser` — meta files, study scan/classify, TSV header + streaming `BodyReader`/`MatrixUnpivotReader` (`io.Reader`s), clinical attribute-meta.
- `internal/ingest` — orchestration: record/matrix/headerless streaming, provenance columns, schema evolution, dimensions (study/profile/gene-panel/clinical-meta).
- `internal/reference` — gene/alias/cancer-type and reference-genome gene loading from the seed gzip via `FORMAT Values`.
- `internal/transform` — the SQL views and tables (dimensions, cbioportal-core's gene resolution per importer recorded in `gene_key_map`, mutation filter, CNA/SV, case lists, clinical unpivot).
- `cmd/cbioportal-import` — CLI.

## Usage

```bash
go build -o bin/cbioportal-import ./cmd/cbioportal-import

bin/cbioportal-import plan <study-dir>
bin/cbioportal-import ingest <study-dir> --db cbioportal_raw
bin/cbioportal-import load-reference <seed.sql.gz> --db cbioportal_ref
bin/cbioportal-import load-gene-panels <panels-dir> --db cbioportal_raw
bin/cbioportal-import ingest-case-lists <study-dir>... --db cbioportal_raw   # restage only case_lists/
bin/cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref
```

## Testing

Three tiers, fastest first:

1. **Unit tests** — `go test ./internal/...` (no services needed). Includes the
   `internal/databin` port of Java's DataBinner, checked against ~1,800
   Java-recorded scenarios in `internal/databin/testdata`.
2. **API contract tests** — `go test ./test/` (~5s) against a running API
   (`API=http://host:port`, default `http://localhost:18090`; skipped when
   unreachable). They pin the exact endpoint behaviour the frontend and e2e
   suite rely on: study-view filters, chart counts, bins, sessions, gene
   tables, enrichments, resources. Add a case whenever a contract is fixed.
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
     SESSION_STORE_FILE=.sessions.json PORT=18090 bin/cbioportal-api
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
   Not a backend issue.

## API environment

| Variable | Meaning |
|---|---|
| `CLICKHOUSE_URL`, `CBIOPORTAL_DB` | ClickHouse HTTP endpoint and transform database |
| `PORT` | listen port (binds 0.0.0.0) |
| `FRONTEND_DIR` | serve this frontend build for every non-`/api/` path (single origin) |
| `FRONTEND_URL` | reverse-proxy every non-`/api/` path to this frontend dev server instead |
| `ONCOKB_URL`, `ONCOKB_TOKEN` | OncoKB API behind the frontend's `/proxy/...` endpoint |
| `SESSION_STORE_FILE` | persist sessions (virtual studies, groups, custom data) to this JSON file |
| `AUTH_ENABLED`, `AUTH_JWT_SECRET` | HS256 bearer-token auth |
| `AUTH_DEFAULT_USER` | single-user development login for requests without a token |
| `PUBLISHER_API_KEY` | key required by `/api/public_virtual_studies` publish/unpublish |

## Parity with the Rust version

Verified byte-for-count identical on `study_es_0` and `msk_impact_2017`:
staging row counts match per file, and all transform views match
(patient 841, sample 845, mutation 34, cna 1743, seg 9, clinical long
844/3361, …). Throughput on msk_impact_2017 is ~3.5s for 5.2M rows
(ClickHouse-bound, comparable to the Rust version).

## Notes learned porting

- Go's stdlib `flag` stops parsing at the first positional arg, so `ingest <dir>
  --db X` would silently ignore `--db`; the CLI reorders flags ahead of
  positionals to fix this.
- ClickHouse `ALTER … DELETE` is asynchronous; re-ingest uses
  `SETTINGS mutations_sync = 1` so a study's old rows are gone before the count.
- An `io.Reader` that returns one line per `Read` makes the HTTP transport do
  tiny writes; `BodyReader.Read` fills the whole buffer (12× faster on the seg
  file).
