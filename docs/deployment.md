# Deployment

This page explains how cbioportal-go is packaged and where it runs. It covers
the container image, the public preview at go.cbioportal.org, the hub that
previews new datahub studies, and the per-pull-request previews.

## Contents

- [Container image](#container-image)
- [go.cbioportal.org](#gocbioportalorg)
  - [Sign-in](#sign-in)
  - [Loading studies](#loading-studies)
  - [Mirroring production](#mirroring-production)
- [hub.cbioportal.org](#hubcbioportalorg)
- [PR previews](#pr-previews)

## Container image

The image `docker.io/cbioportal/cbioportal-go` is built and published by
`.github/workflows/docker.yml` on every push to `main`. It runs on both Intel
(linux/amd64) and ARM (linux/arm64) machines.

| Tag | When |
|---|---|
| `latest`, `sha-<7-char commit>` | every push to `main` |
| `pr-<N>`, `sha-<commit>` | pull requests labelled `preview` (see [PR previews](#pr-previews)) |

The [Dockerfile](../Dockerfile) builds the frontend once and compiles the Go
programs for each platform, so the ARM image does not need slow emulation.

What is inside:

| Path | Contents |
|---|---|
| `/usr/local/bin/cbioportal-api` | the API server (the default command) |
| `/usr/local/bin/cbioportal-import` | the importer |
| `/usr/local/bin/cbioportal-ops` | the cBioPortal team's jobs: PR previews and the hub ([ops/cbioportal.org](../ops/cbioportal.org/)) |
| `/usr/local/bin/cbioportal-rewrite-test` | tools for the move from Java cBioPortal, such as the production mirror ([rewrite/](../rewrite/)) |
| `/app/frontend` | the built frontend |
| `/app/deploy` | deployment scripts, which the cluster's jobs run |
| `/opt/cbioportal-core` | cbioportal-core's study validator (`scripts/importer/validateData.py`) and Python 3, for the hub's import job |

The validator is copied from a fixed cbioportal-core commit, the
`CBIOPORTAL_CORE_REF` build argument in the Dockerfile (its commit is also in
`/opt/cbioportal-core/REF`). To update it, change that commit to a newer one
on cbioportal-core's `main` and check that cbioportal-core's
`requirements.txt` asks for nothing new; the image provides requests, PyYAML
and Jinja2 from Alpine, and dsnparse.

The image is based on Alpine Linux, runs as a non-root user (uid 10001) and
listens on port 8080. The build has no `.git` folder, so two build arguments
record the version: `VERSION` is printed by `-version`, and `COMMIT` is shown
as `gitCommitId` in `/api/info`.

```bash
docker build -t cbioportal/cbioportal-go:dev --build-arg VERSION="$(git rev-parse --short HEAD) (dev)" \
  --build-arg COMMIT="$(git rev-parse HEAD)" .
docker run --rm --network host cbioportal/cbioportal-go:dev -db cbioportal
```

The API container uses the settings described in
[Configuration](configuration.md):

- `PORT=8080` and `FRONTEND_DIR=/app/frontend`.
- `CLICKHOUSE_URL`, `CLICKHOUSE_USER` and `CLICKHOUSE_PASSWORD` to reach
  ClickHouse, the database.
- `CBIOPORTAL_DB`, `CBIOPORTAL_RAW_DB` and `CBIOPORTAL_REF_DB`, the three
  databases it reads.
- Saved sessions (virtual studies, shared links): `SESSION_STORE_FILE` on a
  persistent volume. With more than one copy of the server, use cBioPortal's
  session service instead (`SESSION_SERVICE_URL`, see
  [Sessions](configuration.md#sessions)).
- With login turned on (`authenticate=oauth2` or `optional_oauth2`) and more
  than one copy of the server, keep login sessions in Redis
  (`spring.session.store-type=redis`, see
  [Login sessions in Redis](configuration.md#login-sessions-in-redis)).

The health check is `GET /health`.

## go.cbioportal.org

go.cbioportal.org is the public preview of cbioportal-go. It runs in the
cBioPortal production Kubernetes cluster. Its setup files (manifests) are in
[knowledgesystems-k8s-deployment](https://github.com/knowledgesystems/knowledgesystems-k8s-deployment)
under `apps/cbioportal-go/`. Its passwords and keys are in
portal-configuration under `secrets/cbioportal-go/`.

It reads ClickHouse Cloud databases in two copies, blue and green:
`dev_cbioportal_public_go_blue` and `dev_cbioportal_public_go_green`, each
with `_raw` and `_ref` companion databases. One copy serves the site while
the other is rebuilt. The ConfigMap `cbioportal-go-active` says which copy is
live.

### Sign-in

Like www, the preview is open to everyone and also offers Google sign-in
(`authenticate=optional_oauth2`). The secrets come from the Kubernetes Secret
`cbioportal-public-green` as environment variables:
`GOOGLE_PLUS_CONSUMER_KEY`, `GOOGLE_PLUS_CONSUMER_SECRET`,
`REDIS_SESSION_HOST`, `REDIS_SESSION_PORT` and `REDIS_SESSION_PASSWORD`. The
container arguments refer to them with `${…}` placeholders. Kubernetes leaves
these alone, and the server fills them in when it starts:

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

The Google login client must list
`https://go.cbioportal.org/login/oauth2/code/google` as an allowed redirect
address. The ingress (the entry point into the cluster) sends
`X-Forwarded-Proto: https`, so the server knows the site uses HTTPS.

### Loading studies

Use this when a portal should be loaded from study files instead of copied
from production.

To add studies to a portal that is being served, use `import-study`. It
loads the studies and rebuilds the portal in one step, and is safe while
the portal serves: one import runs at a time (a lock table in the raw
database), the new tables are swapped in together at the end, and a study
that fails is taken out again. `fetch-datahub` checks out datahub studies,
with their large files from datahub's own file store:

```bash
cbioportal-import fetch-datahub datahub public/acbc_mskcc_2015
cbioportal-import import-study datahub/public/acbc_mskcc_2015 --db cbioportal \
  --seed datahub/seedDB/seed-cbioportal_hg19_hg38_v2.14.5.sql.gz \
  --gene-panels datahub/reference_data/gene_panels
```

[`deploy/publish-local-studies.sh`](../deploy/publish-local-studies.sh)
copies studies already loaded into a local ClickHouse to another ClickHouse,
then builds the portal database there:

```bash
export CLICKHOUSE_URL=https://<host>:8443 CLICKHOUSE_USER=go_import
read -rs CLICKHOUSE_PASSWORD && export CLICKHOUSE_PASSWORD
deploy/publish-local-studies.sh dev_cbioportal_public_go_blue \
  seed-cbioportal_hg19_hg38_v2.14.5.sql.gz msk_impact_2017 acbc_mskcc_2015
```

For a database name `<prefix>`, the script:

1. copies the studies' loaded rows, plus the shared gene panels, into
   `<prefix>_raw`;
2. loads the reference data (genes, cancer types) from the seed file into
   `<prefix>_ref`;
3. runs `transform` to build `<prefix>`, the database the API reads.

You can run it again safely: a study's old rows are replaced. The three
databases must already exist. The ClickHouse user needs SELECT, INSERT,
ALTER, CREATE TABLE, CREATE VIEW, DROP TABLE, DROP VIEW and TRUNCATE on them.

### Mirroring production

go.cbioportal.org shows exactly the same data as www.cbioportal.org. It does
this by copying production's database every hour, rather than loading study
files itself. This is needed only while go replaces the Java cBioPortal, so
the code is in [rewrite/](../rewrite/). The hourly job runs
[`ops/cbioportal.org/mirror-prod.sh`](../ops/cbioportal.org/mirror-prod.sh),
which calls `cbioportal-rewrite-test java-v7-to-go mirror`. It works like
the LibreChat copy job:

1. It reads which production copy (blue or green) is live and when it was
   last imported, from `publicdb_update_management_database.update_status`.
   It stops without changes while an import is running, or when the live go
   copy was already built from that import. The import it was built from is
   recorded in `<prefix>_<color>_ref._go_mirror_source`.
2. It empties go's inactive copy. Then it clones every table of
   `cbioportal_public_<color>` into `<prefix>_<color>_ref`, except the login
   and password tables. ClickHouse's `CREATE TABLE … CLONE AS` makes this
   copy without duplicating the data on disk, in about 20 s.
3. It builds the portal database from the clone, as this command does:

   ```bash
   cbioportal-import transform --from-prod \
     --ref-db <prefix>_<color>_ref --raw-db <prefix>_<color>_raw --db <prefix>_<color>
   ```

4. It passes the three database names (`CBIOPORTAL_DB`, `CBIOPORTAL_RAW_DB`,
   `CBIOPORTAL_REF_DB`) to the step that switches `cbioportal-go-active` to
   the new copy. The API reads the three databases as they are (`-db`,
   `-raw-db`, `-ref-db`).

#### What `transform --from-prod` builds

`transform --from-prod` starts from production's tables instead of study
files. Like the mirror, it exists only for the move from Java. Those are the Java importer's tables plus the `*_derived` tables
production builds for the study view. From them it builds every table and
view the API reads, with the same shape and meaning as a normal import. It
takes about a minute on ClickHouse Cloud and copies little data.

**Views (no copy).** The largest tables are read in place through views,
including `genetic_alteration_derived` (10 billion rows) and
`generic_assay_data_derived` (3.8 billion). The views also cover:

- samples, clinical data (empty cells left out, as a normal import does),
  genomic events, gene panels per sample and per gene, and the timeline;
- the copy-number levels −1, 0 and 1, and the gene id map (`gene_key_map`);
- in the raw database, the per-gene tables that the molecular-data,
  co-expression and enrichment endpoints read (`cna_matrix`,
  `mrna_expression_matrix`, `methylation_matrix`, `protein_level_matrix`,
  `generic_assay_matrix`), plus `genetic_profile` and the generic assay
  tables.

These views let the API's study, profile and gene filters use the sort order
of the production tables, so lookups stay fast.

**Copied tables.** Smaller data, tens of millions of rows at most, is copied
and sorted to suit the API's filters: studies, profiles, case lists,
patients, samples, clinical data, which samples are in which profile,
mutations, amplifications and deep deletions with their driver annotations,
structural variants, segments, gene panels, GISTIC, MutSig, resources, the
timeline and its attributes, and each profile's samples in the order the data
uses.

The clone also serves as the reference database: genes, gene aliases, cancer
types and reference genomes. A few responses use columns only production has,
when they exist: a study's `public` flag and import date, and a generic-assay
profile's pivot threshold, sort order and patient level.

#### Known differences from www

On the mirror, any difference from www.cbioportal.org comes from the API
code, not from the data. `paritydiff` measures them (see
[Parity with Java](rewrite/parity.md#api-parity-with-wwwcbioportalorg)).

| Where | What happens |
|---|---|
| Study list on www | It also shows resource definitions of other studies that share a resource id (a www bug). |
| `studies/fetch` on www | It reports `allSampleCount: 1` (a www bug). |
| `clinical-events-meta`, `survival-data` on www | They answer HTTP 500. |
| Multi-filter study-view requests | Java attaches one NA count (or bin) to whichever gene or entity it happens to process first, so the result depends on Java's internal order. Panels added to a shared list in `ProfiledCasesCounter` have the same problem. |
| Page boundaries | Where only ClickHouse decides the order, the rows at the edge of a page can differ. This affects the clinical tab's samples when no sort column is chosen (production itself returns different pages), a study's clinical data paged by internal id, and the label of a case list or profile type merged across studies. |

Java returns many lists in import order: studies, patients, clinical data,
segments, clinical events, and sample lists and their members. The mirror
keeps that order by using production's internal ids (`internal_id`,
`seg_id`, `list_internal_id`, `clinical_event_id`).

Expression enrichments of a TCGA PanCancer study take a few seconds, because
`genetic_alteration_derived` is sorted by gene before profile.

## hub.cbioportal.org

The hub shows a preview of the studies in each cBioPortal/datahub pull
request labelled `preview`. It uses the same ClickHouse Cloud service, with
the databases `dev_cbioportal_hub_go`, `_raw` and `_ref`. Its manifests are
in knowledgesystems-k8s-deployment under `apps/cbioportal-hub/`.

For each new commit on such a pull request, Argo CD (the tool that deploys to
the cluster) starts a job that runs
[`ops/cbioportal.org/hub-import.sh`](../ops/cbioportal.org/hub-import.sh),
which calls `cbioportal-ops hub import-pr`. The job:

1. checks out the studies the pull request changes;
2. checks each study with cbioportal-core's validator (`validateData.py`),
   while it is being imported;
3. imports them as `cbioportal-import import-study` does (see
   [Loading studies](#loading-studies));
4. reports a Check Run and a Deployment on the pull request.

**Validation.** The validator compares each study with the hub's reference
data: cancer types, genes, gene sets and gene panels. The job reads these
once from the hub's own API and saves them as files for the validator (its
`-p` option). If the pull request adds or changes gene panel files, those
panels replace the hub's, because the hub loads them only during the import;
their genes are matched by Hugo symbol or Entrez id only. Gene aliases are
not checked, as when the validator asks a portal's API directly.

A study is imported whatever the validator finds, so the preview can always
be looked at. The Check Run shows, for each study, whether validation passed
or how many errors and warnings it found, and lists the messages under the
summary (up to GitHub's size limit; the job's log has the full output). When
any study has validation errors, the Check Run fails, as datahub's own
validation does, while the Deployment still links to the preview. A commit
whose studies all imported is not imported again, even if validation failed.
If the validator is missing or the hub's API cannot be read, the studies are
imported without validation and the summary says so.

Imports from different pull requests wait for each other, using a lock table
in the raw database. If a study fails to load, it is removed. If the
transform fails, the pull request's studies are removed and the hub is
rebuilt without them. Meanwhile the hub keeps serving its previous build.
The settings are listed in [ops/cbioportal.org](../ops/cbioportal.org/). The
job can also run locally against any ClickHouse and GitHub API
(`GITHUB_API`).

## PR previews

A cbioportal-go pull request labelled `preview` gets its own copy of the
portal at `https://pr-<N>.gopreview.cbioportal.org`. It runs the pull
request's image on its own databases (`dev_cbioportal_go_pr<N>`, `_raw` and
`_ref`), built with the pull request's own `ingest` and `transform`. That way
changes to the database layout are tested too.

The "Preview Configuration" section of the pull request description chooses
the data ([template](../.github/pull_request_template.md)):

| `preview-data:` | Data |
|---|---|
| `prod` (default) | a copy of production, made as for the mirror (`cbioportal-rewrite-test java-v7-to-go mirror` with `TARGET_DB`) |
| `datahub-pr <N>` | reference data plus the studies that cBioPortal/datahub#N changes, at its latest commit (limit them with `preview-studies:`) |
| `studies` | reference data plus the datahub master studies listed under `preview-studies:` |

[`ops/cbioportal.org/pr-preview-setup.sh`](../ops/cbioportal.org/pr-preview-setup.sh)
(`cbioportal-ops preview setup`) runs every few minutes in the cluster. It
rebuilds a preview from empty
databases only when the pull request's latest commit or its configuration has
changed. It records what it built in `<raw>._preview_setup`, and reports a
"Preview" Check Run and a Deployment on the pull request.

Closing the pull request or removing the label deletes the preview, and
`pr-preview-teardown.sh` (`cbioportal-ops preview teardown`) drops its
databases. A cleanup job (`cbioportal-ops preview gc`) also removes the
previews of closed or unlabelled pull requests. The manifests are in
knowledgesystems-k8s-deployment under `apps/cbioportal-go-preview/`.

To run the jobs locally:

```bash
make build && export PATH=$PWD/bin:$PATH
PR_NUMBER=9999 HEAD_SHA=abc1234 PR_BODY="$(cat body.md)" WORKDIR=/tmp/pr \
  cbioportal-ops preview setup     # prod mode: also SRC_DB=<a Java-schema db>
cbioportal-ops preview teardown 9999
```
