# Configuration

This page explains how to set up go's two programs. `cbioportal-import`
loads study files into ClickHouse, the database go runs on.
`cbioportal-api` is the web server that answers API requests and serves the
cBioPortal website. The page covers their commands and settings, login, saved
sessions, and monitoring.

## Contents

- [cbioportal-import](#cbioportal-import)
- [cbioportal-api](#cbioportal-api)
  - [Limits on bulk requests](#limits-on-bulk-requests)
  - [Website paths](#website-paths)
- [Portal properties](#portal-properties)
- [Login and study authorization](#login-and-study-authorization)
  - [Optional sign-in (optional_oauth2)](#optional-sign-in-optional_oauth2)
  - [Login sessions in Redis](#login-sessions-in-redis)
  - [Data access tokens](#data-access-tokens)
- [Sessions](#sessions)
- [Metrics and logs](#metrics-and-logs)

Both programs print all their options with `-h`. To see which build you
are running, use `-version` (or `cbioportal-import version`). It prints
something like `cbioportal-api 6b38ed6 (2026-10-02T09:15:00Z)`. That is the
git commit the program was built from and the time of that commit. A
`+dirty` after the commit means the build had changes that were not
committed.

## cbioportal-import

The importer works in steps. `ingest` copies a study's files as they are
into a staging database (the "raw" database). `transform` then builds the
tables the API reads, in the portal database. Reference data, such as
genes and cancer types, goes into a third database (the "ref" database).

```
$ cbioportal-import help
Commands:
  plan               list a study's data files and the staging tables they load into
  ingest             stage a study's data files in the raw database
  remove-study       delete studies' staged rows from the raw database
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
cbioportal-import remove-study <study-id>... --db cbioportal_raw     # then transform
cbioportal-import ingest-case-lists <study-dir>... --db cbioportal_raw
cbioportal-import load-reference <seed.sql.gz> --db cbioportal_ref
cbioportal-import load-genesets <genesets.gmt> --version msigdb_7.5.1 \
  [--supp <supp-genesets.txt>] [--hierarchy <tree.yaml>] --db cbioportal_ref
cbioportal-import load-gene-panels <panels-dir> --db cbioportal_raw
cbioportal-import transform --raw-db cbioportal_raw --db cbioportal --ref-db cbioportal_ref
```

`load-genesets` replaces all gene sets. Gene sets are the reference data
for GSVA profiles. It does the same job as two scripts of the Java importer
(cbioportal-core): `importGenesetData.pl --new-version` and
`importGenesetHierarchy.pl`. The hierarchy file is the same YAML tree the
Java importer reads: nested `Category:` keys, each with a `Gene sets:` list.
Without a hierarchy file, the API names each gene set by its id and puts
them all under one node.

`cbioportal-import help <command>` (or `<command> -h`) shows a command's
options, their defaults, and examples. Options can come before or after the
other arguments. You can write them as `-db x`, `--db x` or `--db=x`.

Every command that connects to ClickHouse takes `--url`. Its default is the
`CLICKHOUSE_URL` environment variable, or `http://localhost:18123` if that
is not set. The ClickHouse user name and password come only from the
environment variables `CLICKHOUSE_USER` and `CLICKHOUSE_PASSWORD`.

The importer exits with one of three codes:

| Code | Meaning |
|---|---|
| 0 | The command worked. |
| 1 | The command failed. It prints `cbioportal-import: <command>: <error>` to stderr. |
| 2 | The command line was wrong. |

## cbioportal-api

Each server setting can come from a command-line flag or from an
environment variable. A flag wins over the environment variable, and the
environment variable wins over the default. The container image and the
Kubernetes setup use environment variables. On the command line, flags are
usually easier.

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `-port` | `PORT` | `8080` | The port to listen on. The server listens on all network interfaces (0.0.0.0). |
| `-clickhouse-url` | `CLICKHOUSE_URL` | `http://localhost:18123` | The address of ClickHouse's HTTP interface. For ClickHouse Cloud, use `https://<host>:8443`. |
| `-clickhouse-log-comment` | `CLICKHOUSE_LOG_COMMENT` | | A label added to every query. ClickHouse keeps it in its query log (`system.query_log`), so you can tell which deployment sent a query. |
| `-db` | `CBIOPORTAL_DB` | `cbioportal` | The portal database, built by `transform`. |
| `-raw-db` | `CBIOPORTAL_RAW_DB` | `cbioportal_raw` | The staging database, written by `ingest`. |
| `-ref-db` | `CBIOPORTAL_REF_DB` | `cbioportal_ref` | The reference database, written by `load-reference`. |
| `-clickhouse-bulk-max-memory` | `CLICKHOUSE_BULK_MAX_MEMORY` | `10GiB` | The memory one ClickHouse query of a bulk endpoint may use. `0` means no limit. See [Limits on bulk requests](#limits-on-bulk-requests). |
| `-clickhouse-bulk-max-time` | `CLICKHOUSE_BULK_MAX_TIME` | `10m` | How long one query of a bulk endpoint may run, including the time the client takes to read the answer. A number alone means seconds. `0` means no limit. |
| `-clickhouse-bulk-sort-spill` | `CLICKHOUSE_BULK_SORT_SPILL` | `1GiB` | The memory a sort in a bulk query may use before ClickHouse continues it on disk. `0` keeps ClickHouse's own setting. |
| `-frontend-dir` | `FRONTEND_DIR` | | A folder with a built frontend, so the website and the API share one address. The server serves its `index.html` as the portal's pages and its files under `/reactapp/` and `/images/`. See [Website paths](#website-paths). |
| `-frontend-url` | `FRONTEND_URL` | | The address of a frontend development server. The server passes the portal's pages, and every path it does not serve itself, on to it. Use this instead of `-frontend-dir`. |
| `-oncokb-url` | `ONCOKB_URL` | `https://public.api.oncokb.org/api/v1` | The OncoKB API. The frontend reaches it through the server's `/proxy/...` path. |
| `-site-banner`, `-site-banner-link` | `SITE_BANNER`, `SITE_BANNER_LINK` | | A notice (and an optional link) shown at the top of every page. This is useful for a preview site. |
| `-session-store` | `SESSION_STORE_FILE` | | A JSON file to save sessions in (virtual studies, groups, custom data). If it is not set, sessions live in memory and are lost on restart. It is not used with a session service, see [Sessions](#sessions). |
| `-auth-enabled` | `AUTH_ENABLED` | `false` | Requires a bearer token signed with HS256 on every request. It cannot be combined with `dat.method=oauth2`. |
| `-auth-default-user` | `AUTH_DEFAULT_USER` | | A single user name for development. Requests without a token are treated as this user. It cannot be combined with `authenticate=oauth2`. |
| `-metrics-addr` | `METRICS_ADDR` | | An address, such as `:9090`, where the server serves Prometheus metrics at `/metrics`. It is separate from the API's port. |
| `-access-log` | `ACCESS_LOG` | `false` | Writes one JSON line per request to stdout. |
| `-portal-properties` | `PORTAL_PROPERTIES` | | A portal properties file, see [Portal properties](#portal-properties). |

Secrets have no flag. Every user on a machine can see the command lines of
running programs (for example with `ps`), so secrets come only from
environment variables:

| Environment only | Meaning |
|---|---|
| `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` | The ClickHouse user name and password. If they are not set, ClickHouse's default user is used. `cbioportal-import` reads them too. |
| `ONCOKB_TOKEN` | The OncoKB API token that the `/proxy/...` path sends to OncoKB. The `oncokb.token` property sets it too, but the variable wins. With either one set, `/config_service` reports `oncoKbTokenDefined: true`. |
| `AUTH_JWT_SECRET` | The HS256 secret used to check bearer tokens. |
| `PUBLISHER_API_KEY` | The key needed to publish or unpublish a virtual study through `/api/public_virtual_studies`. The `session.endpoint.publisher-api-key` property sets it too. |
| `SESSION_SERVICE_URL`, `SESSION_SERVICE_USER`, `SESSION_SERVICE_PASSWORD` | The address of a cBioPortal session service, and its user name and password. They are used when the `session.service.*` properties are not set. See [Sessions](#sessions). |
| `CBIOPORTAL_PPROF_ADDR` | An address, such as `127.0.0.1:6060`, where the server serves Go's profiling pages (`net/http/pprof`). |

These two commands do the same thing:

```bash
cbioportal-api -db cbioportal -frontend-dir frontend/dist
PORT=8080 CBIOPORTAL_DB=cbioportal FRONTEND_DIR=frontend/dist cbioportal-api
```

When it starts, the server logs its version and the settings it uses:

```
sessions: session service http://session-service:5000/api/sessions/my_portal/
cbioportal-api 6b38ed6 (2026-10-02T09:15:00Z) listening on 0.0.0.0:8080 (clickhouse http://localhost:18123, db cbioportal, raw-db cbioportal_raw, ref-db cbioportal_ref)
portal properties: 5 used (authenticate, quick_search.enabled, session.service.url, skin.blurb, skin.title), 3 ignored (not used by this server: db.user, spring.datasource.password, spring.datasource.url)
```

### Limits on bulk requests

Some endpoints can return a whole profile of data at once: molecular data,
discrete copy number, generic assay data, gene panel data, mutations,
copy-number segments, co-expression, the expression and generic assay
enrichments, and the study download. go streams these answers, so the
server's own memory stays small. The cost lands on ClickHouse, which may be
shared with other services. So each query of these endpoints runs with
limits:

| Limit | ClickHouse setting | What happens |
|---|---|---|
| Memory (`10GiB`) | `max_memory_usage` | ClickHouse stops a query that needs more. Grouping (`GROUP BY`) moves to disk at half of it (`max_bytes_before_external_group_by`). |
| Time (`10m`) | `max_execution_time` | ClickHouse stops a query that runs longer. |
| Sort memory (`1GiB`) | `max_bytes_before_external_sort` | A larger sort continues on disk. The answer is the same, only slower. |

If a query stops before any data is sent, the client gets a 400 with a
message, as Java formats errors: `{"message": "This request needs more
database memory than one request may use. Ask for less data at a time: name
the genes (entrezGeneIds) or fewer samples, or page through the result with
pageSize and pageNumber."}`. If it stops after the answer has started, the
server breaks off the connection, so the client sees a broken transfer, not
a shorter list.

The defaults come from a week of the public portal mirror's query log. The
largest everyday queries (one gene across all profiles of a pan-cancer
study) use 7–9 GiB, and none ran longer than 2 minutes. The two that went
past 10 GiB were whole-profile pulls of 15 GiB. One of them pushed a 30 GiB
ClickHouse server to its limit. With the sort on disk, the 15 GiB pull
(124 million values) needs 1.3 GiB.

### Website paths

The server answers the paths outside `/api/` as the Java portal does:

| Path | Answer |
|---|---|
| The portal's pages: `/`, `/index.html`, `/results/*`, `/study/*`, `/patient/*`, `/comparison/*`, `/mutation_mapper`, `/oncoprinter`, `/restore`, `/loading/*`, `/webAPI`, `/datasets`, `/news`, `/visualize`, `/ln`, `/encodedRedirect`, `/index.do`, `/case.do/*`, `/oncotree2genes` | The frontend's page. It includes the portal's configuration, so the browser does not have to ask `/config_service` for it. |
| `/installations`, `/tutorials`, `/oql`, `/donate` | A redirect to the map of installations or to the documentation. |
| `/reactapp/…` | The frontend's code and styles. |
| `/images/…`, `/js/…` | The Java portal's images and scripts, which are built into the server. Other `/images/…` files come from the frontend. |
| Anything else, such as `/about` or `/favicon.ico` | A 404 with Spring's error body. The frontend still shows its own pages, such as About, when you get there by clicking inside the website. |

Every answer also carries the security headers that Spring Security adds in
the Java portal. They tell the browser not to show the site inside a frame
of another site (`X-Frame-Options: DENY`), not to guess file types
(`X-Content-Type-Options: nosniff`), and not to cache answers
(`Cache-Control: no-store`). The frontend's code files are the exception:
browsers may cache them. A request that came in over HTTPS also gets
`Strict-Transport-Security`, which tells the browser to always use HTTPS.
Behind an ingress that ends HTTPS, the server learns this from the
`X-Forwarded-Proto` header.

## Portal properties

Portal properties set how the portal looks and behaves. They use the same
names as the Java portal. Examples are the `skin.*` properties (title,
blurb, logos, footer, example queries, quick-select buttons),
`priority_studies`, `quick_search.enabled` and `oncokb.public_api.url`.
You can pass them in two ways:

- **As arguments.** Any `--key=value` argument whose key is not one of the
  flags above is a portal property. This is how the Java portal is started
  too (`java -jar cbioportal.jar --skin.title=…`), so you can reuse an
  existing deployment's arguments as they are. A property needs both dashes
  and the `=`. If a property has the same name as a flag, put it in the
  file instead.
- **In a properties file**, named by `-portal-properties` or
  `PORTAL_PROPERTIES`. This can be an `application.properties` file or an
  older `portal.properties` file. Both use `key=value` lines.

Arguments win over the file, and the file wins over the built-in defaults.

```bash
cbioportal-api --skin.title="My portal" --quick_search.enabled=true
cbioportal-api --portal-properties=./portal.properties --skin.title="Preview"
```

**Secrets from the environment.** A property value can refer to an
environment variable with `${NAME}`, or `${NAME:default}` to give a
fallback. This works as in Spring Boot, the framework the Java portal uses.
The server looks up `NAME` at startup, in this order:

1. The arguments.
2. The environment. It tries `NAME` as written, then in upper case with
   `.` and `-` turned into `_`.
3. The properties file.

This keeps secrets such as OAuth2 client secrets and the Redis password out
of the image, the process list and the logs. The arguments or the file hold
only the placeholder, and the secret itself comes from an environment
variable (in Kubernetes, from a Secret). For example:

```bash
cbioportal-api --spring.security.oauth2.client.registration.google.clientSecret='${GOOGLE_PLUS_CONSUMER_SECRET}'
```

If a placeholder has no value and no default, the server stops at startup.
The error names the property and the placeholder, never a value.
Kubernetes only fills in `$(NAME)` in container arguments, so `${NAME}`
reaches the server unchanged.

**Reusing a cbioportal-docker-compose setup.** You can point the server at
the properties file your docker-compose setup already has. In current
versions this is `config/application.properties` (the Java container sees
it as `application.properties`). In older versions it is
`config/portal.properties`.

```bash
cbioportal-api -db cbioportal --portal-properties=../cbioportal-docker-compose/config/application.properties

docker run --rm --network host \
  -v "$PWD/config/application.properties:/app/application.properties:ro" \
  -e CBIOPORTAL_DB=cbioportal -e PORTAL_PROPERTIES=/app/application.properties \
  cbioportal/cbioportal-go
```

A few of the Java backend's server properties work here too:

| Properties | What they control |
|---|---|
| `cache.endpoint.enabled`, `cache.endpoint.api-key` | The `DELETE /api/cache` endpoint, which clears the metadata cache. |
| `sitemaps`, `robots.disallow_paths`, `robots.crawl_delay`, `robots.disallow_user_agents` | `/robots.txt`, `/sitemap_index.xml` and `/sitemap_study.xml`. |
| `robots.disallow_all` (go only) | When `true`, `/robots.txt` disallows every crawler and the sitemaps answer 404. Use it on preview sites. |
| `llms_txt.location` | A file served as `/llms.txt`. It can start with `file:`. The server reads it again every 5 minutes. |
| `feature.study.export` | Study downloads at `/export/study/<id>.zip`. |

These files often also hold settings for the Java backend itself, such as
`spring.datasource.*`, `db.*` and `security.*`. go does not use them and
ignores them. The startup log lists which keys were used and which were
ignored. It shows only the names, never the values, since values can be
passwords.

## Login and study authorization

By default the portal is open. Anyone can read every study, and nobody
logs in.

With `authenticate=oauth2`, users must log in through an OpenID Connect
provider. OpenID Connect is a standard way to log in through another
service. cBioPortal deployments usually use Keycloak. You set it up with
the same properties as the Java portal's login (Spring Security OAuth2):

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

Keep the client secret off the command line. Put it in the properties
file, or in an environment variable through a placeholder, such as
`client-secret=${KEYCLOAK_CLIENT_SECRET}` (see
[Portal properties](#portal-properties)).

A few more details:

- `redirect-uri` defaults to `{baseUrl}/login/oauth2/code/{registrationId}`.
- The server finds the provider's addresses from `issuer-uri`. You can also
  set them yourself with `authorization-uri`, `token-uri`, `user-info-uri`
  and `jwk-set-uri`.
- Without `issuer-uri`, the server does not check who issued the login
  token, as in Java. It still checks the token's signature, that it is for
  this client, and that it has not expired. This is how Microsoft sign-in
  for both work and personal accounts is set up: Microsoft's `common`
  addresses issue each token in the name of the user's own organization.
- Property names can be written in any of Spring's styles. `clientId`,
  `client_id` and `client-id` all mean the same.
- `{baseUrl}` is the address the browser uses. If a proxy or ingress in
  front of the server handles HTTPS, it must send the `X-Forwarded-Proto`
  header. It must also send `X-Forwarded-Host`, unless it passes on the
  original `Host` header.

How login works:

- A page sends the user to the provider's login. After logging in, the user
  comes back to the page they asked for. A form posted before the login is
  posted again.
- An `/api/**` request without a login session gets `401`. `/api/health`
  and `/api/public_virtual_studies` stay open to everyone.
- `/logout` ends the session at the portal and at the provider.
- The portal offers every registration that has a `client-id`. If there is
  only one, `/login` goes straight to it, unless
  `security.force_redirect_on_one_idp=false`. If there are several, `/login`
  lists them.

Which studies a user can read:

- A user's roles name the studies they may read, as in the Java portal. A
  role can be a study id, one of the study's `groups`, `ALL`, `ALL_TCGA` or
  `ALL_NCI_TARGET`.
- With `filter_groups_by_appname=true` (the default), only roles that start
  with `<app.name>:` count.
- Everyone gets the group named in `always_show_study_group`, such as
  `PUBLIC`.
- Lists of studies, profiles, sample lists, patients, samples and clinical
  attributes leave out studies the user cannot read. With
  `skin.home_page.show_unauthorized_studies=true`, the home page lists them
  anyway, marked as not readable.
- A request that names a study the user cannot read gets `403`.
- A study's `readPermission` in `/api/studies` also depends on its import
  status, but only when `study_availability.enabled=true`. Then a study
  that is still being imported (status not 1) is not readable. Java's
  main branch applies this rule whatever the setting says. go follows the
  setting, which matches production: production (v7.1.2) ignores the
  status, and every study there has status 0. So with the setting on, go
  behaves like Java's main branch, and with it off, like production.

Login sessions end after 30 minutes without activity. They are kept in the
server's memory, so a restart logs everyone out. They can be kept in Redis
instead, see [Login sessions in Redis](#login-sessions-in-redis).

go does not support the other `authenticate` values (`saml`,
`saml_plus_basic`) or `authorization=true` (users and their permissions
stored in the database). With any of them, the server stops at startup
instead of running an open portal by mistake.

### Optional sign-in (optional_oauth2)

With `authenticate=optional_oauth2`, the portal stays open, as on
www.cbioportal.org. Users who do not sign in get exactly what they would get
with `authenticate=false`. Signing in is optional. Signed-in users can save
virtual studies, groups and page settings to their account.

You set up the providers the same way as for `oauth2`. Google needs only a
client id and secret, because Spring's built-in Google settings apply:

```properties
authenticate=optional_oauth2
authorization=false
security.method_authorization_enabled=false
spring.security.oauth2.client.registration.google.clientId=${GOOGLE_PLUS_CONSUMER_KEY}
spring.security.oauth2.client.registration.google.clientSecret=${GOOGLE_PLUS_CONSUMER_SECRET}
spring.security.oauth2.client.provider.google.user-name-attribute=email
```

- A registration (or its `provider`) named `google` gets Google's settings
  by default. Any of these that you set yourself wins:

  | Setting | Default |
  |---|---|
  | Login page | `https://accounts.google.com/o/oauth2/v2/auth` |
  | Token address | `https://www.googleapis.com/oauth2/v4/token` |
  | Signing keys (JWKs) | `https://www.googleapis.com/oauth2/v3/certs` |
  | User info | `https://www.googleapis.com/oauth2/v3/userinfo` |
  | Issuer | `https://accounts.google.com` |
  | Scopes | `openid,profile,email` |
  | User name | the `sub` field |
  | Redirect address | `{baseUrl}/login/oauth2/code/google` |

  You must register the redirect address with Google, for example
  `https://go.cbioportal.org/login/oauth2/code/google`. GitHub and Facebook
  do not support OpenID Connect, so go does not support them.
- `/config_service` reports `authenticationMethod: optional_oauth2`, so the
  frontend shows its sign-in button. It also reports the signed-in user in
  `user_display_name` and `user_email_address`, or `anonymousUser` when
  nobody is signed in.
- The sign-in button opens `/login` in a new window. After the login, the
  provider returns to `/login/oauth2/code/{registrationId}`, which sends the
  window to `/`. The frontend then closes the window and reloads its
  settings.
- `/logout` ends the session and returns to `/`. The user stays logged in
  at the provider, as in Java.
- Signing in does not change which studies a user can read. Every study
  stays open to everyone. With `security.method_authorization_enabled=true`,
  study access works as with `oauth2`: signed-in users by their roles, and
  everyone else by `always_show_study_group` only.
- `dat.method=oauth2` needs `authenticate=oauth2`, so it does not work here.

### Login sessions in Redis

Redis is a fast key-value database. If you set Spring Session's Redis
properties, login sessions (and logins still in progress) are kept in
Redis. Then they survive a restart, and every copy of the server shares
them.

```properties
spring.session.store-type=redis
spring.data.redis.host=redis.example.org
spring.data.redis.port=6379
spring.data.redis.password=${REDIS_SESSION_PASSWORD}
# optional: spring.data.redis.username, .database, .ssl.enabled=true,
# .timeout (default 5s), or spring.data.redis.url=rediss://user:pass@host:6380/0
```

- The keys start with `cbioportal-go:login:`. Each one holds JSON and
  expires after 30 minutes. The time starts over each time the session is
  used.
- go leaves the Java portal's own sessions alone. So go and Java can share
  one Redis, but a login on one does not log you in on the other.
- Without `spring.session.store-type=redis`, sessions stay in memory.
- The startup log names the store, such as `login sessions: redis
  host:6379`. It never shows the password.
- When Redis cannot be reached, `optional_oauth2` serves every request as
  if nobody is signed in, and `oauth2` answers `503`. Both log the error.

### Data access tokens

A data access token lets a script or program use the API as a logged-in
user. With `dat.method=oauth2` (and `authenticate=oauth2`), logged-in users
can download one with the portal's "Data Access Token" button, as in the
Java portal. The token is the provider's offline token, a long-lived token
the server can trade for short-lived ones.

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

**Getting a token.**

- `GET /api/data-access-token` sends the user to the provider and asks for
  the scope `openid offline_access`. The provider's client must allow
  `offline_access`, and `redirectUri` must be one of its allowed redirect
  addresses.
- The provider sends the user back to `/api/data-access-token/oauth2`.
  This downloads the file `cbioportal_data_access_token.txt`, which holds
  `token: <offline token>`.
- go adds a safety check that the Java portal does not have. The round trip
  carries an OAuth2 `state` value, tied to the browser by a cookie that
  lasts 10 minutes. A return without the cookie and its state gets `401`.
  So does a second use of the same ones.
- If `dat.filter_user_role` is set, go compares it with `ROLE_<role>` for
  each of the user's roles. Users without that role get `401`.

**Using a token.**

- An `/api/**` request with an `Authorization: Bearer <token>` header is
  logged in by the token alone.
- The server trades the token for a short-lived access token at
  `accessTokenUri`. It then checks that token's signature (with the keys at
  `jwkUrl`), its issuer and its expiry time.
- The user is the token's `sub` field, and the roles are at `jwtRolesPath`.
  Study access works as for a login session.
- A missing, revoked or invalid token gets `401`, even on paths that are
  otherwise open.
- go remembers checked tokens (at most 1024, stored by hash) until 30
  seconds before their access token expires. So when you revoke a token at
  the provider, it stops working within one access token's lifetime.

**Other rules.**

- `/api/data-access-tokens*` (listing and revoking tokens) answers `501`.
  You revoke tokens at the provider instead, for example in Keycloak's
  account console under "Applications", offline access.
- The server stops at startup in these cases: `dat.method=oauth2` without
  `authenticate=oauth2`, `dat.method=oauth2` together with `AUTH_ENABLED`,
  or any other `dat.method` value (`uuid`, `jwt`).
- `dat.ttl_seconds`, `dat.unauth_users` and the `uuid` and `jwt` settings
  belong to those other methods only.

Example of using a downloaded token:

```bash
curl -H "Authorization: Bearer $(sed -n 's/^token: //p' cbioportal_data_access_token.txt)" \
  https://cbioportal.example.org/api/studies
```

## Sessions

Sessions here are things users save: virtual studies, groups, comparison
and results-view sessions, page settings, custom data and gene lists. They
use `/api/session/…` and `/api/public_virtual_studies`. go can keep them in
one of two stores:

- **The built-in store** (the default). It keeps sessions in the server's
  memory, or in the JSON file named by `SESSION_STORE_FILE`. It works for
  a single server process only.
- **cBioPortal's [session service](https://github.com/cBioPortal/session-service)**,
  set up with the same properties as the Java portal:

  ```properties
  session.service.url=http://session-service:5000/api/sessions/my_portal/
  # only when the session service requires basic authentication
  session.service.user=user
  session.service.password=pass
  ```

  Every copy of the server then shares the sessions. So do Java portals that
  use the same session service and the same portal name (`my_portal`
  above). Existing links, saved virtual studies and settings keep working.
  Without a properties file, the environment variables
  `SESSION_SERVICE_URL`, `SESSION_SERVICE_USER` and
  `SESSION_SERVICE_PASSWORD` do the same. `SESSION_STORE_FILE` is then not
  used. The startup log names the store, such as
  `sessions: session service http://…`. It never shows the password.

With the session service, go sends requests to it the same way the Java
backend does (its `SessionServiceController`):

- The service makes up the ids. If someone saves data that is already
  stored, the service returns the id of the existing session.
- Sessions carry the service's `source`, which is the portal name, and no
  `checksum`. In the built-in store, the source is `cbioportal`.
- The lists of a user's own items (saved virtual studies, groups and custom
  data for a study, gene lists, page settings) search for the user name as
  Java does. The search ignores case and matches any part of `data.users`
  (or `data.owner` for settings). So a user named `ann` also sees studies
  shared with `joanne@example.org`. The built-in store matches whole names
  only.
- When the service fails or does not answer within 30 seconds, go answers
  as Java does. Most endpoints answer `500` with Spring's error body.
  `GET /api/session/{type}/{id}` and the user's virtual studies answer a
  bare `500`. `/api/session/settings/fetch` answers a bare `400`. An
  unknown id is a `404`. go logs the failure without the password.

To publish a virtual study (`/api/public_virtual_studies/{id}`), the request
must send a key in the `X-PUBLISHER-API-KEY` header. The key is set by
`session.endpoint.publisher-api-key` or `PUBLISHER_API_KEY`. This applies
to both stores.

## Metrics and logs

Metrics are numbers that monitoring tools collect over time, such as how
many requests the server answered. With `-metrics-addr`, the server serves
them at `/metrics` on that address, in Prometheus's text format.
Prometheus, or Datadog's OpenMetrics check, can collect them. This address
is separate from the API's, so the metrics are never public through the
API's public address.

Each request is labelled with the route it matched, such as
`GET /api/studies/{studyId}`, not the full path. This keeps the number of
different labels small.

| Metric | Meaning |
|---|---|
| `cbioportal_http_requests_total{route,code}` | The number of requests, by route and status code. |
| `cbioportal_http_request_duration_seconds{route}` | How long requests take, as a histogram. |
| `cbioportal_http_requests_in_flight` | The number of requests being answered right now. |
| `cbioportal_clickhouse_queries_total{outcome}` | The number of ClickHouse queries, by outcome: `ok`, `error` (ClickHouse reported an error) or `transport` (the query did not reach ClickHouse or got no answer). |
| `cbioportal_clickhouse_query_duration_seconds` | How long ClickHouse takes until the whole result is read. |
| `cbioportal_clickhouse_read_rows_total`, `…_read_bytes_total` | How many rows and bytes ClickHouse reports reading. |
| `cbioportal_metadata_cache_lookups_total{result}` | How often the metadata cache had the answer (hit) or not (miss). |
| `cbioportal_logins_total{outcome}` | The number of OAuth2 logins, by outcome. |
| `cbioportal_data_access_token_checks_total{result}` | Data access token checks: answered from memory, traded at the provider, or rejected. |
| `go_*`, `process_*` | The Go runtime and the process: memory, garbage collection, goroutines, CPU and open files. |

With `-access-log`, the server writes one JSON line per request to stdout,
for a log collector to pick up. Each line has `method`, `route`, `path`,
`status`, `bytes`, `duration_ms`, `remote`, and `user` when someone is
logged in. `-clickhouse-log-comment` labels the deployment's queries in
ClickHouse's own query log (`system.query_log`).

In Kubernetes, Datadog collects the metrics when the pod has annotations
like these:

```yaml
ad.datadoghq.com/cbioportal-go.checks: |
  {"openmetrics": {"instances": [{"openmetrics_endpoint": "http://%%host%%:9090/metrics",
    "namespace": "cbioportal_go", "metrics": [".*"]}]}}
```
