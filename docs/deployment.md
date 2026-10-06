# Deployment

## Container image

`docker.io/cbioportal/cbioportal-go` (`latest` and `sha-<7-char commit>`,
linux/amd64 and linux/arm64) is built and pushed by
`.github/workflows/docker.yml` on every push to `main`. The
[Dockerfile](../Dockerfile) builds the frontend once on the build platform and
cross-compiles the Go binaries, so the arm64 image needs no emulation for the
heavy steps.

The image (alpine, non-root uid 10001, `WORKDIR /app`, port 8080) contains
`/usr/local/bin/cbioportal-api` (the default command),
`/usr/local/bin/cbioportal-import`, the built frontend in `/app/frontend` and
the deployment scripts in `/app/deploy`. The build arguments `VERSION` (the
`-version` line) and `COMMIT` (the `gitCommitId` of `/api/info`) stamp the
binaries, as the build context has no `.git`.

```bash
docker build -t cbioportal/cbioportal-go:dev --build-arg VERSION="$(git rev-parse --short HEAD) (dev)" \
  --build-arg COMMIT="$(git rev-parse HEAD)" .
docker run --rm --network host cbioportal/cbioportal-go:dev -db cbioportal
```

The API container is configured with the settings in
[Configuration](configuration.md): `PORT=8080`, `FRONTEND_DIR=/app/frontend`,
`CLICKHOUSE_URL` / `CLICKHOUSE_USER` / `CLICKHOUSE_PASSWORD`, `CBIOPORTAL_DB` /
`CBIOPORTAL_RAW_DB` / `CBIOPORTAL_REF_DB`, and `SESSION_STORE_FILE` on a
persistent volume — or, for more than one replica, cBioPortal's session
service (`SESSION_SERVICE_URL`, see [Sessions](configuration.md#sessions)). Health check: `GET /health`.
With a login (`authenticate=oauth2` or `optional_oauth2`) and more than one
replica, login sessions go to Redis (`spring.session.store-type=redis`, see
[Login sessions in Redis](configuration.md#login-sessions-in-redis)).

## go.cbioportal.org

The preview runs in the cBioPortal production cluster: manifests in
[knowledgesystems-k8s-deployment](https://github.com/knowledgesystems/knowledgesystems-k8s-deployment)
(`apps/cbioportal-go/`), secrets in portal-configuration
(`secrets/cbioportal-go/`). It serves ClickHouse Cloud databases in two colors,
`dev_cbioportal_public_go_{blue,green}` with their `_raw` and `_ref`
companions; the ConfigMap `cbioportal-go-active` says which color is live.

### Sign-in

As www, the preview offers Google sign-in on an open portal
(`authenticate=optional_oauth2`). The secrets come from the Secret
`cbioportal-public-green` as environment variables
(`GOOGLE_PLUS_CONSUMER_KEY`, `GOOGLE_PLUS_CONSUMER_SECRET`,
`REDIS_SESSION_HOST`, `REDIS_SESSION_PORT`, `REDIS_SESSION_PASSWORD`); the
container arguments name them by placeholder, which the server resolves
(Kubernetes leaves `${…}` alone):

```yaml
args:
  - --authenticate=optional_oauth2
  - --authorization=false
  - --security.method_authorization_enabled=false
  - --always_show_study_group=PUBLIC
  - --spring.security.oauth2.client.registration.google.clientId=${GOOGLE_PLUS_CONSUMER_KEY}
  - --spring.security.oauth2.client.registration.google.clientSecret=${GOOGLE_PLUS_CONSUMER_SECRET}
  - --spring.security.oauth2.client.provider.google.user-name-attribute=email
  - --spring.session.store-type=redis
  - --spring.data.redis.host=${REDIS_SESSION_HOST}
  - --spring.data.redis.port=${REDIS_SESSION_PORT}
  - --spring.data.redis.password=${REDIS_SESSION_PASSWORD}
```

The Google client must list
`https://go.cbioportal.org/login/oauth2/code/google` as an authorized
redirect URI; the ingress sends `X-Forwarded-Proto: https`.

### Loading studies

[`deploy/publish-local-studies.sh`](../deploy/publish-local-studies.sh) copies
studies staged in a local ClickHouse to another ClickHouse and builds the
portal database there — the way the preview is loaded today:

```bash
export CLICKHOUSE_URL=https://<host>:8443 CLICKHOUSE_USER=go_import
read -rs CLICKHOUSE_PASSWORD && export CLICKHOUSE_PASSWORD
deploy/publish-local-studies.sh dev_cbioportal_public_go_blue \
  seed-cbioportal_hg19_hg38_v2.14.5.sql.gz msk_impact_2017 acbc_mskcc_2015
```

It copies the studies' staging rows (plus the shared gene panels) into
`<prefix>_raw`, loads the seed into `<prefix>_ref` and runs `transform` into
`<prefix>`. It is safe to re-run: a study's rows are replaced. The three
databases must exist; the user needs SELECT, INSERT, ALTER, CREATE TABLE,
CREATE VIEW, DROP TABLE, DROP VIEW and TRUNCATE on them.

### Mirroring production

[`deploy/mirror-prod.sh`](../deploy/mirror-prod.sh) is the daily job that
makes the preview serve exactly www.cbioportal.org's data, the same pattern as
the LibreChat clone job:

1. read the live production color from
   `publicdb_update_management_database.update_status`;
2. empty the inactive Go color and zero-copy `CREATE TABLE … CLONE AS` every
   table of `cbioportal_public_<color>` (minus auth / credential tables) into
   `<prefix>_<color>_ref` (~20 s);
3. build the portal database from the clone:

   ```bash
   cbioportal-import transform --from-prod \
     --ref-db <prefix>_<color>_ref --raw-db <prefix>_<color>_raw --db <prefix>_<color>
   ```

4. hand `CBIOPORTAL_DB` / `CBIOPORTAL_RAW_DB` / `CBIOPORTAL_REF_DB` to the
   step that flips `cbioportal-go-active`. The API serves the three databases
   unchanged (`-db`, `-raw-db`, `-ref-db`).

`transform --from-prod` reads nothing from staged studies; it takes
production's schema (cbioportal-core's tables migrated to ClickHouse plus the
`*_derived` tables) and builds every object the API reads, with the shapes
and meaning the datahub transform gives them. It takes about a minute on
ClickHouse Cloud and copies little:

- **Views over the clone (no copy):** `sample_derived`,
  `clinical_data_derived` (blank cells left out, as the datahub transform
  stores only values), `genomic_event_derived`, `genetic_alteration_derived`
  (10 billion rows), `generic_assay_data_derived` (3.8 billion),
  `sample_to_gene_panel_derived`, `gene_panel_to_gene_derived`,
  `clinical_event_derived`, `clinical_event_data_derived`; the discrete
  copy-number levels −1/0/1 of `cna`; `gene_key_map`; and in the raw database
  the per-gene matrices the molecular-data, co-expression and enrichment
  endpoints read (`cna_matrix`, `mrna_expression_matrix`,
  `methylation_matrix`, `protein_level_matrix` over
  genetic_alteration_derived; `generic_assay_matrix` over
  generic_assay_data_derived) plus `genetic_profile` and the generic assay
  entity tables. The API's study / profile / gene filters reach the sort keys
  of the derived tables through these views.
- **Materialized from the normalized tables** (tens of millions of rows at
  most, sorted for the API's filters): studies, profiles, case lists,
  patients, samples, long-form clinical data, `sample_profile`, mutations of
  the mutation and uncalled-mutation profiles (from `mutation_derived`),
  AMP/HOMDEL events with their driver annotations
  (`cna_event`), structural variants, segments, gene panels, GISTIC, MutSig,
  resources, the timeline (`clinical_event` with its attributes), and each
  matrix profile's samples in matrix order (`profile_sample`, from
  `genetic_profile_samples`).

The clone also serves as the reference database (genes, aliases, cancer
types, reference genomes). A few API responses read production-only columns
when the portal database has them: a study's `public` flag and import date,
and a generic-assay profile's pivot threshold, sort order and patient level.

Known differences from www.cbioportal.org on the mirror are those of the API
itself, not of the data; `cmd/paritydiff` measures them (see
[Testing](testing.md#api-parity-with-wwwcbioportalorg)). www's study list
attaches resource definitions of other studies that share a resource id; its
legacy `studies/fetch` reports `allSampleCount: 1`; `clinical-events-meta`
and `survival-data` answer HTTP 500 there. Java returns many lists in import
(internal id) order — studies, patients, clinical data, segments, clinical
events, sample lists and their members — which the mirror keeps through
production's internal ids (`internal_id`, `seg_id`, `list_internal_id`,
`clinical_event_id`). Where Java attaches a single NA count (or bin) to an
arbitrary gene or entity of a multi-filter study-view request, or appends
panels to a shared list in `ProfiledCasesCounter`, results depend on Java's
iteration order. Expression
enrichments of a TCGA PanCancer study take a few seconds, since
genetic_alteration_derived is sorted by gene before profile.
