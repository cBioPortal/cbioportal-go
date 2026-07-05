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
- `internal/reference` — gene/alias/cancer-type loading from the seed gzip via `FORMAT Values`.
- `internal/transform` — the SQL views (dimensions, mutation filter with Hugo→Entrez resolution, CNA/SV, clinical unpivot).
- `cmd/cbioportal-import` — CLI.

## Usage

```bash
go build -o bin/cbioportal-import ./cmd/cbioportal-import

bin/cbioportal-import plan <study-dir>
bin/cbioportal-import ingest <study-dir> --db cbioportal_raw
bin/cbioportal-import load-reference <seed.sql.gz> --db cbioportal_ref
bin/cbioportal-import load-gene-panels <panels-dir> --db cbioportal_raw
bin/cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref
```

`go test ./...` runs the parser unit tests.

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
