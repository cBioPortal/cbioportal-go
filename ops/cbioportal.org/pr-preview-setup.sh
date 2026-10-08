#!/usr/bin/env bash
# Builds the databases of a cbioportal-go pull request's preview
# (pr-<N>.gopreview.cbioportal.org) with the PR's own importer, and reports
# the result as a Check Run and a Deployment on the PR.
#
#   PR_NUMBER=42 HEAD_SHA=<sha> ops/cbioportal.org/pr-preview-setup.sh
#
# The data comes from the "Preview Configuration" of the PR description
# (.github/pull_request_template.md):
#
#   preview-data: prod             (the default) zero-copy clone of the live
#                                  production database, with mirror-prod.sh
#   preview-data: datahub-pr 2372  reference data plus the studies that
#                                  cBioPortal/datahub#2372 changes, at its head
#   preview-data: studies          reference data plus the studies listed
#                                  under preview-studies, from datahub master
#   preview-studies:               for datahub-pr: only these of its studies
#   - public/msk_impact_2017
#
# The databases are ${DB_PREFIX}<N>, its _raw and its _ref. A build is
# recorded in <raw>._preview_setup with the PR commit and the configuration
# (for datahub-pr, including the datahub PR's head), so the script runs on a
# schedule and rebuilds only when either changes; a failed build is retried
# MAX_ATTEMPTS times. Each build starts from empty databases, so schema
# changes of the PR apply. $WORKDIR/state.env gets REBUILT=1 after a
# successful build, for the step that restarts the preview's API.
#
# Environment (and datahub-lib.sh's, and mirror-prod.sh's MGMT_DB,
# SRC_PREFIX, PORTAL_DATABASE and SRC_DB for `prod`):
#   PR_NUMBER            cbioportal-go pull request (required)
#   HEAD_SHA             the commit of this image; when the PR head has moved
#                        on, the run stops and leaves it to the next image
#   PR_BODY              the PR description, instead of reading it from GitHub
#   CLICKHOUSE_URL / CLICKHOUSE_USER / CLICKHOUSE_PASSWORD
#   DB_PREFIX            (dev_cbioportal_go_pr)
#   PREVIEW_URL          (https://pr-<N>.gopreview.cbioportal.org)
#   GO_REPO              (cBioPortal/cbioportal-go)
#   DATAHUB_REF          datahub branch for `studies` (master)
#   FORCE                1: rebuild even when up to date
#   MAX_ATTEMPTS         (2)
#   WORKDIR              scratch space for the checkout (/tmp/pr-preview)
#   LOG_URL              link to this run's logs
set -uo pipefail

: "${PR_NUMBER:?set PR_NUMBER}"
[[ $PR_NUMBER =~ ^[0-9]+$ ]] || { echo "PR_NUMBER must be a number" >&2; exit 2; }
HEAD_SHA=${HEAD_SHA:-}
DB_PREFIX=${DB_PREFIX:-dev_cbioportal_go_pr}
DB=${DB_PREFIX}${PR_NUMBER} RAW_DB=${DB_PREFIX}${PR_NUMBER}_raw REF_DB=${DB_PREFIX}${PR_NUMBER}_ref
PREVIEW_URL=${PREVIEW_URL:-https://pr-$PR_NUMBER.gopreview.cbioportal.org}
GO_REPO=${GO_REPO:-cBioPortal/cbioportal-go}
DATAHUB_REF=${DATAHUB_REF:-master}
MAX_ATTEMPTS=${MAX_ATTEMPTS:-2}
WORKDIR=${WORKDIR:-/tmp/pr-preview}
LOG_URL=${LOG_URL:-$PREVIEW_URL}
REPO="$WORKDIR/datahub"
SETUP_LOG="\`$RAW_DB\`.\`_preview_setup\`"
HERE=$(dirname "$0")
# shellcheck source=datahub-lib.sh
. "$HERE/datahub-lib.sh"

mkdir -p "$WORKDIR"
SUMMARY="$WORKDIR/summary.md"
: > "$SUMMARY"
echo REBUILT=0 > "$WORKDIR/state.env"

# ---- The PR and its configuration ----
if [ -z "${PR_BODY+set}" ]; then
  PR_INFO=$(gh_get "/repos/$GO_REPO/pulls/$PR_NUMBER") || { log "cannot read $GO_REPO#$PR_NUMBER"; exit 1; }
  PR_BODY=$(jq -r '.body // ""' <<<"$PR_INFO")
  PR_HEAD=$(jq -r '.head.sha' <<<"$PR_INFO")
  HEAD_SHA=${HEAD_SHA:-$PR_HEAD}
  if [ "$PR_HEAD" != "$HEAD_SHA" ]; then
    log "PR head is $PR_HEAD, this run is for $HEAD_SHA; leaving it to the newer image"
    exit 0
  fi
fi
: "${HEAD_SHA:?set HEAD_SHA}"

# Configuration lines start in column 0; HTML comments (the template's
# explanations) are ignored.
BODY=$(tr -d '\r' <<<"$PR_BODY" | awk '
  { line = $0; out = "" }
  { while (1) {
      if (incomment) { e = index(line, "-->"); if (!e) { line = ""; break } line = substr(line, e + 3); incomment = 0 }
      s = index(line, "<!--"); if (!s) { out = out line; break }
      out = out substr(line, 1, s - 1); line = substr(line, s + 4); incomment = 1
    }
    print out }')
DATA=$(sed -n 's/^preview-data:[[:space:]]*//p' <<<"$BODY" | head -1 | sed 's/[[:space:]]*#.*//; s/[[:space:]]*$//')
LIST=$(body_list preview-studies <<<"$BODY" | sort -u)
read -r MODE ARG EXTRA <<<"${DATA:-prod}"
CONFIG_ERROR=""
case $MODE in
  prod) [ -z "$ARG" ] || CONFIG_ERROR="\`preview-data: prod\` takes no argument" ;;
  datahub-pr) [[ ${ARG:-} =~ ^[0-9]+$ ]] || CONFIG_ERROR="\`preview-data: datahub-pr\` needs a datahub PR number" ;;
  studies) [ -n "$LIST" ] || CONFIG_ERROR="\`preview-data: studies\` needs a \`preview-studies:\` list" ;;
  *) CONFIG_ERROR="unknown \`preview-data: $MODE\` (prod, datahub-pr <N> or studies)" ;;
esac
[ -z "${EXTRA:-}" ] || CONFIG_ERROR="unexpected \`$EXTRA\` in \`preview-data: $DATA\`"
while IFS= read -r p; do
  [ -z "$p" ] || [[ $p =~ ^[A-Za-z0-9_][A-Za-z0-9_./-]*$ && $p != *..* ]] || CONFIG_ERROR="invalid study path \`$p\`"
done <<<"$LIST"
CONFIG="$MODE${ARG:+ $ARG}"
[ "$MODE" = prod ] || CONFIG+="${LIST:+ [$(paste -sd, <<<"$LIST")]}"

DH_INFO=""
if [ -z "$CONFIG_ERROR" ] && [ "$MODE" = datahub-pr ]; then
  DH_INFO=$(gh_get "/repos/$DATAHUB/pulls/$ARG") || { log "cannot read $DATAHUB#$ARG"; exit 1; }
  DH_HEAD=$(jq -r '.head.sha' <<<"$DH_INFO")
  CONFIG+=" @$DH_HEAD"
fi
log "PR #$PR_NUMBER at $HEAD_SHA: $CONFIG${CONFIG_ERROR:+ ($CONFIG_ERROR)}"

# ---- Up to date? ----
for d in "$DB" "$RAW_DB" "$REF_DB"; do
  ch "CREATE DATABASE IF NOT EXISTS \`$d\`" >/dev/null || { log "cannot create $d"; exit 1; }
done
LOG_DDL="CREATE TABLE IF NOT EXISTS $SETUP_LOG (sha String, config String, conclusion String, finished DateTime DEFAULT now()) ENGINE = MergeTree ORDER BY finished"
ch "$LOG_DDL" >/dev/null || exit 1
key="sha = '$(sql_lit "$HEAD_SHA")' AND config = '$(sql_lit "$CONFIG")'"
IFS=$'\t' read -r built failed < <(ch "SELECT countIf(conclusion != 'failure'), countIf(conclusion = 'failure') FROM $SETUP_LOG WHERE $key FORMAT TSV")
if [ "${FORCE:-}" != 1 ]; then
  if [ "${built:-0}" -gt 0 ]; then log "up to date"; exit 0; fi
  if [ "${failed:-0}" -ge "$MAX_ATTEMPTS" ]; then log "failed $failed times already; push a commit or change the configuration to retry"; exit 0; fi
fi
PREVIOUS_LOG=$(ch "SELECT sha, config, conclusion, finished FROM $SETUP_LOG FORMAT TSV")

# ---- GitHub status ----
CHECK_ID="" DEPLOY_ID=""
CHECK_ID=$(gh_api POST "/repos/$GO_REPO/check-runs" -d "$(jq -n --arg sha "$HEAD_SHA" --arg log "$LOG_URL" --arg cfg "$CONFIG" \
  '{name:"Preview", head_sha:$sha, status:"in_progress", details_url:$log,
    output:{title:"Building the preview databases...", summary:"Data: `\($cfg)`\n\n[Logs](\($log))"}}')" | jq -r '.id // empty')
DEPLOY_ID=$(gh_api POST "/repos/$GO_REPO/deployments" -d "$(jq -n --arg sha "$HEAD_SHA" --arg env "pr-$PR_NUMBER-preview" \
  '{ref:$sha, environment:$env, auto_merge:false, required_contexts:[], transient_environment:true,
    description:"Preview on gopreview.cbioportal.org"}')" | jq -r '.id // empty')
if [ -n "$DEPLOY_ID" ]; then
  gh_api POST "/repos/$GO_REPO/deployments/$DEPLOY_ID/statuses" -d "$(jq -n --arg env "$PREVIEW_URL/" --arg log "$LOG_URL" \
    '{state:"in_progress", environment_url:$env, log_url:$log}')" >/dev/null || true
fi
[ -n "$CHECK_ID" ] && log "posted check run $CHECK_ID, deployment $DEPLOY_ID"

# finish records the build, completes the Check Run and Deployment, and
# exits (non-zero on failure).
finish() {
  local concl=$1 title=$2 state=failure
  # mirror-prod.sh empties the raw database, the setup log included.
  ch "$LOG_DDL" >/dev/null
  if [ -n "$PREVIOUS_LOG" ] && [ "$(ch "SELECT count() FROM $SETUP_LOG FORMAT TSV")" = 0 ]; then
    ch "INSERT INTO $SETUP_LOG FORMAT TSV
$PREVIOUS_LOG" >/dev/null
  fi
  ch "INSERT INTO $SETUP_LOG (sha, config, conclusion) VALUES ('$(sql_lit "$HEAD_SHA")', '$(sql_lit "$CONFIG")', '$concl')" >/dev/null \
    || log "WARN: could not record the build"
  printf '\nData: `%s`\n\n[Preview](%s) · [Logs](%s)\n' "$CONFIG" "$PREVIEW_URL" "$LOG_URL" >> "$SUMMARY"
  log "result: $concl: $title"
  cat "$SUMMARY" >&2
  if [ -n "$CHECK_ID" ]; then
    gh_api PATCH "/repos/$GO_REPO/check-runs/$CHECK_ID" -d "$(jq -n --arg c "$concl" --arg t "$title" \
      --arg s "$(cat "$SUMMARY")" --arg u "$([ "$concl" = failure ] && echo "$LOG_URL" || echo "$PREVIEW_URL/")" \
      '{status:"completed", conclusion:$c, details_url:$u, output:{title:$t, summary:$s}}')" >/dev/null \
      || log "WARN: could not update check run $CHECK_ID"
  fi
  [ "$concl" = failure ] || state=success
  if [ -n "$DEPLOY_ID" ]; then
    gh_api POST "/repos/$GO_REPO/deployments/$DEPLOY_ID/statuses" -d "$(jq -n --arg s "$state" \
      --arg env "$PREVIEW_URL/" --arg log "$LOG_URL" '{state:$s, environment_url:$env, log_url:$log}')" >/dev/null \
      || log "WARN: could not update deployment $DEPLOY_ID"
  fi
  [ "$state" = success ] || exit 1
  echo REBUILT=1 > "$WORKDIR/state.env"
  exit 0
}
fail() { printf -- '- :x: %s\n' "$1" >> "$SUMMARY"; finish failure "$1"; }

[ -z "$CONFIG_ERROR" ] || fail "Invalid Preview Configuration: $CONFIG_ERROR"

# Drops every table and view of the three databases but the setup log.
empty_databases() {
  local d obj
  for d in "$DB" "$RAW_DB" "$REF_DB"; do
    for obj in $(ch "SELECT name FROM system.tables WHERE database = '$d' AND engine = 'View' FORMAT TSV"); do
      ch "DROP VIEW IF EXISTS \`$d\`.\`$obj\`" >/dev/null || return 1
    done
    for obj in $(ch "SELECT name FROM system.tables WHERE database = '$d' AND engine != 'View' AND NOT (database = '$RAW_DB' AND name = '_preview_setup') FORMAT TSV"); do
      ch "DROP TABLE IF EXISTS \`$d\`.\`$obj\` SYNC" >/dev/null || return 1
    done
  done
}

# ---- prod: a clone of production ----
if [ "$MODE" = prod ]; then
  if TARGET_DB=$DB WORKDIR="$WORKDIR/mirror" "$HERE/mirror-prod.sh"; then
    printf -- '- :white_check_mark: production: %s samples\n' "$(ch "SELECT count() FROM \`$DB\`.sample_derived FORMAT TSV")" >> "$SUMMARY"
    finish success "Production data"
  fi
  fail "Cloning production failed (see logs)"
fi

# ---- datahub-pr / studies: studies imported from datahub ----
if [ "$MODE" = datahub-pr ]; then
  STUDIES=$(datahub_pr_files "$ARG" | study_dirs) || fail "Could not list the files of $DATAHUB#$ARG"
  [ -z "$LIST" ] || STUDIES=$(comm -12 <(sort -u <<<"$STUDIES") <(printf '%s\n' "$LIST"))
  REF="pull/$ARG/head"
  HEAD_LFS_URL="$(jq -r '.head.repo.clone_url // empty' <<<"$DH_INFO")"
  HEAD_LFS_URL="${HEAD_LFS_URL:-$DATAHUB_GIT}/info/lfs"
else
  STUDIES=$LIST
  REF=$DATAHUB_REF
fi
[ -n "$STUDIES" ] || fail "No studies to import"
log "studies: $(tr '\n' ' ' <<<"$STUDIES")"

datahub_fetch "$REF" <<<"$STUDIES" && datahub_checkout || fail "Could not check out $DATAHUB $REF"
log "datahub $REF at $FETCHED"
empty_databases || fail "Could not empty the preview databases"
load_seed "$REF_DB" || fail "Loading the reference data failed"
load_gene_panels "$RAW_DB" || fail "Loading the shared gene panels failed"

OK=0 FAILED=0
for dir in $STUDIES; do
  log "study $dir"
  if [ ! -f "$REPO/$dir/meta_study.txt" ]; then
    printf -- '- :heavy_minus_sign: `%s`: no meta_study.txt, skipped\n' "$dir" >> "$SUMMARY"
    continue
  fi
  sid=$(study_id "$dir")
  start=$(date +%s)
  if lfs_pull "$dir" && "$IMPORT" ingest "$REPO/$dir" --db "$RAW_DB"; then
    OK=$((OK + 1))
    printf -- '- :white_check_mark: [%s](%s/study/summary?id=%s) (%ss)\n' "$sid" "$PREVIEW_URL" "$sid" "$(($(date +%s) - start))" >> "$SUMMARY"
  else
    FAILED=$((FAILED + 1))
    [ -n "$sid" ] && "$IMPORT" remove-study "$sid" --db "$RAW_DB"
    printf -- '- :x: `%s`: import failed (see logs)\n' "${sid:-$dir}" >> "$SUMMARY"
  fi
  rm -rf "${REPO:?}/$dir"
done
rm -rf "$REPO"

log "transform"
"$IMPORT" transform --raw-db "$RAW_DB" --db "$DB" --ref-db "$REF_DB" || fail "Transform failed (see logs)"
if [ "$OK" -eq 0 ]; then
  fail "No study imported"
elif [ "$FAILED" -gt 0 ]; then
  finish neutral "Imported $OK of $((OK + FAILED)) studies"
fi
finish success "Imported $OK studies"
