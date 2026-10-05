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
  load-genesets      load gene sets and their hierarchy into the reference database
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
cbioportal-import load-genesets <genesets.gmt> --version msigdb_7.5.1 \
  [--supp <supp-genesets.txt>] [--hierarchy <tree.yaml>] --db cbioportal_ref
cbioportal-import load-gene-panels <panels-dir> --db cbioportal_raw
cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref
```

`load-genesets` replaces the gene sets (GSVA's reference) as cbioportal-core's
`importGenesetData.pl --new-version` and `importGenesetHierarchy.pl` do; the
hierarchy file is the importer's YAML tree (nested `Category:` keys with
`Gene sets:` lists). Without it the API names gene sets after their ids and
lists them under one hierarchy node.

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
| `-clickhouse-log-comment` | `CLICKHOUSE_LOG_COMMENT` | | tag every ClickHouse query with this `log_comment`, to tell deployments apart in `system.query_log` |
| `-db` | `CBIOPORTAL_DB` | `cbioportal` | database built by `transform` |
| `-raw-db` | `CBIOPORTAL_RAW_DB` | `cbioportal_raw` | staging database written by `ingest` |
| `-ref-db` | `CBIOPORTAL_REF_DB` | `cbioportal_ref` | reference database written by `load-reference` |
| `-frontend-dir` | `FRONTEND_DIR` | | serve this frontend build for every non-`/api/` path (single origin) |
| `-frontend-url` | `FRONTEND_URL` | | reverse-proxy every non-`/api/` path to this frontend dev server instead |
| `-oncokb-url` | `ONCOKB_URL` | `https://public.api.oncokb.org/api/v1` | OncoKB API behind the frontend's `/proxy/...` endpoint |
| `-site-banner`, `-site-banner-link` | `SITE_BANNER`, `SITE_BANNER_LINK` | | notice bar (and link) at the top of every portal page, e.g. for a preview deployment |
| `-session-store` | `SESSION_STORE_FILE` | | persist sessions (virtual studies, groups, custom data) to this JSON file; unset keeps them in memory. Not used with a session service, see [Sessions](#sessions) |
| `-auth-enabled` | `AUTH_ENABLED` | `false` | require HS256 bearer tokens (not with `dat.method=oauth2`) |
| `-auth-default-user` | `AUTH_DEFAULT_USER` | | single-user development login for requests without a token (not with `authenticate=oauth2`) |
| `-metrics-addr` | `METRICS_ADDR` | | serve Prometheus metrics at `/metrics` on this address (e.g. `:9090`), apart from the API |
| `-access-log` | `ACCESS_LOG` | `false` | write one JSON access log line per request to stdout |
| `-portal-properties` | `PORTAL_PROPERTIES` | | portal properties file, see below |

Secrets have no flag, because command-line arguments are visible to every user
of the machine through the process list (`ps`):

| Environment only | Meaning |
|---|---|
| `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` | ClickHouse credentials (unset: the default user); also used by `cbioportal-import` |
| `ONCOKB_TOKEN` | OncoKB API token sent by the `/proxy/...` endpoint |
| `AUTH_JWT_SECRET` | HS256 secret bearer tokens are verified with |
| `PUBLISHER_API_KEY` | key required by `/api/public_virtual_studies` publish/unpublish (or the `session.endpoint.publisher-api-key` property) |
| `SESSION_SERVICE_URL`, `SESSION_SERVICE_USER`, `SESSION_SERVICE_PASSWORD` | cBioPortal session service and its basic authentication when the `session.service.*` properties are unset, see [Sessions](#sessions) |
| `CBIOPORTAL_PPROF_ADDR` | serve `net/http/pprof` on this address, e.g. `127.0.0.1:6060` |

```bash
cbioportal-api -db cbioportal -frontend-dir frontend/dist
PORT=8080 CBIOPORTAL_DB=cbioportal FRONTEND_DIR=frontend/dist cbioportal-api   # the same
```

At startup the server logs its version and the effective settings:

```
sessions: session service http://session-service:5000/api/sessions/my_portal/
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

**Secrets from the environment.** Values may contain Spring placeholders,
`${NAME}` or `${NAME:default}`, resolved at startup as Spring Boot does: from
the arguments, then the environment (`NAME` as written, then upper-cased with
`.` and `-` as `_`), then the file. Secrets such as OAuth2 client secrets and
the Redis password can so come from environment variables (in Kubernetes,
from a Secret) while the arguments or the image's properties file carry only
the placeholder — out of the image, the process list and the logs:

```bash
cbioportal-api --spring.security.oauth2.client.registration.google.clientSecret='${GOOGLE_PLUS_CONSUMER_SECRET}'
```

A placeholder that is not set and has no default stops the server at
startup with an error naming the property and the placeholder (never a
value). Kubernetes expands only `$(NAME)` in container arguments, so
`${NAME}` reaches the server unchanged.

**Reusing a cbioportal-docker-compose setup.** Point the server at the
properties file the docker-compose setup already has — `config/application.properties`
in current versions (mounted into the Java container as `application.properties`),
`config/portal.properties` in older ones:

```bash
cbioportal-api -db cbioportal --portal-properties=../cbioportal-docker-compose/config/application.properties

docker run --rm --network host \
  -v "$PWD/config/application.properties:/app/application.properties:ro" \
  -e CBIOPORTAL_DB=cbioportal -e PORTAL_PROPERTIES=/app/application.properties \
  cbioportal/cbioportal-go
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

## Login and study authorization

By default the portal is open: every study is readable and nobody logs in.
With `authenticate=oauth2` users log in through an OpenID Connect provider
(Keycloak in cBioPortal deployments), configured with the same properties as
the Java portal's Spring Security OAuth2 login:

```properties
authenticate=oauth2
spring.security.oauth2.client.registration.keycloak.client-id=cbioportal_oauth2
spring.security.oauth2.client.registration.keycloak.client-secret=<secret>
spring.security.oauth2.client.registration.keycloak.client-authentication-method=client_secret_post
spring.security.oauth2.client.registration.keycloak.scope=openid,email,roles
spring.security.oauth2.client.provider.keycloak.issuer-uri=https://keycloak.example.org/auth/realms/cbio
spring.security.oauth2.client.provider.keycloak.user-name-attribute=email
# where the provider puts the user's roles (default shown)
spring.security.oauth2.client.jwt-roles-path=resource_access::cbioportal::roles
```

Keep the client secret out of the command line: in the properties file, or
in the environment through a placeholder
(`client-secret=${KEYCLOAK_CLIENT_SECRET}`, see
[Portal properties](#portal-properties)).
`redirect-uri` defaults to `{baseUrl}/login/oauth2/code/{registrationId}`; the
provider endpoints are discovered from `issuer-uri` (or set
`authorization-uri`, `token-uri`, `user-info-uri` and `jwk-set-uri`).
Property names bind as in Spring (`clientId`, `client_id` and `client-id`
alike). `{baseUrl}` is the origin the browser uses: behind a
TLS-terminating proxy or ingress, the proxy must send `X-Forwarded-Proto`
(and `X-Forwarded-Host` unless it passes the `Host` header on).

- Pages redirect to the provider's login and return to the page asked for
  (a form posted before the login is re-posted). `/api/**` requests without
  a session get `401`. `/api/health` and `/api/public_virtual_studies` stay
  open. `/logout` ends the session at the portal and the provider.
- Every registration with a `client-id` is offered: `/login` goes straight
  to the provider when there is one (unless
  `security.force_redirect_on_one_idp=false`) and lists them otherwise.
- A user's roles name the studies they may read, as in the Java portal: a
  study id, one of the study's `groups`, `ALL`, `ALL_TCGA` or
  `ALL_NCI_TARGET`. With `filter_groups_by_appname=true` (the default) only
  roles prefixed `<app.name>:` count. `always_show_study_group` (e.g.
  `PUBLIC`) is granted to everyone.
- Lists of studies, profiles, sample lists, patients, samples and clinical
  attributes leave unreadable studies out
  (`skin.home_page.show_unauthorized_studies=true` lists them on the home
  page as not readable). Requests naming an unreadable study get `403`.
- Login sessions end after 30 minutes of inactivity. They are kept in the
  server's memory (a restart logs everyone out), or in Redis, see
  [Login sessions in Redis](#login-sessions-in-redis).

Other `authenticate` values (`saml`, `saml_plus_basic`) and
`authorization=true` (users and their authorities from the database) are
not supported and stop the server at startup rather than serve the portal
open.

### Optional sign-in (optional_oauth2)

`authenticate=optional_oauth2` keeps the portal open, as on
www.cbioportal.org: anonymous requests are served exactly as with
`authenticate=false`, and signing in is optional. Signed-in users save
virtual studies, groups and page settings to their account in the session
store. The registrations are configured as for `oauth2`; Google needs only
its client id and secret, since Spring's built-in Google provider settings
apply:

```properties
authenticate=optional_oauth2
authorization=false
security.method_authorization_enabled=false
spring.security.oauth2.client.registration.google.clientId=${GOOGLE_PLUS_CONSUMER_KEY}
spring.security.oauth2.client.registration.google.clientSecret=${GOOGLE_PLUS_CONSUMER_SECRET}
spring.security.oauth2.client.provider.google.user-name-attribute=email
```

- A registration (or its `provider`) named `google` defaults to Google's
  endpoints (`https://accounts.google.com/o/oauth2/v2/auth`,
  `https://www.googleapis.com/oauth2/v4/token`, the JWKs at
  `https://www.googleapis.com/oauth2/v3/certs`, userinfo at
  `https://www.googleapis.com/oauth2/v3/userinfo`), issuer
  `https://accounts.google.com`, scopes `openid,profile,email`, the user named
  by `sub`, and the redirect URI `{baseUrl}/login/oauth2/code/google`, which
  must be registered with Google (e.g.
  `https://go.cbioportal.org/login/oauth2/code/google`). Properties that are
  set override these. GitHub and Facebook, which have no OpenID Connect, are
  not supported.
- `/config_service` reports `authenticationMethod: optional_oauth2`, so the
  frontend shows its sign-in button, and the signed-in user in
  `user_display_name` / `user_email_address` (`anonymousUser` otherwise).
  The button opens `/login` in a window; after the login the provider
  returns to `/login/oauth2/code/{registrationId}`, which sends the window to
  `/` — the frontend then closes it and reloads its configuration.
- `/logout` ends the session and returns to `/` (the provider's session is
  kept, as in Java).
- Signing in grants no study access: every study stays open to everyone.
  With `security.method_authorization_enabled=true` study authorization
  applies as with `oauth2`, to signed-in users by their roles and to
  anonymous ones by `always_show_study_group` alone.
- `dat.method=oauth2` needs `authenticate=oauth2`.

### Login sessions in Redis

With Spring Session's Redis properties login sessions (and logins in
progress) are kept in Redis, so they survive a restart and are shared by
every replica:

```properties
spring.session.store-type=redis
spring.data.redis.host=redis.example.org
spring.data.redis.port=6379
spring.data.redis.password=${REDIS_SESSION_PASSWORD}
# optional: spring.data.redis.username, .database, .ssl.enabled=true,
# .timeout (default 5s), or spring.data.redis.url=rediss://user:pass@host:6380/0
```

The keys are `cbioportal-go:login:…`, JSON with a 30-minute expiry renewed
by use; Spring Session's own (Java-serialized) sessions are left alone, so a
Redis can be shared with Java portals but logins are not. Without
`spring.session.store-type=redis` sessions stay in memory. The startup log
names the store (`login sessions: redis host:6379`), never the password.
When Redis cannot be reached, `optional_oauth2` serves requests anonymously
and `oauth2` answers `503`; both log the error.

### Data access tokens

With `dat.method=oauth2` (and `authenticate=oauth2`) logged-in users download
a data access token for API clients from the portal's "Data Access Token"
button, as in the Java portal. The token is the provider's offline (refresh)
token:

```properties
dat.method=oauth2
dat.oauth2.clientId=cbioportal_oauth2
dat.oauth2.clientSecret=<secret>
dat.oauth2.issuer=https://keycloak.example.org/auth/realms/cbio
dat.oauth2.accessTokenUri=https://keycloak.example.org/auth/realms/cbio/protocol/openid-connect/token
dat.oauth2.userAuthorizationUri=https://keycloak.example.org/auth/realms/cbio/protocol/openid-connect/auth
dat.oauth2.jwkUrl=https://keycloak.example.org/auth/realms/cbio/protocol/openid-connect/certs
dat.oauth2.redirectUri=https://cbioportal.example.org/api/data-access-token/oauth2
# where the provider puts the user's roles in access tokens (default shown)
dat.oauth2.jwtRolesPath=resource_access::cbioportal::roles
# optional: only users with this authority may download a token
dat.filter_user_role=ROLE_<role>
```

- `GET /api/data-access-token` redirects to the provider asking for scope
  `openid offline_access` (the client must allow `offline_access`, and
  `redirectUri` must be one of its redirect URIs). The provider returns to
  `/api/data-access-token/oauth2`, which downloads
  `cbioportal_data_access_token.txt` (`token: <offline token>`). Unlike
  the Java portal, the round trip carries an OAuth2 `state` bound to the
  browser by a 10-minute cookie: a callback without the cookie and its
  state, or a second use of them, gets `401`.
  `dat.filter_user_role` is matched against `ROLE_<role>` for each of the
  user's roles; others get `401`.
- An `/api/**` request with an `Authorization: Bearer <token>` header is
  authenticated by the token alone: the server trades it for an access token
  at `accessTokenUri` (refresh-token grant) and checks that token's signature
  (`jwkUrl`), issuer and expiry. The user is its `sub` claim, the roles are at
  `jwtRolesPath`, and study authorization applies as for a session. A missing,
  revoked or invalid token gets `401`, on the open API paths too. Verified
  tokens are cached (by hash, at most 1024) until 30 seconds before the access
  token expires, so revoking a token at the provider takes effect within an
  access token's lifetime.
- `/api/data-access-tokens*` (token listing and revocation) answer `501`;
  tokens are revoked at the provider (e.g. Keycloak's account console,
  "Applications" → offline access).
- `dat.method=oauth2` without `authenticate=oauth2`, together with
  `AUTH_ENABLED`, or other `dat.method` values (`uuid`, `jwt`) stop the server
  at startup. `dat.ttl_seconds`, `dat.unauth_users` and the `uuid`/`jwt`
  settings apply to those methods only.

```bash
curl -H "Authorization: Bearer $(sed -n 's/^token: //p' cbioportal_data_access_token.txt)" \
  https://cbioportal.example.org/api/studies
```

## Sessions

Virtual studies, groups, comparison and results-view sessions, page
settings, custom data and gene lists (`/api/session/…`,
`/api/public_virtual_studies`) are kept in one of two stores:

- **Built-in** (default): in the server's memory, or in the JSON file
  `SESSION_STORE_FILE`. It serves a single process.
- **cBioPortal's [session service](https://github.com/cBioPortal/session-service)**,
  configured with the Java portal's properties:

  ```properties
  session.service.url=http://session-service:5000/api/sessions/my_portal/
  # only when the session service requires basic authentication
  session.service.user=user
  session.service.password=pass
  ```

  Sessions are then shared by every replica of this server and by Java
  portals using the same session service and portal instance (`my_portal`
  above), so existing links, saved virtual studies and settings carry over.
  Without a properties file the environment variables `SESSION_SERVICE_URL`,
  `SESSION_SERVICE_USER` and `SESSION_SERVICE_PASSWORD` set the same;
  `SESSION_STORE_FILE` is then not used. The startup log names the store
  (`sessions: session service http://…`), never the password.

With the session service, requests go to it as the Java backend's
`SessionServiceController` sends them:

- The service generates the ids and returns the id of an existing session
  for identical data. Sessions are served with the service's `source` (the
  portal instance) and without `checksum`; the built-in store's source is
  `cbioportal`.
- The per-user lists (saved virtual studies, groups and custom data of the
  study, gene lists, page settings) query the user name as Java does: a
  case-insensitive match anywhere in `data.users` (`data.owner` for
  settings), so a user `ann` is also served studies shared with
  `joanne@example.org`. The built-in store matches whole names.
- When the service fails or cannot be reached (30 seconds per request), the
  endpoints answer as Java's: `500` with Spring's error body, a bare `500`
  for `GET /api/session/{type}/{id}` and the user's virtual studies, and a
  bare `400` for `/api/session/settings/fetch`. An unknown id is a `404`.
  Failures are logged without credentials.

Publishing virtual studies (`/api/public_virtual_studies/{id}`) requires the
key `session.endpoint.publisher-api-key`, or `PUBLISHER_API_KEY`, in the
`X-PUBLISHER-API-KEY` header, with either store.

## Metrics and logs

With `-metrics-addr`, `/metrics` on that address serves Prometheus text
format (scraped by Prometheus or Datadog's OpenMetrics check); it is a
separate listener, so the metrics are never public through the API's
ingress. Requests are labelled by the route pattern they match (e.g.
`GET /api/studies/{studyId}`), which keeps the number of series bounded.

| Metric | Meaning |
|---|---|
| `cbioportal_http_requests_total{route,code}` | requests by route and status code |
| `cbioportal_http_request_duration_seconds{route}` | request time histogram |
| `cbioportal_http_requests_in_flight` | requests being served |
| `cbioportal_clickhouse_queries_total{outcome}` | ClickHouse requests (`ok`, `error`, `transport`) |
| `cbioportal_clickhouse_query_duration_seconds` | ClickHouse time until the result is read |
| `cbioportal_clickhouse_read_rows_total`, `…_read_bytes_total` | what ClickHouse reports reading |
| `cbioportal_metadata_cache_lookups_total{result}` | metadata cache hits and misses |
| `cbioportal_logins_total{outcome}` | OAuth2 logins |
| `cbioportal_data_access_token_checks_total{result}` | data access tokens: cached, refreshed, rejected |
| `go_*`, `process_*` | Go runtime and process (memory, GC, goroutines, CPU, file descriptors) |

With `-access-log`, each request is logged to stdout as JSON (`method`,
`route`, `path`, `status`, `bytes`, `duration_ms`, `remote`, and `user` when
logged in) for log collection. `-clickhouse-log-comment` tags the
deployment's queries in ClickHouse's own `system.query_log`.

In Kubernetes, Datadog collects the metrics with pod annotations such as:

```yaml
ad.datadoghq.com/cbioportal-go.checks: |
  {"openmetrics": {"instances": [{"openmetrics_endpoint": "http://%%host%%:9090/metrics",
    "namespace": "cbioportal_go", "metrics": [".*"]}]}}
```
