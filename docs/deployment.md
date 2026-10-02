# Deployment

## Container image

`docker.io/inodb/cbioportal-go` (`latest` and `sha-<7-char commit>`,
linux/amd64 and linux/arm64) is built and pushed by
`.github/workflows/docker.yml` on every push to `main`. The
[Dockerfile](../Dockerfile) builds the frontend once on the build platform and
cross-compiles the Go binaries, so the arm64 image needs no emulation for the
heavy steps.

The image (alpine, non-root uid 10001, `WORKDIR /app`, port 8080) contains
`/usr/local/bin/cbioportal-api` (the default command),
`/usr/local/bin/cbioportal-import`, the built frontend in `/app/frontend` and
the deployment scripts in `/app/deploy`.

```bash
docker build -t inodb/cbioportal-go:dev --build-arg VERSION="$(git rev-parse --short HEAD) (dev)" .
docker run --rm --network host inodb/cbioportal-go:dev -db cbioportal
```

The API container is configured with the settings in
[Configuration](configuration.md): `PORT=8080`, `FRONTEND_DIR=/app/frontend`,
`CLICKHOUSE_URL` / `CLICKHOUSE_USER` / `CLICKHOUSE_PASSWORD`, `CBIOPORTAL_DB` /
`CBIOPORTAL_RAW_DB` / `CBIOPORTAL_REF_DB`, and `SESSION_STORE_FILE` on a
persistent volume. Health check: `GET /health`.

## go.cbioportal.org

The preview runs in the cBioPortal production cluster: manifests in
[knowledgesystems-k8s-deployment](https://github.com/knowledgesystems/knowledgesystems-k8s-deployment)
(`apps/cbioportal-go/`), secrets in portal-configuration
(`secrets/cbioportal-go/`). It serves ClickHouse Cloud databases in two colors,
`dev_cbioportal_public_go_{blue,green}` with their `_raw` and `_ref`
companions; the ConfigMap `cbioportal-go-active` says which color is live.

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

### Planned: mirroring production

A daily job will clone the live production color
(`cbioportal_public_<color>`, read from
`publicdb_update_management_database.update_status`) with zero-copy
`CREATE TABLE … CLONE AS` into the inactive color, build the portal database
from production's schema, and flip `cbioportal-go-active` — the same pattern as
the LibreChat clone job. The preview then serves exactly www.cbioportal.org's
data.
