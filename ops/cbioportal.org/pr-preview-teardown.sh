#!/usr/bin/env bash
# Removes the databases of cbioportal-go pull request previews.
#
#   pr-preview-teardown.sh <N>   drop ${DB_PREFIX}<N>, its _raw and its _ref,
#                                and mark the PR's preview Deployment inactive
#   pr-preview-teardown.sh --gc  the same for every preview database whose PR
#                                is closed or no longer labelled `preview`; a
#                                PR that cannot be read is left alone
#
# Environment (and datahub-lib.sh's): CLICKHOUSE_URL / CLICKHOUSE_USER /
# CLICKHOUSE_PASSWORD, DB_PREFIX (dev_cbioportal_go_pr), GO_REPO
# (cBioPortal/cbioportal-go), PREVIEW_LABEL (preview), WORKDIR
# (/tmp/pr-preview). The GitHub App is needed to read a private GO_REPO.
set -uo pipefail

DB_PREFIX=${DB_PREFIX:-dev_cbioportal_go_pr}
GO_REPO=${GO_REPO:-cBioPortal/cbioportal-go}
PREVIEW_LABEL=${PREVIEW_LABEL:-preview}
WORKDIR=${WORKDIR:-/tmp/pr-preview}
# shellcheck source=datahub-lib.sh
. "$(dirname "$0")/datahub-lib.sh"
mkdir -p "$WORKDIR"

teardown() {
  local n=$1 d id rc=0
  for d in "${DB_PREFIX}$n" "${DB_PREFIX}${n}_raw" "${DB_PREFIX}${n}_ref"; do
    ch "DROP DATABASE IF EXISTS \`$d\` SYNC" >/dev/null && log "dropped $d" || rc=1
  done
  for id in $(gh_get "/repos/$GO_REPO/deployments?environment=pr-$n-preview&per_page=100" | jq -r '.[].id' 2>/dev/null); do
    gh_api POST "/repos/$GO_REPO/deployments/$id/statuses" -d '{"state":"inactive"}' >/dev/null || true
  done
  return $rc
}

case ${1:-} in
  --gc)
    rc=0
    for n in $(ch "SELECT DISTINCT extract(name, '^$DB_PREFIX([0-9]+)(?:_raw|_ref)?\$') AS n FROM system.databases WHERE n != '' ORDER BY toUInt32(n) FORMAT TSV"); do
      if ! pr=$(gh_get "/repos/$GO_REPO/pulls/$n"); then
        log "#$n: cannot read the PR, left alone"
        continue
      fi
      state=$(jq -r '.state' <<<"$pr")
      labelled=$(jq --arg l "$PREVIEW_LABEL" '[.labels[].name] | index($l) != null' <<<"$pr")
      if [ "$state" = open ] && [ "$labelled" = true ]; then
        log "#$n: open with label $PREVIEW_LABEL, kept"
      else
        log "#$n: $state, label $PREVIEW_LABEL: $labelled; removing"
        teardown "$n" || rc=1
      fi
    done
    exit $rc
    ;;
  *)
    [[ ${1:-} =~ ^[0-9]+$ ]] || { echo "usage: $0 <pr-number> | --gc" >&2; exit 2; }
    teardown "$1"
    ;;
esac
