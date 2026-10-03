# Configuration

Both binaries print their full usage with `-h`; `-version` (or
`cbioportal-import version`) prints the build, e.g.
`cbioportal-api 6b38ed6 (2026-10-02T09:15:00Z)` — the git commit, `+dirty` for
a build with uncommitted changes, and the commit time.

## cbioportal-import

```
$ cbioportal-import help
Commands:
  plan               list a study's data files and the staging tables they load into
  ingest             stage a study's data files in the raw database
  ingest-case-lists  restage only the case lists of one or more studies
  load-reference     load genes, gene aliases and cancer types from a seed dump
  load-gene-panels   load gene panel definitions into the raw database
  transform          build the portal database's tables from the raw and reference databases
  version            print the version
  help               show help for a command
```

```bash
cbioportal-import plan <study-dir>                                  # no ClickHouse needed
cbioportal-import ingest <study-dir> --db cbioportal_raw
cbioportal-import ingest-case-lists <study-dir>... --db cbioportal_raw
cbioportal-import load-reference <seed.sql.gz> --db cbioportal_ref
cbioportal-import load-gene-panels <panels-dir> --db cbioportal_raw
cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref
```

`cbioportal-import help <command>` (or `<command> -h`) shows a command's flags,
defaults and examples. Flags may come before or after the arguments, written
`-db x`, `--db x` or `--db=x`. Every command that connects to ClickHouse takes
`--url` (default `$CLICKHOUSE_URL`, else `http://localhost:18123`); the
credentials `CLICKHOUSE_USER` and `CLICKHOUSE_PASSWORD` are read from the
environment only.

Exit status: 0 on success, 1 when the command fails (message
`cbioportal-import: <command>: <error>` on stderr), 2 for a malformed command
line.

## cbioportal-api

Every server setting is a flag that falls back to an environment variable,
then to a built-in default: **flag > environment > default**. The container
image and Kubernetes manifests use the environment variables; on the command
line the flags are usually handier.

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `-port` | `PORT` | `8080` | listen port; binds all interfaces (0.0.0.0) |
| `-clickhouse-url` | `CLICKHOUSE_URL` | `http://localhost:18123` | ClickHouse HTTP endpoint (ClickHouse Cloud: `https://<host>:8443`) |
| `-db` | `CBIOPORTAL_DB` | `cbioportal` | database built by `transform` |
| `-raw-db` | `CBIOPORTAL_RAW_DB` | `cbioportal_raw` | staging database written by `ingest` |
| `-ref-db` | `CBIOPORTAL_REF_DB` | `cbioportal_ref` | reference database written by `load-reference` |
| `-frontend-dir` | `FRONTEND_DIR` | | serve this frontend build for every non-`/api/` path (single origin) |
| `-frontend-url` | `FRONTEND_URL` | | reverse-proxy every non-`/api/` path to this frontend dev server instead |
| `-oncokb-url` | `ONCOKB_URL` | `https://public.api.oncokb.org/api/v1` | OncoKB API behind the frontend's `/proxy/...` endpoint |
| `-site-banner`, `-site-banner-link` | `SITE_BANNER`, `SITE_BANNER_LINK` | | notice bar (and link) at the top of every portal page, e.g. for a preview deployment |
| `-session-store` | `SESSION_STORE_FILE` | | persist sessions (virtual studies, groups, custom data) to this JSON file; unset keeps them in memory |
| `-auth-enabled` | `AUTH_ENABLED` | `false` | require HS256 bearer tokens |
| `-auth-default-user` | `AUTH_DEFAULT_USER` | | single-user development login for requests without a token |
| `-portal-properties` | `PORTAL_PROPERTIES` | | portal properties file, see below |

Secrets have no flag, because command-line arguments are visible to every user
of the machine through the process list (`ps`):

| Environment only | Meaning |
|---|---|
| `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` | ClickHouse credentials (unset: the default user); also used by `cbioportal-import` |
| `ONCOKB_TOKEN` | OncoKB API token sent by the `/proxy/...` endpoint |
| `AUTH_JWT_SECRET` | HS256 secret bearer tokens are verified with |
| `PUBLISHER_API_KEY` | key required by `/api/public_virtual_studies` publish/unpublish |
| `CBIOPORTAL_PPROF_ADDR` | serve `net/http/pprof` on this address, e.g. `127.0.0.1:6060` |

```bash
cbioportal-api -db cbioportal -frontend-dir frontend/dist
PORT=8080 CBIOPORTAL_DB=cbioportal FRONTEND_DIR=frontend/dist cbioportal-api   # the same
```

At startup the server logs its version and the effective settings:

```
cbioportal-api 6b38ed6 (2026-10-02T09:15:00Z) listening on 0.0.0.0:8080 (clickhouse http://localhost:18123, db cbioportal, raw-db cbioportal_raw, ref-db cbioportal_ref)
portal properties: 5 used (authenticate, quick_search.enabled, session.service.url, skin.blurb, skin.title), 3 ignored (not used by this server: db.user, spring.datasource.password, spring.datasource.url)
```

## Portal properties

Portal settings — `skin.*` (title, blurb, logos, footer, example queries,
quick-select buttons), `priority_studies`, `quick_search.enabled`,
`oncokb.public_api.url`, … — use the Java portal's property names and are
passed the same two ways:

- **Arguments.** Any `--key=value` argument whose key is not one of the flags
  above is a portal property, exactly as the Java portal is started
  (`java -jar cbioportal.jar --skin.title=…`), so an existing deployment's
  argument list can be reused verbatim. A property needs both dashes and the
  `=`; a property that happens to share a flag's name can be set in the file.
- **A properties file** named by `-portal-properties` / `PORTAL_PROPERTIES`:
  an `application.properties`, or a legacy `portal.properties`, which has the
  same `key=value` syntax.

Arguments override the file, which overrides the built-in defaults.

```bash
cbioportal-api --skin.title="My portal" --quick_search.enabled=true
cbioportal-api --portal-properties=./portal.properties --skin.title="Preview"
```

**Reusing a cbioportal-docker-compose setup.** Point the server at the
properties file the docker-compose setup already has — `config/application.properties`
in current versions (mounted into the Java container as `application.properties`),
`config/portal.properties` in older ones:

```bash
cbioportal-api -db cbioportal --portal-properties=../cbioportal-docker-compose/config/application.properties

docker run --rm --network host \
  -v "$PWD/config/application.properties:/app/application.properties:ro" \
  -e CBIOPORTAL_DB=cbioportal -e PORTAL_PROPERTIES=/app/application.properties \
  inodb/cbioportal-go
```

A few of the Java backend's server-side properties apply here too:
`cache.endpoint.enabled` and `cache.endpoint.api-key` (the `DELETE /api/cache`
endpoint, which drops the metadata cache), `sitemaps` with
`robots.disallow_paths`, `robots.crawl_delay` and
`robots.disallow_user_agents` (`/robots.txt`, `/sitemap_index.xml`,
`/sitemap_study.xml`), and `feature.study.export` (study download as
`/export/study/<id>.zip`).

Such files also hold the Java backend's own settings (`spring.datasource.*`,
`db.*`, `security.*`, …) that this server does not use. They are
ignored, and the startup log line lists which keys took effect and which were
ignored — names only, never values, which can contain passwords.
