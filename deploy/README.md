# Deploying cbioportal-go

Generic tooling for running cbioportal-go on your own ClickHouse; see
[Deployment](../docs/deployment.md) for the container image and
[Configuration](../docs/configuration.md) for its settings.

| File | Purpose |
|---|---|
| `publish-local-studies.sh` | Copy studies imported into a local ClickHouse to another ClickHouse and build the portal database there |

The cBioPortal team's own deployment scripts are in [ops/cbioportal.org/](../ops/cbioportal.org/).
