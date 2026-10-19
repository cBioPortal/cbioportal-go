# Architecture

This page explains how cbioportal-go is built. It follows a study from its
data files to the browser, shows where each part lives in the code, and lists
the outside libraries the project uses. Read it before you change the
importer or the API.

## Contents

- [Data flow](#data-flow)
- [Code layout](#code-layout)
- [The ClickHouse client](#the-clickhouse-client)
- [Dependencies](#dependencies)
- [ClickHouse rules](#clickhouse-rules)

## Data flow

All data lives in ClickHouse, a database built for fast counting and
summarizing over very large tables. Data moves through three databases:

| Database | What it holds |
|---|---|
| `cbioportal_raw` | The "staging" area. Each study file is copied here as it is, one table per kind of file. |
| `cbioportal_ref` | Reference data that every study shares: genes, gene aliases and cancer types. |
| `cbioportal` | The finished tables that the API reads. |

```
 study files (datahub-format TSVs)
        │  cbioportal-import ingest          copy the files in as they are
        ▼
 ClickHouse  cbioportal_raw  (staging)  +  cbioportal_ref  (genes, cancer types)
        │  cbioportal-import transform       build the API's tables with SQL
        ▼
 ClickHouse  cbioportal
        ▲
        │  SQL over HTTP
 cbioportal-api  ──  /api/*  cBioPortal REST API
                 ──  everything else: the website build (frontend/dist)
```

The importer follows a pattern called ELT: **e**xtract and **l**oad the
files first, then **t**ransform them inside the database. The Java importer
does it the other way around. It cleans each row in Java before it stores
it.

### Ingest: copy the files in

`cbioportal-import ingest` streams each study file into a staging table
whose columns match the file's columns. It does not check or clean the data
yet. Because there is little work per row, ClickHouse's own speed sets the
pace. For example, msk_impact_2017 (about 11,000 samples) loads in seconds.
Ingesting a study again replaces the old copy.

### Transform: build the API's tables

`cbioportal-import transform` reads the staging tables and builds the tables
the API uses. All of this work is done with SQL inside ClickHouse. It
follows the same import rules as the Java importer, cbioportal-core. For
example, it matches gene names to genes, builds the case lists, filters
mutations, and computes extra clinical attributes.

It builds two kinds of tables:

- the **normalized model**: one table per kind of thing (studies, samples,
  mutations, and so on), similar to the Java database, and
- the **`*_derived` tables**: wide tables, shaped for the study view page so
  its charts can be counted quickly.

A few rules keep the results the same as Java:

- Every table the API reads always exists, even when no study has that kind
  of data. It is just empty then.
- Studies, patients, samples, sample lists and segments get the same
  internal ids that the Java importer would give them. Studies are numbered
  in ingest order. Inside a study, ids follow the order in which the Java
  importer reads the folder and files. This matters because the Java API
  returns many lists sorted by these ids.

**Nothing changes until the transform finishes.** Each table is first built
under a temporary name (`__next_<name>`). At the end, all new tables are
swapped in at once with ClickHouse's `EXCHANGE TABLES`, and the views are
recreated on top of them. So the API keeps serving the previous build while
a transform runs. If a transform fails, nothing changes.

`transform --from-prod` builds the same tables from a different source: a
copy of cBioPortal's production ClickHouse database. Most of them become
views over production's own `*_derived` tables. This is how go.cbioportal.org
gets the same data as www.cbioportal.org (see
[Deployment](deployment.md#mirroring-production)). It is needed only while
go replaces the Java cBioPortal.

### The API

`cbioportal-api` answers the cBioPortal REST API from the `cbioportal`
tables. It also serves the website build from the same address. The Java
backend ([cBioPortal/cbioportal](https://github.com/cBioPortal/cbioportal))
and the Java importer
([cbioportal-core](https://github.com/cBioPortal/cbioportal-core)) are the
reference for how things should behave. That covers fields, row order,
statistics and errors.

## Code layout

| Folder | What it contains |
|---|---|
| `cmd/cbioportal-import`, `cmd/cbioportal-api` | The two programs: the importer and the API server. |
| `cmd/apibench` | A tool that times slow, heavy API requests. |
| `cmd/e2ereport` | A tool that summarizes the results of an e2e test run. |
| `internal/parser`, `internal/ingest` | Reading study files and streaming them into staging tables. |
| `internal/studyimport` | Importing into a portal that is being served: the import lock and the import steps (`import-study`). |
| `internal/datahub` | Checking out datahub studies with their large (LFS) files (`fetch-datahub`). |
| `internal/reference` | Loading genes, aliases, cancer types and reference-genome genes from the seed file. |
| `internal/transform` | The SQL that builds the normalized model and the `*_derived` tables. |
| `internal/api` | The API server: it puts the packages below together into one router. |
| `internal/api/core` | What every handler uses: the server state, Java's error answers and parameter checks, paging, streaming JSON, the queries that read the big study tables, and the metadata cache. |
| `internal/api/studies` | Studies, samples, patients, clinical data, sample lists, genes, cancer types, resources and study tags. |
| `internal/api/molecular` | Molecular profiles and their data: mutations, copy number, structural variants, generic assays, gene sets, gene panels, co-expression. |
| `internal/api/studyview` | The study view: its filter, counts, bins, charts, the clinical data table and treatments. |
| `internal/api/comparison` | Group comparison: enrichments, survival and clinical events. |
| `internal/api/session`, `internal/api/sessionstore` | Saved sessions and virtual studies, and where they are stored. |
| `internal/api/export` | Downloading a study as study files. |
| `internal/api/auth` | Logging in, who may read which study, and data access tokens. |
| `internal/api/portal`, `internal/api/apidocs` | The portal's own pages (frontend settings, status, sitemaps) and the API descriptions. |
| `internal/databin`, `internal/stats` | Copies of the Java portal's chart binning and statistics code, rewritten in Go. |
| `internal/javaorder` | Code that reproduces the order in which Java collections list their items, where that order shows up in answers. |
| `internal/config` | Reading settings, including an existing `portal.properties`. |
| `internal/metrics` | Server metrics in the Prometheus format. |
| `internal/clickhouse` | The client that talks to ClickHouse (next section). |
| `ops/cbioportal.org` | The cBioPortal team's own jobs (`cbioportal-ops`): PR previews and hub.cbioportal.org imports. |
| `rewrite/java-v7-to-go` | Tools for the move from Java cBioPortal, run as `cbioportal-rewrite-test java-v7-to-go ...`: the production mirror, the nightly parity check, and `paritydiff`, which sends the same requests to go and to another server (such as www) and compares the answers. Deleting the folder removes them. |

## The ClickHouse client

`internal/clickhouse` talks to ClickHouse over its HTTP interface. You only
need this section if you work on database queries.

**Reading rows.** `ch.Query[T]` and `ch.QueryP[T]` read rows into Go
structs. They use ClickHouse's official Go driver (clickhouse-go) and its
compact binary "Native" format. The driver uses the same HTTP address,
login and metrics as everything else. Columns are matched to struct fields
by the fields' `json` names. The values come out exactly as they would from
ClickHouse's JSON output format (JSONEachRow).

Some results still go through JSONEachRow instead:

- columns of other types in a struct field, such as Tuple, Map,
  Date/DateTime, Decimal or UUID,
- rows read as `any` or as maps,
- fields of type `json.RawMessage` or with their own JSON decoding, and
- every error.

You can change this with the `CLICKHOUSE_DECODE` environment variable.
`CLICKHOUSE_DECODE=json` sends every read through JSONEachRow.
`CLICKHOUSE_DECODE=log` logs each statement that falls back to it.

Creating tables, inserting rows and reading plain text use plain HTTP
requests.

**Passing values safely.** Values that come from a request are never pasted
into the SQL text. They are sent as ClickHouse query parameters instead. The
SQL contains placeholders such as `{name:Type}`, and ClickHouse fills them
in. `ch.QueryP` does this. Inside `internal/api`, `core.SQLArgs` collects
the values while a statement is put together.

- Small parameters are sent in the URL.
- Larger ones are sent in a multipart/form-data request body.
- A list longer than one form field allows (ClickHouse's
  `http_max_field_value_size`, 128 KiB) is sent as a temporary "external
  table", and the placeholder selects from it.

`core.SQLLit` and `core.InList` still write values straight into the SQL. Use them
only for values chosen by the code, never for values from a request.

**Cache.** In `internal/api/core`, a small cache (`CachedQueryP`) keeps answers to
metadata queries.
Each entry is stored under its SQL text plus its parameter values.

## Dependencies

The project uses Go's standard library first. HTTP serving and routing,
JSON, gzip, the OncoKB proxy and profiling all come from it. So the whole
project builds with a plain `go build`.

Outside libraries are fine when they are official and well maintained and
pull in few other packages. Examples are vendor libraries such as
ClickHouse's Go driver, and the Go team's own `golang.org/x/...` packages.
A small, widely used community package is also fine when it saves real code
and brings almost no other packages with it. The project does not add
packages for things the standard library already does well, such as
routing, logging or validation.

Statistics code is copied by hand from Java, not taken from a Go library.
The results must match the Java backend (which uses commons-math and
Datumbox) down to the last digit. CI runs `govulncheck`, which checks
dependencies for known security problems, on every push.

The project uses these seven libraries directly:

| Library | What it is for |
|---|---|
| `github.com/ClickHouse/clickhouse-go/v2` | ClickHouse's official driver. It reads rows in the Native format over HTTP (see [the client](#the-clickhouse-client)). It brings along ch-go, compression (lz4, zstd, brotli), its Decimal type, and a few small packages. |
| `github.com/KimMachineGun/automemlimit` | Sets Go's memory limit to 80% of the container's memory limit when the API or the importer starts, so Go frees memory before the container runs out. |
| `github.com/spf13/cobra` | The subcommands of each program (such as `cbioportal-import ingest`), their help text, shell completion, and "did you mean" suggestions for a mistyped command. |
| `github.com/spf13/pflag` | GNU-style flags (`--db X` or `--db=X`), which may come before or after a folder name. Go's standard `flag` package stops reading flags at the first argument that is not a flag. |
| `go.yaml.in/yaml/v3` | Reading YAML files: study tags and the gene set hierarchy. This is the YAML organization's maintained version of yaml.v3. |
| `golang.org/x/crypto` | `bcrypt`, to check passwords for basic login against their stored hashes. |
| `golang.org/x/sync` | `singleflight`, so that many requests for the same data share one database query. |

## ClickHouse rules

These rules come from how ClickHouse works. Each one says what the code
does and why.

- **Deletes wait until they are done.** In ClickHouse, `ALTER … DELETE`
  runs in the background. So when a study is ingested again, the importer
  adds `SETTINGS mutations_sync = 2`. ClickHouse then waits until the old
  rows are gone before the importer counts the new ones.
- **Every request asks for consistent copies.** ClickHouse Cloud keeps
  several copies of the database, and two requests in a row can reach
  different copies. An `INSERT` right after `ALTER … ADD COLUMN` could reach
  a copy that does not have the new column yet. Cloud also skips an inserted
  block of rows that is identical to an earlier one, so a file ingested
  again without changes would stay deleted. To avoid both problems, the
  importer sends every request with `alter_sync = 2`, `mutations_sync = 2`,
  `select_sequential_consistency = 1` and `insert_deduplicate = 0`. A
  single-server ClickHouse ignores these settings.
- **Writes are sent in large pieces.** If a reader hands over one line at a
  time, the HTTP client sends many tiny pieces, which is slow. So
  `BodyReader.Read` fills its whole buffer each time. This makes loading a
  large file, such as a copy-number segment file, many times faster.
