# cbioportal.org operations

Scripts and configuration for the cBioPortal team's own deployments
(go.cbioportal.org, hub.cbioportal.org). They depend on that infrastructure:
production's ClickHouse databases and update status, datahub's pull requests
and the team's GitHub App. To run cbioportal-go yourself, see [deploy/](../../deploy/)
and [Deployment](../../docs/deployment.md).

| File | Used by |
|---|---|
| `public-portal.properties`, `public-portal-frontend-config.json` | www.cbioportal.org's portal settings, used by go.cbioportal.org and its mirror deployments |
| `mirror-prod.sh` | Hourly job that rebuilds go.cbioportal.org's databases after each production import |
| `hub-import.sh` | Per-PR import Job of hub.cbioportal.org (datahub `preview` PRs) |
| `pr-preview-setup.sh`, `pr-preview-teardown.sh` | Databases of the cbioportal-go PR previews, pr-&lt;N&gt;.gopreview.cbioportal.org |
| `datahub-lib.sh` | ClickHouse, GitHub App and datahub checkout helpers shared by the scripts above |

The image ships these files in `/app/deploy/`, next to `deploy/`'s.
