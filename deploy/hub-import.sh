#!/usr/bin/env bash
# Imports the studies a cBioPortal/datahub pull request changes into the
# hub.cbioportal.org databases and reports the result on the pull request:
# the per-PR Job of the `datahub-pr-import-set` ApplicationSet.
#
#   PR_NUMBER=2327 HEAD_SHA=<sha> deploy/hub-import.sh
#
# Steps: read the PR's changed files (public/<study>, crdc/gdc/<study>,
# reference_data/gene_panels), narrowed by a `hub-import-only:` list in the
# PR body; check out those paths of the PR head; then, holding the hub's
# import lock, `ingest` each study (its LFS files pulled just before) and run
# one `transform` of the hub database. The transform publishes all its tables
# at once, so a failed run leaves the hub as it was. A study that fails to
# ingest is removed from the hub; when the transform fails, this run's
# studies are removed and the hub rebuilt without them. Reference data (seed
# genes, cancer types) and the shared gene panels are loaded into an empty
# hub first.
#
# Environment:
#   PR_NUMBER            datahub pull request (required)
#   HEAD_SHA             the commit this Job is for; a Job whose commit is no
#                        longer the PR head stops without importing
#   CLICKHOUSE_URL / CLICKHOUSE_USER / CLICKHOUSE_PASSWORD
#   HUB_DB               portal database (dev_cbioportal_hub_go); the raw and
#                        reference databases are ${HUB_DB}_raw and ${HUB_DB}_ref
#   HUB_BASE             public URL of the hub (https://hub.cbioportal.org)
#   WORKDIR              scratch space for the checkout (/tmp/hub-import)
#   LOCK_WAIT            seconds to wait for another import (7200)
#   LOCK_STALE           seconds without a heartbeat after which a lock is
#                        taken over (600)
#   GH_CLIENT_ID, GH_INSTALLATION_ID, GH_APP_KEY (/etc/gh-app/key.pem)
#                        GitHub App that posts the Check Run and Deployment;
#                        without them nothing is posted
#   GITHUB_API           (https://api.github.com), DATAHUB_GIT
#                        (https://github.com/cBioPortal/datahub.git)
#   LOG_URL              link to this Job's logs for the PR
set -uo pipefail

: "${PR_NUMBER:?set PR_NUMBER}"
[[ $PR_NUMBER =~ ^[0-9]+$ ]] || { echo "PR_NUMBER must be a number" >&2; exit 2; }
HEAD_SHA=${HEAD_SHA:-}
HUB_DB=${HUB_DB:-dev_cbioportal_hub_go}
RAW_DB=${HUB_RAW_DB:-${HUB_DB}_raw}
REF_DB=${HUB_REF_DB:-${HUB_DB}_ref}
HUB_BASE=${HUB_BASE:-https://hub.cbioportal.org}
WORKDIR=${WORKDIR:-/tmp/hub-import}
LOCK_WAIT=${LOCK_WAIT:-7200}
LOCK_STALE=${LOCK_STALE:-600}
LOCK_POLL=${LOCK_POLL:-30}
GITHUB_API=${GITHUB_API:-https://api.github.com}
DATAHUB=cBioPortal/datahub
DATAHUB_GIT=${DATAHUB_GIT:-https://github.com/$DATAHUB.git}
# datahub's LFS objects live behind this endpoint (its .lfsconfig), also for
# PRs from forks.
DATAHUB_LFS_URL=${DATAHUB_LFS_URL:-https://nsssw8k94d.execute-api.us-east-1.amazonaws.com/}
GH_APP_KEY=${GH_APP_KEY:-/etc/gh-app/key.pem}
IMPORT=${CBIOPORTAL_IMPORT:-cbioportal-import}
LOG_URL=${LOG_URL:-$HUB_BASE}
OWNER="pr-$PR_NUMBER-${HEAD_SHA:0:7}-$(hostname)-$$"
LOCK_TABLE="\`$RAW_DB\`.\`_hub_import_lock\`"
IMPORT_LOG="\`$RAW_DB\`.\`_hub_import_log\`"

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

# ---- ClickHouse ----
# Credentials travel as headers, so they stay out of URLs and process lists.
ch() {
  local auth=()
  if [ -n "${CLICKHOUSE_USER:-}" ]; then
    auth=(-H "X-ClickHouse-User: $CLICKHOUSE_USER" -H "X-ClickHouse-Key: ${CLICKHOUSE_PASSWORD:-}")
  fi
  curl -sS --fail-with-body "${auth[@]}" "${CLICKHOUSE_URL:-http://localhost:18123}/" --data-binary "$1"
}
sql_lit() { printf '%s' "$1" | sed "s/\\\\/\\\\\\\\/g; s/'/\\\\'/g"; }

# ---- Import lock ----
# The lock is a table in the raw database: CREATE TABLE succeeds for one
# Job only. Its holder adds a heartbeat row every minute; a lock without a
# heartbeat for LOCK_STALE seconds (its Job was killed) is taken over.
HB_PID=""
lock_acquire() {
  local deadline=$(($(date +%s) + LOCK_WAIT)) out info holder age rows
  local me
  me=$(sql_lit "$OWNER")
  while :; do
    if out=$(ch "CREATE TABLE $LOCK_TABLE ENGINE = MergeTree ORDER BY tuple() AS SELECT '$me' AS owner, now() AS heartbeat" 2>&1); then
      (while sleep 60; do
        ch "INSERT INTO $LOCK_TABLE SELECT '$me', now() FROM $LOCK_TABLE HAVING argMax(owner, heartbeat) = '$me'" >/dev/null 2>&1
      done) &
      HB_PID=$!
      log "lock: acquired as $OWNER"
      return 0
    fi
    case $out in
      *TABLE_ALREADY_EXISTS*|*"already exists"*) ;;
      *) log "lock: cannot create $LOCK_TABLE: $out"; return 1 ;;
    esac
    info=$(ch "SELECT argMax(owner, heartbeat), dateDiff('second', max(heartbeat), now()), count() FROM $LOCK_TABLE FORMAT TSV" 2>/dev/null) || continue
    IFS=$'\t' read -r holder age rows <<<"$info"
    if [ "$rows" = 0 ]; then
      holder="(starting)"
      age=$(ch "SELECT dateDiff('second', metadata_modification_time, now()) FROM system.tables WHERE database = '$(sql_lit "$RAW_DB")' AND name = '_hub_import_lock' FORMAT TSV" 2>/dev/null)
      age=${age:-0}
    fi
    if [ "$age" -gt "$LOCK_STALE" ]; then
      log "lock: taking over from $holder (no heartbeat for ${age}s)"
      ch "DROP TABLE IF EXISTS $LOCK_TABLE" >/dev/null || return 1
      continue
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      log "lock: still held by $holder after ${LOCK_WAIT}s"
      return 1
    fi
    log "lock: held by $holder (heartbeat ${age}s ago), waiting"
    sleep "$LOCK_POLL"
  done
}
lock_release() {
  [ -n "$HB_PID" ] || return 0
  kill "$HB_PID" 2>/dev/null; wait "$HB_PID" 2>/dev/null
  HB_PID=""
  local holder
  holder=$(ch "SELECT argMax(owner, heartbeat) FROM $LOCK_TABLE FORMAT TSV" 2>/dev/null)
  if [ "$holder" = "$OWNER" ]; then
    ch "DROP TABLE IF EXISTS $LOCK_TABLE" >/dev/null && log "lock: released"
  else
    log "lock: held by ${holder:-nobody} now, left as is"
  fi
}

# ---- GitHub ----
# Reads use the App token when there is one (higher rate limit), else go
# anonymous; writes need the App.
gh_token() {
  if [ ! -f "$GH_APP_KEY" ] || [ -z "${GH_CLIENT_ID:-}" ] || [ -z "${GH_INSTALLATION_ID:-}" ]; then return 1; fi
  if [ -s "$WORKDIR/.gh-token" ] && [ "$(($(date +%s) - $(stat -c %Y "$WORKDIR/.gh-token")))" -lt 3000 ]; then
    cat "$WORKDIR/.gh-token"; return 0
  fi
  local b64url='openssl base64 -A | tr -d = | tr /+ _-'
  local now h p sig tok
  now=$(date +%s)
  h=$(printf '%s' '{"alg":"RS256","typ":"JWT"}' | eval "$b64url")
  p=$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' $((now - 60)) $((now + 540)) "$GH_CLIENT_ID" | eval "$b64url")
  sig=$(printf '%s.%s' "$h" "$p" | openssl dgst -sha256 -sign "$GH_APP_KEY" -binary | eval "$b64url")
  tok=$(curl -sf -X POST -H "Authorization: Bearer $h.$p.$sig" -H "Accept: application/vnd.github+json" \
    "$GITHUB_API/app/installations/$GH_INSTALLATION_ID/access_tokens" | jq -r '.token // empty')
  [ -n "$tok" ] || return 1
  (umask 077; printf '%s' "$tok" > "$WORKDIR/.gh-token")
  printf '%s' "$tok"
}
gh_api() {
  local method=$1 path=$2 tok
  shift 2
  tok=$(gh_token) || return 1
  curl -sf -X "$method" -H "Authorization: Bearer $tok" -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" -H "Content-Type: application/json" "$GITHUB_API$path" "$@"
}
gh_get() {
  local path=$1 tok auth=()
  if tok=$(gh_token); then auth=(-H "Authorization: Bearer $tok"); fi
  curl -sf "${auth[@]}" -H "Accept: application/vnd.github+json" "$GITHUB_API$path"
}

CHECK_ID="" DEPLOY_ID=""
SUCCESSES=0 FAILURES=0 STUDY_COUNT=0
SUMMARY="$WORKDIR/summary.md"
check_progress() {
  [ -n "$CHECK_ID" ] || return 0
  local title="Importing... ($((SUCCESSES + FAILURES)) of $STUDY_COUNT studies, $FAILURES failed)"
  gh_api PATCH "/repos/$DATAHUB/check-runs/$CHECK_ID" -d "$(jq -n --arg t "$title" --arg s "$(cat "$SUMMARY")" --arg log "$LOG_URL" \
    '{status:"in_progress", details_url:$log, output:{title:$t, summary:$s}}')" >/dev/null || true
}
# finish posts the final Check Run and Deployment status and exits.
finish() {
  local concl=$1 title=$2 details first_ok state
  # Only a complete import is recorded: a recreated Job retries a partial one.
  if [ -n "$HB_PID" ] && [ "$concl" = success ]; then
    ch "INSERT INTO $IMPORT_LOG SELECT $PR_NUMBER, '$(sql_lit "$HEAD_SHA")', '$concl', now()" >/dev/null \
      || log "WARN: could not record the import"
  fi
  lock_release
  first_ok=$(grep -m1 ':white_check_mark:' "$SUMMARY" 2>/dev/null | grep -oE 'https://[^)]+' || true)
  details=$LOG_URL
  [ "$concl" = success ] && [ -n "$first_ok" ] && details=$first_ok
  printf '\n[Import logs](%s)\n' "$LOG_URL" >> "$SUMMARY"
  log "result: $concl: $title"
  cat "$SUMMARY" >&2
  if [ -n "$CHECK_ID" ]; then
    gh_api PATCH "/repos/$DATAHUB/check-runs/$CHECK_ID" -d "$(jq -n --arg c "$concl" --arg t "$title" \
      --arg s "$(cat "$SUMMARY")" --arg u "$details" \
      '{status:"completed", conclusion:$c, details_url:$u, output:{title:$t, summary:$s}}')" >/dev/null \
      || log "WARN: could not update check run $CHECK_ID"
  fi
  if [ -n "$DEPLOY_ID" ]; then
    state=failure
    case $concl in success) state=success ;; skipped) state=inactive ;; esac
    gh_api POST "/repos/$DATAHUB/deployments/$DEPLOY_ID/statuses" -d "$(jq -n --arg s "$state" \
      --arg env "${first_ok:-$HUB_BASE/}" --arg log "$LOG_URL" '{state:$s, environment_url:$env, log_url:$log}')" >/dev/null \
      || log "WARN: could not update deployment $DEPLOY_ID"
  fi
  [ "$concl" = failure ] && exit 1
  exit 0
}

mkdir -p "$WORKDIR"
: > "$SUMMARY"
trap 'lock_release' EXIT

# ---- What to import ----
PR_INFO=$(gh_get "/repos/$DATAHUB/pulls/$PR_NUMBER") || { log "cannot read PR #$PR_NUMBER"; exit 1; }
PR_HEAD=$(jq -r '.head.sha' <<<"$PR_INFO")
HEAD_SHA=${HEAD_SHA:-$PR_HEAD}
log "PR #$PR_NUMBER head $PR_HEAD, Job for $HEAD_SHA"

CHANGED=""
for page in $(seq 1 30); do
  files=$(gh_get "/repos/$DATAHUB/pulls/$PR_NUMBER/files?per_page=100&page=$page" | jq -r '.[].filename') \
    || { log "cannot list the PR's files"; exit 1; }
  [ -n "$files" ] || break
  CHANGED+="$files"$'\n'
done
STUDIES=$( { grep -E '^public/[^/]+/' <<<"$CHANGED" | cut -d/ -f1-2
             grep -E '^crdc/gdc/[^/]+/' <<<"$CHANGED" | cut -d/ -f1-3; } | sort -u)
PANELS_CHANGED=$(grep -c '^reference_data/gene_panels/' <<<"$CHANGED")

# `hub-import-only:` followed by "- <path>" lines restricts the import to
# those of the changed studies.
ONLY=$(jq -r '.body // ""' <<<"$PR_INFO" | tr -d '\r' | awk '
  /^hub-import-only:/ {found=1; next}
  found { if ($0 ~ /^-[[:space:]]/) { sub(/^-[[:space:]]+/, ""); sub(/[[:space:]]+$/, ""); if (length($0)) print } else exit }')
if [ -n "$ONLY" ]; then
  log "hub-import-only: $(tr '\n' ' ' <<<"$ONLY")"
  STUDIES=$(comm -12 <(sort -u <<<"$STUDIES") <(sort -u <<<"$ONLY"))
fi
STUDY_COUNT=$(grep -c . <<<"$STUDIES")
if [ "$STUDY_COUNT" -eq 0 ] && [ "$PANELS_CHANGED" -eq 0 ]; then
  log "no studies to import"
  exit 0
fi
log "studies: $(tr '\n' ' ' <<<"$STUDIES")"

# Argo CD recreates a finished Job once its TTL removes it; a commit that
# was imported already is not imported again.
ch "CREATE TABLE IF NOT EXISTS $IMPORT_LOG (pr UInt32, sha String, conclusion String, finished DateTime) ENGINE = MergeTree ORDER BY (pr, sha)" >/dev/null \
  || { log "cannot reach ClickHouse"; exit 1; }
done_at=$(ch "SELECT max(finished) FROM $IMPORT_LOG WHERE pr = $PR_NUMBER AND sha = '$(sql_lit "$HEAD_SHA")' HAVING count() > 0 FORMAT TSV")
if [ -n "$done_at" ]; then
  log "commit $HEAD_SHA was imported at $done_at; nothing to do"
  exit 0
fi

CHECK_ID=$(gh_api POST "/repos/$DATAHUB/check-runs" -d "$(jq -n --arg sha "$HEAD_SHA" --arg base "$HUB_BASE" --arg log "$LOG_URL" \
  '{name:"Hub preview import", head_sha:$sha, status:"in_progress", details_url:$log,
    output:{title:"Importing studies...", summary:"[Logs](\($log)) · [hub.cbioportal.org](\($base))"}}')" | jq -r '.id // empty')
DEPLOY_ID=$(gh_api POST "/repos/$DATAHUB/deployments" -d "$(jq -n --arg sha "$HEAD_SHA" \
  '{ref:$sha, environment:"hub-preview", auto_merge:false, required_contexts:[], transient_environment:true,
    description:"Preview on hub.cbioportal.org"}')" | jq -r '.id // empty')
if [ -n "$DEPLOY_ID" ]; then
  gh_api POST "/repos/$DATAHUB/deployments/$DEPLOY_ID/statuses" -d "$(jq -n --arg env "$HUB_BASE/" --arg log "$LOG_URL" \
    '{state:"in_progress", environment_url:$env, log_url:$log}')" >/dev/null || true
fi
[ -n "$CHECK_ID" ] && log "posted check run $CHECK_ID, deployment $DEPLOY_ID"

# ---- Checkout (LFS files are pulled per study, just before its ingest) ----
REPO="$WORKDIR/datahub"
rm -rf "$REPO"
git init -q "$REPO" || exit 1
git -C "$REPO" config lfs.url "$DATAHUB_LFS_URL"
# datahub's .lfsconfig excludes every path from LFS downloads by default.
git -C "$REPO" config lfs.fetchexclude ""
git -C "$REPO" remote add origin "$DATAHUB_GIT"
# The studies, the shared gene panels and the seed dump (not its archives).
{ sed 's|.*|/&/|' <<<"$STUDIES"; echo /reference_data/gene_panels/; echo '/seedDB/seed-cbioportal_hg19_hg38_*.sql.gz'; } \
  | grep -v '^//$' | git -C "$REPO" sparse-checkout set --no-cone --stdin
if ! GIT_LFS_SKIP_SMUDGE=1 git -C "$REPO" fetch -q --depth=1 --filter=blob:none origin "pull/$PR_NUMBER/head"; then
  printf -- '- :x: could not fetch the PR head\n' >> "$SUMMARY"
  finish failure "Could not fetch the PR"
fi
FETCHED=$(git -C "$REPO" rev-parse FETCH_HEAD)
if [ "$FETCHED" != "$HEAD_SHA" ]; then
  printf -- '- superseded by %s\n' "$FETCHED" >> "$SUMMARY"
  finish skipped "Superseded by a newer commit"
fi
GIT_LFS_SKIP_SMUDGE=1 git -C "$REPO" -c advice.ignoredHook=false checkout -q FETCH_HEAD || finish failure "Could not check out the PR"
# lfs_pull replaces the LFS pointer files under a directory by their
# content. Each file is smudged on its own: `git lfs pull` lists the whole
# tree with sizes, which in this blobless clone downloads every blob. An
# object missing from datahub's LFS store is fetched from the GitHub LFS of
# the PR's head repository, where a contributor may have pushed it.
HEAD_LFS_URL="$(jq -r '.head.repo.clone_url // empty' <<<"$PR_INFO")"
HEAD_LFS_URL="${HEAD_LFS_URL:-$DATAHUB_GIT}/info/lfs"
is_pointer() { head -c 100 "$1" | grep -q '^version https://git-lfs'; }
lfs_pull() {
  local f n=0
  while IFS= read -r -d '' f; do
    is_pointer "$f" || continue
    if ! git -C "$REPO" -c lfs.transfer.maxretries=1 lfs smudge -- "${f#"$REPO"/}" < "$f" > "$f.lfs" 2>"$WORKDIR/lfs.err" || is_pointer "$f.lfs"; then
      log "  ${f#"$REPO"/}: not in datahub's LFS store, trying $HEAD_LFS_URL"
      if ! git -C "$REPO" -c lfs.url="$HEAD_LFS_URL" lfs smudge -- "${f#"$REPO"/}" < "$f" > "$f.lfs" || is_pointer "$f.lfs"; then
        log "  LFS download failed: ${f#"$REPO"/}"
        rm -f "$f.lfs"
        return 1
      fi
    fi
    mv "$f.lfs" "$f"
    n=$((n + 1))
  done < <(find "$REPO/$1" -type f -size -2k -print0)
  log "  $n LFS files downloaded"
}

# ---- Import, one at a time across Jobs ----
lock_acquire || { printf -- '- :x: could not take the hub import lock (see logs)\n' >> "$SUMMARY"; finish failure "Could not take the hub import lock"; }
# A newer commit may have been pushed while this Job waited.
if latest=$(gh_get "/repos/$DATAHUB/pulls/$PR_NUMBER" | jq -r '.head.sha') && [ -n "$latest" ] && [ "$latest" != "$HEAD_SHA" ]; then
  printf -- '- superseded by %s\n' "$latest" >> "$SUMMARY"
  finish skipped "Superseded by a newer commit"
fi

genes=$(ch "SELECT count() FROM system.tables WHERE database = '$(sql_lit "$REF_DB")' AND name = 'gene' FORMAT TSV")
[ "$genes" = 1 ] && genes=$(ch "SELECT count() FROM \`$REF_DB\`.gene FORMAT TSV")
if [ "${genes:-0}" = 0 ]; then
  seed=$(ls "$REPO"/seedDB/seed-cbioportal_hg19_hg38_*.sql.gz 2>/dev/null | sort -V | tail -1)
  log "loading reference data from ${seed:-?}"
  [ -n "$seed" ] && "$IMPORT" load-reference "$seed" --db "$REF_DB" \
    || { printf -- '- :x: loading the reference data failed\n' >> "$SUMMARY"; finish failure "Loading the reference data failed"; }
fi
shared=$(ch "SELECT count() FROM system.tables WHERE database = '$(sql_lit "$RAW_DB")' AND name = 'gene_panel' FORMAT TSV")
[ "$shared" = 1 ] && shared=$(ch "SELECT count() FROM \`$RAW_DB\`.gene_panel WHERE study_id = '' FORMAT TSV")
if [ "${shared:-0}" = 0 ] || [ "$PANELS_CHANGED" -gt 0 ]; then
  log "loading the shared gene panels"
  if lfs_pull reference_data/gene_panels && "$IMPORT" load-gene-panels "$REPO/reference_data/gene_panels" --db "$RAW_DB"; then
    if [ "$PANELS_CHANGED" -gt 0 ]; then
      printf -- '- :white_check_mark: reference gene panels\n' >> "$SUMMARY"
      SUCCESSES=$((SUCCESSES + 1))
    fi
  else
    printf -- '- :x: loading the reference gene panels failed\n' >> "$SUMMARY"
    FAILURES=$((FAILURES + 1))
  fi
fi

IMPORTED=()
declare -A RESULT
i=0
for dir in $STUDIES; do
  i=$((i + 1))
  log "[$i/$STUDY_COUNT] $dir"
  if [ ! -f "$REPO/$dir/meta_study.txt" ]; then
    log "  no meta_study.txt, skipped"
    RESULT[$dir]="- :heavy_minus_sign: \`$dir\` — no meta_study.txt in the PR, skipped"
    continue
  fi
  study_id=$(sed -n 's/^cancer_study_identifier:[[:space:]]*//p' "$REPO/$dir/meta_study.txt" | tr -d '\r' | sed 's/[[:space:]]*$//')
  start=$(date +%s)
  if lfs_pull "$dir" && "$IMPORT" ingest "$REPO/$dir" --db "$RAW_DB"; then
    IMPORTED+=("$study_id")
    RESULT[$dir]="- :white_check_mark: [$study_id]($HUB_BASE/study/summary?id=$study_id) ($(($(date +%s) - start))s)"
  else
    log "  ingest failed; removing ${study_id:-?} from the hub"
    [ -n "$study_id" ] && "$IMPORT" remove-study "$study_id" --db "$RAW_DB"
    FAILURES=$((FAILURES + 1))
    RESULT[$dir]="- :x: \`$study_id\` — import failed (see logs); removed from the hub"
    printf '%s\n' "${RESULT[$dir]}" >> "$SUMMARY"
    check_progress
  fi
  rm -rf "${REPO:?}/$dir"
done

log "transform"
start=$(date +%s)
if "$IMPORT" transform --raw-db "$RAW_DB" --db "$HUB_DB" --ref-db "$REF_DB" >"$WORKDIR/transform.log" 2>&1; then
  log "transform done in $(($(date +%s) - start))s"
  for dir in $STUDIES; do
    case ${RESULT[$dir]:-} in
      "- :white_check_mark:"*) SUCCESSES=$((SUCCESSES + 1)); printf '%s\n' "${RESULT[$dir]}" >> "$SUMMARY" ;;
      "- :heavy_minus_sign:"*) printf '%s\n' "${RESULT[$dir]}" >> "$SUMMARY" ;;
    esac
  done
else
  cat "$WORKDIR/transform.log" >&2
  log "transform failed; removing this PR's studies (${IMPORTED[*]:-none}) and rebuilding without them"
  for id in "${IMPORTED[@]}"; do
    "$IMPORT" remove-study "$id" --db "$RAW_DB"
    printf -- '- :x: `%s` — building the hub failed (see logs); removed from the hub\n' "$id" >> "$SUMMARY"
    FAILURES=$((FAILURES + 1))
  done
  if [ ${#IMPORTED[@]} -gt 0 ] && ! "$IMPORT" transform --raw-db "$RAW_DB" --db "$HUB_DB" --ref-db "$REF_DB" >/dev/null; then
    log "the rebuild failed too; the hub keeps serving its previous build"
  fi
fi

TOTAL=$((SUCCESSES + FAILURES))
if [ "$FAILURES" -eq 0 ] && [ "$SUCCESSES" -gt 0 ]; then
  finish success "Imported $SUCCESSES of $TOTAL"
elif [ "$SUCCESSES" -gt 0 ]; then
  finish neutral "Imported $SUCCESSES of $TOTAL"
elif [ "$FAILURES" -eq 0 ]; then
  finish neutral "Nothing imported"
else
  finish failure "Imported 0 of $TOTAL"
fi
