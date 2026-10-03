# Architecture

## Data flow

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
                 ──  everything else: the frontend build (frontend/dist)
```

- **Ingest** streams each study's files into staging tables shaped like the
  files (schema-on-read), so loading is bound by ClickHouse, not parsing
  (msk_impact_2017, ~11k samples, loads in seconds). Re-ingesting a study
  replaces it.
- **Transform** rebuilds the normalized model and the study-view `*_derived`
  tables with SQL, applying cbioportal-core's import rules (gene resolution,
  case lists, mutation filtering, computed clinical attributes). Every table the
  API reads exists for any mix of data types, empty when the data is absent.
  `transform --from-prod` builds the same objects from a clone of cBioPortal's
  production ClickHouse database instead, mostly as views over its `*_derived`
  tables (see [Deployment](deployment.md#mirroring-production)).
- **The API** answers the cBioPortal REST API from those tables and serves the
  frontend build on the same origin. The Java backend
  ([cBioPortal/cbioportal](https://github.com/cBioPortal/cbioportal)) and
  importer ([cbioportal-core](https://github.com/cBioPortal/cbioportal-core))
  are the reference for behaviour: fields, ordering, statistics and errors.

## Code layout

- `cmd/cbioportal-import`, `cmd/cbioportal-api` — the two binaries;
  `cmd/apibench` times heavy API requests; `cmd/e2ereport` summarizes e2e runs;
  `cmd/paritydiff` compares API responses with another origin (www).
- `internal/parser`, `internal/ingest` — study file parsing and streaming into
  staging tables.
- `internal/reference` — genes, aliases, cancer types and reference-genome
  genes from the seed dump.
- `internal/transform` — the SQL that builds the normalized model and the
  study-view `*_derived` tables, following cbioportal-core's import rules.
- `internal/api` — HTTP handlers; `internal/databin`, `internal/stats` — ports
  of cBioPortal's data binning and statistics.
- `internal/clickhouse` — minimal HTTP client. Request values reach ClickHouse
  as server-side query parameters (`{name:Type}` placeholders, `ch.QueryP`;
  in `internal/api`, `sqlArgs` binds them while a statement is assembled), not
  as SQL text. Parameters ride in the URL when small and in a
  multipart/form-data body otherwise; arrays longer than a form field
  (ClickHouse's `http_max_field_value_size`, 128 KiB) are sent as external
  tables the placeholder selects from. `sqlLit`/`inList` remain for values
  chosen by code and for statements cached by their SQL text (`cachedQuery`).

## Dependencies

Standard library first: HTTP serving and routing, JSON, gzip, the OncoKB
reverse proxy and profiling all come from it, so a build is a single
`go build`. Official, well-maintained dependencies (vendor libraries such as
ClickHouse's Go driver, Go-team `golang.org/x/...` packages) are fine when they
earn their place and bring few transitive dependencies; community packages
for what the stdlib already covers are avoided. Statistics stay hand-ported:
results must match the Java backend (commons-math, Datumbox) to the last
digit. CI runs `govulncheck` on every push.

Current dependencies, neither with transitive dependencies of its own:

- `go.yaml.in/yaml/v3` (the YAML organization's maintained yaml.v3) — study
  tags and gene set hierarchy files.
- `golang.org/x/sync` — `singleflight` for the metadata cache.

## Notes learned porting

- Go's stdlib `flag` stops parsing at the first positional arg, so `ingest <dir>
  --db X` would silently ignore `--db`; the CLI reorders flags ahead of
  positionals to fix this.
- ClickHouse `ALTER … DELETE` is asynchronous; re-ingest uses
  `SETTINGS mutations_sync = 1` so a study's old rows are gone before the count.
- An `io.Reader` that returns one line per `Read` makes the HTTP transport do
  tiny writes; `BodyReader.Read` fills the whole buffer (12× faster on the seg
  file).
