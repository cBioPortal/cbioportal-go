# cbioportal.org operations

This folder holds the scripts and settings the cBioPortal team uses to run
its own servers: go.cbioportal.org and hub.cbioportal.org. They only work
with the team's own setup. They need production's ClickHouse databases and
import status, datahub's pull requests, and the team's GitHub App.

To run cbioportal-go yourself, see [deploy/](../../deploy/) and
[Deployment](../../docs/deployment.md) instead.

| File | What it is for |
|---|---|
| `public-portal.properties`, `public-portal-frontend-config.json` | The portal settings of www.cbioportal.org. go.cbioportal.org and the other servers that mirror production use them too, so they look and behave like www. |
| `mirror-prod.sh` | An hourly job. After each new import on production, it rebuilds go.cbioportal.org's databases as a copy (a "mirror") of production. |
| `hub-import.sh` | The import job for hub.cbioportal.org. It runs for each datahub pull request labeled `preview` and loads the studies that pull request changes. |
| `pr-preview-setup.sh`, `pr-preview-teardown.sh` | Create and remove the databases for previews of cbioportal-go pull requests, at pr-&lt;N&gt;.gopreview.cbioportal.org. |
| `datahub-lib.sh` | Shared helpers for the scripts above: talking to ClickHouse, signing in as the GitHub App, and checking out datahub. |

The container image copies these files into `/app/deploy/`, next to the
files from `deploy/`.
