# Deploying cbioportal-go

This folder holds tools that help you run cbioportal-go on your own
ClickHouse database. See [Deployment](../docs/deployment.md) for the
container image, and [Configuration](../docs/configuration.md) for its
settings.

| File | What it is for |
|---|---|
| `publish-local-studies.sh` | Copies studies you imported into a local ClickHouse over to another ClickHouse (for example ClickHouse Cloud). There it loads the reference data and builds the portal database. Run it without arguments to see how to use it. |

The cBioPortal team's own deployment scripts are in
[ops/cbioportal.org/](../ops/cbioportal.org/).
