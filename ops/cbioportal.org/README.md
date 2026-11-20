# cbioportal.org operations

This folder holds the code and settings the cBioPortal team uses to run its
own servers: go.cbioportal.org, hub.cbioportal.org with its datahub pull
request previews, and the cbioportal-go pull request previews. They only work with the team's own setup: they need
production's ClickHouse databases, datahub's pull requests and the team's
GitHub App.

To run cbioportal-go yourself, see [deploy/](../../deploy/) and
[Deployment](../../docs/deployment.md) instead. To import studies into your
own portal, use `cbioportal-import import-study` and `fetch-datahub`.

## Contents

- [Files](#files)
- [Settings](#settings)

## Files

The jobs are Go code (`cmd/cbioportal-ops`, `internal/`). The `.sh` files
are the paths the cluster's job manifests run; each just calls a command.
The image copies them, and the settings files, into `/app/deploy/`.

| File | Runs | What it is for |
|---|---|---|
| `hub-import.sh` | `cbioportal-ops hub import-pr` | The import job of a datahub pull request labelled `preview`. It loads the studies that pull request changes into the pull request's own preview, at datahub-pr-&lt;N&gt;.gopreview.cbioportal.org. |
| | `cbioportal-ops hub sync-master` | The job that keeps hub.cbioportal.org at datahub's `master` branch. It runs every minute and imports only when `master` has moved. |
| | `cbioportal-ops hub teardown <N>`, `hub gc` | Removes the databases of one datahub pull request's preview, or of every such preview whose pull request is closed or unlabelled. With `--dry-run`, it only logs what it would remove. |
| `pr-preview-setup.sh` | `cbioportal-ops preview setup` | Builds the databases of a cbioportal-go pull request preview, at pr-&lt;N&gt;.gopreview.cbioportal.org. |
| `pr-preview-teardown.sh` | `cbioportal-ops preview teardown <N>`, `preview gc` (`--gc`) | Removes the databases of one preview, or of every preview whose pull request is closed or unlabelled. With `--dry-run`, it only logs what it would remove. |
| `mirror-prod.sh` | `cbioportal-rewrite-test java-v7-to-go mirror` | An hourly job. After each new import on production, it rebuilds go.cbioportal.org's databases as a copy (a "mirror") of production. It belongs to the move from Java, see [rewrite/](../../rewrite/). |
| `public-portal.properties`, `public-portal-frontend-config.json` | | The portal settings of www.cbioportal.org. go.cbioportal.org and the other servers that mirror production use them too, so they look and behave like www. |

## Settings

All jobs read ClickHouse's address from `CLICKHOUSE_URL`, `CLICKHOUSE_USER`
and `CLICKHOUSE_PASSWORD`, and the GitHub App from `GH_CLIENT_ID`,
`GH_INSTALLATION_ID` and `GH_APP_KEY` (default `/etc/gh-app/key.pem`).
Without the App, GitHub is read anonymously and nothing is posted.
`GITHUB_API` (default `https://api.github.com`) points them at another
GitHub API, `DATAHUB_GIT` and `DATAHUB_LFS_URL` at another datahub.

**`hub sync-master`**

| Setting | Default | Meaning |
|---|---|---|
| `HUB_DB` | `dev_cbioportal_hub_go` | the hub's portal database; `HUB_RAW_DB` and `HUB_REF_DB` default to it plus `_raw` and `_ref` |
| `DATAHUB_BRANCH` | `master` | the datahub branch the hub follows |
| `RETRY_AFTER` | 1800 | seconds to wait before trying again a commit whose import failed |
| `LOCK_WAIT`, `LOCK_STALE`, `LOCK_POLL` | 7200, 600, 30 | seconds to wait for another import, without a heartbeat before a lock is taken over, between checks |
| `WORKDIR` | `/tmp/hub-sync` | scratch space for the checkout |

Each run asks for `master`'s head commit and stops at once when it is the
last commit imported (`<raw>._hub_master_sync`). Otherwise it imports every
study that changed since then in one import at the head commit, removes
the studies deleted from `master`, and records the head only when the
import succeeded.

**`hub import-pr`**

| Setting | Default | Meaning |
|---|---|---|
| `PR_NUMBER` | (required) | the datahub pull request |
| `HEAD_SHA` | the PR's head | the commit this job is for; a job for an older commit stops |
| `HUB_PR_DB_PREFIX` | `dev_cbioportal_go_prdatahub` | the preview's databases are `<prefix><N>`, `_raw` and `_ref` |
| `HUB_PR_BASE` | `https://datahub-pr-<N>.gopreview.cbioportal.org` | the preview's address, for links; `<N>` is the pull request |
| `HUB_DB` | `dev_cbioportal_hub_go` | the hub's databases, which `hub-preview: pr-studies-plus-hub` copies; `HUB_RAW_DB` and `HUB_REF_DB` default to it plus `_raw` and `_ref` |
| `HUB_BASE` | `https://hub.cbioportal.org` | the hub's address |
| `LOCK_WAIT`, `LOCK_STALE`, `LOCK_POLL` | 7200, 600, 30 | seconds to wait for another import, without a heartbeat before a lock is taken over, between checks |
| `WORKDIR` | `/tmp/hub-import` | scratch space for the checkout |
| `LOG_URL` | `HUB_PR_BASE` | link to the job's logs |
| `HUB_API` | `HUB_BASE` | the API the validator's reference data (cancer types, genes, gene panels) is read from |
| `VALIDATOR` | `/opt/cbioportal-core/scripts/importer/validateData.py` | cbioportal-core's validator; `off` imports without validating |
| `PYTHON` | `python3` | the validator's Python |
| `VALIDATE_TIMEOUT` | 3600 | seconds one study's validation may take |

Each study is validated in the background, starting when it is imported.
The import lock is released when the import is done. The job then waits for
the validations before its final report. Studies are imported whatever the
result. The Check Run passes when every study was imported; validation
errors are counted in its title (datahub's CircleCI fails on them). Its summary gives each
study's error and warning counts, and its text lists the messages (see
[docs/deployment.md](../../docs/deployment.md#hubcbioportalorg)).

A `hub-preview: pr-studies-plus-hub` line in the pull request description
starts the preview from a copy of the hub; the default, `hub-preview:
pr-studies`, holds only the pull request's studies. A list of
`- public/<study>` lines right below it limits the import to those
studies. A commit that
was imported already in the same mode is not imported again
(`<raw>._hub_import_log`).

**`hub teardown`, `gc`** read `HUB_PR_DB_PREFIX` and `PREVIEW_LABEL`
(default `preview`), the label a pull request needs to keep its preview.

**`preview setup`, `teardown`, `gc`**

| Setting | Default | Meaning |
|---|---|---|
| `PR_NUMBER` | (required for setup) | the cbioportal-go pull request |
| `HEAD_SHA` | the PR's head | the commit of this image; when the PR has moved on, setup leaves it to the newer image |
| `PR_BODY` | read from GitHub | the PR description |
| `DB_PREFIX` | `dev_cbioportal_go_pr` | the databases are `<prefix><N>`, `_raw` and `_ref` |
| `PREVIEW_URL` | `https://pr-<N>.gopreview.cbioportal.org` | the preview's address |
| `GO_REPO` | `cBioPortal/cbioportal-go` | the repository of the pull requests |
| `DATAHUB_REF` | `master` | the datahub branch for `preview-data: studies` |
| `FORCE` | | `1`: rebuild even when up to date |
| `MAX_ATTEMPTS` | 2 | failed builds of one commit and configuration before setup stops retrying |
| `PREVIEW_LABEL` | `preview` | gc keeps open pull requests with this label |
| `WORKDIR` | `/tmp/pr-preview` | scratch space; setup writes `state.env` (`REBUILT=1` after a build) here |
| `LOG_URL` | `PREVIEW_URL` | link to the job's logs |

The data comes from the "Preview Configuration" of the pull request
description: `prod`, `datahub-pr <N>` or `studies` (see
[PR previews](../../docs/deployment.md#pr-previews)). `prod` runs the mirror
with `TARGET_DB` set, so its settings (`SRC_DB`, `MGMT_DB`, `SRC_PREFIX`,
`PORTAL_DATABASE`) apply too.

**`mirror`**: run `cbioportal-rewrite-test java-v7-to-go mirror -h`.
