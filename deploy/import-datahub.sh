#!/usr/bin/env bash
# Daily datahub import for go.cbioportal.org (blue/green).
#
# Builds the INACTIVE color from scratch: empties its three databases
# (${DB_PREFIX}_<color>, _raw, _ref), loads the reference data and gene panels,
# ingests every study listed in STUDIES_FILE from cBioPortal/datahub, runs the
# transform, checks the result through a throwaway API process, and writes
# $WORKDIR/state.env with the database names for the next container (which
# switches the API over by patching ConfigMap cbioportal-go-active).
#
# The databases must already exist; this script only drops and creates tables
# and views inside them.
#
# Env:
#   CLICKHOUSE_URL                       ClickHouse HTTP endpoint (required)
#   CLICKHOUSE_USER, CLICKHOUSE_PASSWORD credentials (optional)
#   DB_PREFIX      database name prefix (default dev_cbioportal_public_go)
#   ACTIVE_DB      model database the API serves now; empty/unknown builds blue
#   WORKDIR        scratch space for the datahub checkout (default /work)
#   DATAHUB_REPO   default https://github.com/cBioPortal/datahub.git
#   DATAHUB_REF    default master
#   STUDIES_FILE   one study id per line, '#' comments (default
#                  /app/deploy/datahub-studies.txt)
#   MAX_FAILED_PCT fail the job when more than this % of studies fail (default 25)
set -euo pipefail

: "${CLICKHOUSE_URL:?CLICKHOUSE_URL is required}"
DB_PREFIX="${DB_PREFIX:-dev_cbioportal_public_go}"
ACTIVE_DB="${ACTIVE_DB:-}"
WORKDIR="${WORKDIR:-/work}"
DATAHUB_REPO="${DATAHUB_REPO:-https://github.com/cBioPortal/datahub.git}"
DATAHUB_REF="${DATAHUB_REF:-master}"
STUDIES_FILE="${STUDIES_FILE:-/app/deploy/datahub-studies.txt}"
MAX_FAILED_PCT="${MAX_FAILED_PCT:-25}"
IMPORTER="${IMPORTER:-cbioportal-import}"
API_BIN="${API_BIN:-cbioportal-api}"
CHECK_PORT="${CHECK_PORT:-18999}"

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*"; exit 1; }
# Prefix every line of a command's output with a timestamp.
stamp() { while IFS= read -r line; do printf '%s   %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$line"; done; }

job_start=$(date +%s)
elapsed() { echo "$(( $(date +%s) - $1 ))s"; }

# --- target color ------------------------------------------------------------
if [[ "$ACTIVE_DB" == "${DB_PREFIX}_blue" ]]; then
  COLOR=green
else
  COLOR=blue
fi
TARGET_DB="${DB_PREFIX}_${COLOR}"
TARGET_RAW_DB="${TARGET_DB}_raw"
TARGET_REF_DB="${TARGET_DB}_ref"
log "active database: '${ACTIVE_DB:-<none>}'; building ${COLOR}: ${TARGET_DB}, ${TARGET_RAW_DB}, ${TARGET_REF_DB}"
[[ "$TARGET_DB" != "$ACTIVE_DB" ]] || die "target ${TARGET_DB} is the active database"
[[ -r "$STUDIES_FILE" ]] || die "studies file ${STUDIES_FILE} not readable"

mkdir -p "$WORKDIR"
# git and git-lfs need a writable HOME for their config.
if [[ -z "${HOME:-}" || ! -w "${HOME}" ]]; then
  export HOME="$WORKDIR/.home"
  mkdir -p "$HOME"
fi

# --- ClickHouse access ---------------------------------------------------------
# Credentials go to curl through a private header file, never on the command line.
private_dir=$(mktemp -d "$WORKDIR/.private.XXXXXX")
chmod 700 "$private_dir"
api_pid=""
cleanup() {
  [[ -n "$api_pid" ]] && kill "$api_pid" 2>/dev/null || true
  rm -rf "$private_dir"
}
trap cleanup EXIT
ch_headers="$private_dir/headers"
: > "$ch_headers"
chmod 600 "$ch_headers"
if [[ -n "${CLICKHOUSE_USER:-}" ]]; then
  printf 'X-ClickHouse-User: %s\nX-ClickHouse-Key: %s\n' "$CLICKHOUSE_USER" "${CLICKHOUSE_PASSWORD:-}" > "$ch_headers"
fi

# ch_query SQL — run a statement; prints the response, fails on HTTP errors.
ch_query() {
  local out status
  out=$(curl -sS --max-time 600 -H @"$ch_headers" --data-binary "$1" -w '\n%{http_code}' "$CLICKHOUSE_URL/") || return 1
  status=${out##*$'\n'}
  out=${out%$'\n'*}
  if [[ "$status" != 200 ]]; then
    printf '%s\n' "$out" >&2
    return 1
  fi
  printf '%s' "$out"
}

log "ClickHouse: $(ch_query 'SELECT version()' | tr -d '\n') at ${CLICKHOUSE_URL}"

# --- 1. empty the target databases ---------------------------------------------
clean_db() {
  local db=$1 exists objects n=0 name engine
  exists=$(ch_query "SELECT count() FROM system.databases WHERE name = '${db}'") || die "cannot query ClickHouse"
  [[ "$exists" == 1 ]] || die "database ${db} does not exist (it must be pre-created)"
  # Views first, so no view outlives the tables it reads.
  objects=$(ch_query "SELECT name, engine FROM system.tables WHERE database = '${db}' ORDER BY engine NOT LIKE '%View', name FORMAT TSV") \
    || die "cannot list tables of ${db}"
  while IFS=$'\t' read -r name engine; do
    [[ -n "$name" ]] || continue
    if [[ "$engine" == *View ]]; then
      ch_query "DROP VIEW IF EXISTS \`${db}\`.\`${name}\` SYNC" >/dev/null
    else
      ch_query "DROP TABLE IF EXISTS \`${db}\`.\`${name}\` SYNC" >/dev/null
    fi
    n=$((n + 1))
  done <<< "$objects"
  log "emptied ${db} (${n} tables/views dropped)"
}

t=$(date +%s)
for db in "$TARGET_DB" "$TARGET_RAW_DB" "$TARGET_REF_DB"; do clean_db "$db"; done
log "step clean done in $(elapsed "$t")"

# --- 2. datahub checkout (metadata only; LFS files are pulled per study) --------
mapfile -t STUDIES < <(sed -e 's/#.*//' -e 's/[[:space:]]//g' "$STUDIES_FILE" | grep -v '^$')
(( ${#STUDIES[@]} > 0 )) || die "no studies in ${STUDIES_FILE}"
log "${#STUDIES[@]} studies in ${STUDIES_FILE}"

t=$(date +%s)
DATAHUB="$WORKDIR/datahub"
rm -rf "$DATAHUB"
export GIT_LFS_SKIP_SMUDGE=1
# A full shallow clone (~700 MB of pointers and metadata): with a blobless
# clone, git-lfs's attribute scan fetches blobs one by one and takes hours.
git clone --quiet --depth 1 --branch "$DATAHUB_REF" --no-checkout "$DATAHUB_REPO" "$DATAHUB"
sparse=(/seedDB/seed-*.sql.gz /reference_data/gene_panels/)
for s in "${STUDIES[@]}"; do sparse+=("/public/${s}/"); done
git -C "$DATAHUB" sparse-checkout set --no-cone "${sparse[@]}"
git -C "$DATAHUB" checkout --quiet "$DATAHUB_REF"
log "datahub ${DATAHUB_REF} @ $(git -C "$DATAHUB" rev-parse --short HEAD) checked out in $(elapsed "$t")"

# lfs_pull PATH — download the LFS files under PATH. The repo's .lfsconfig
# excludes everything by default, hence the override.
lfs_pull() {
  git -C "$DATAHUB" -c lfs.fetchexclude= lfs pull --include="$1" 2>&1 | stamp
}
# Drop downloaded LFS objects (the working-tree copies are removed separately).
lfs_prune() { rm -rf "$DATAHUB/.git/lfs/objects"; }

# --- 3. reference data and gene panels -----------------------------------------
t=$(date +%s)
seed=$(cd "$DATAHUB" && ls seedDB/seed-*.sql.gz 2>/dev/null | sort -V | tail -n 1)
[[ -n "$seed" ]] || die "no seedDB/seed-*.sql.gz in datahub"
lfs_pull "$seed"
log "loading reference data from ${seed}"
"$IMPORTER" load-reference "$DATAHUB/$seed" --db "$TARGET_REF_DB" 2>&1 | stamp
rm -f "$DATAHUB/$seed"
log "step load-reference done in $(elapsed "$t")"

t=$(date +%s)
lfs_pull "reference_data/gene_panels"
"$IMPORTER" load-gene-panels "$DATAHUB/reference_data/gene_panels" --db "$TARGET_RAW_DB" 2>&1 | stamp
rm -rf "$DATAHUB/reference_data/gene_panels"
lfs_prune
log "step load-gene-panels done in $(elapsed "$t")"

# --- 4. studies ----------------------------------------------------------------
ok=()
failed=()
for s in "${STUDIES[@]}"; do
  t=$(date +%s)
  dir="$DATAHUB/public/$s"
  if [[ ! -f "$dir/meta_study.txt" ]]; then
    log "study ${s}: FAILED (not in datahub public/)"
    failed+=("$s")
    continue
  fi
  log "study ${s}: downloading"
  if lfs_pull "public/${s}"; then
    log "study ${s}: $(du -sh "$dir" | cut -f1) on disk, ingesting"
    if "$IMPORTER" ingest "$dir" --db "$TARGET_RAW_DB" 2>&1 | stamp; then
      ok+=("$s")
      log "study ${s}: done in $(elapsed "$t")"
    else
      failed+=("$s")
      log "study ${s}: FAILED to ingest after $(elapsed "$t")"
    fi
  else
    failed+=("$s")
    log "study ${s}: FAILED to download after $(elapsed "$t")"
  fi
  rm -rf "$dir"
  lfs_prune
done
log "studies: ${#ok[@]} ingested, ${#failed[@]} failed${failed:+: ${failed[*]}}"
(( ${#ok[@]} > 0 )) || die "no study was ingested"
if (( ${#failed[@]} * 100 > ${#STUDIES[@]} * MAX_FAILED_PCT )); then
  die "${#failed[@]}/${#STUDIES[@]} studies failed (limit ${MAX_FAILED_PCT}%)"
fi

# --- 5. transform --------------------------------------------------------------
t=$(date +%s)
"$IMPORTER" transform --raw-db "$TARGET_RAW_DB" --db "$TARGET_DB" --ref-db "$TARGET_REF_DB" 2>&1 | stamp
log "step transform done in $(elapsed "$t")"
rm -rf "$DATAHUB"

# --- 6. sanity check -----------------------------------------------------------
t=$(date +%s)
for table in "$TARGET_REF_DB".gene "$TARGET_REF_DB".type_of_cancer "$TARGET_RAW_DB".gene_panel \
  "$TARGET_DB".cancer_study "$TARGET_DB".sample_derived "$TARGET_DB".clinical_data_derived \
  "$TARGET_DB".molecular_profile "$TARGET_DB".mutation; do
  n=$(ch_query "SELECT count() FROM ${table}") || die "cannot read ${table}"
  log "check ${table}: ${n} rows"
  [[ "$n" =~ ^[0-9]+$ && "$n" -gt 0 ]] || die "${table} is empty"
done
# A study whose ingest failed part-way may still have staged rows, hence >=.
studies_in_db=$(ch_query "SELECT count() FROM \`${TARGET_DB}\`.cancer_study")
(( studies_in_db >= ${#ok[@]} )) || die "${TARGET_DB}.cancer_study has ${studies_in_db} studies, expected ${#ok[@]}"
(( studies_in_db == ${#ok[@]} )) || log "WARNING: ${TARGET_DB}.cancer_study has ${studies_in_db} studies, ${#ok[@]} ingested cleanly"

empty_studies=$(ch_query "SELECT arrayStringConcat(groupArray(study_id), ' ') FROM \`${TARGET_DB}\`.cancer_study WHERE study_id NOT IN (SELECT cancer_study_identifier FROM \`${TARGET_DB}\`.sample_derived)")
[[ -z "$empty_studies" ]] || log "WARNING: studies without samples (check their meta files): ${empty_studies}"

# The API itself, against the new databases.
PORT="$CHECK_PORT" CBIOPORTAL_DB="$TARGET_DB" CBIOPORTAL_RAW_DB="$TARGET_RAW_DB" CBIOPORTAL_REF_DB="$TARGET_REF_DB" \
  FRONTEND_DIR= SESSION_STORE_FILE= "$API_BIN" > "$WORKDIR/api-check.log" 2>&1 &
api_pid=$!
for _ in $(seq 1 60); do
  curl -fsS "http://127.0.0.1:${CHECK_PORT}/health" >/dev/null 2>&1 && break
  sleep 1
done
api_studies=$(curl -fsS "http://127.0.0.1:${CHECK_PORT}/api/studies?projection=SUMMARY" | grep -o '"studyId"' | wc -l) \
  || { cat "$WORKDIR/api-check.log" >&2; die "API check failed"; }
api_samples=$(curl -fsS "http://127.0.0.1:${CHECK_PORT}/api/studies/${ok[0]}/samples" | grep -o '"sampleId"' | wc -l) \
  || { cat "$WORKDIR/api-check.log" >&2; die "API check failed"; }
kill "$api_pid" 2>/dev/null || true
wait "$api_pid" 2>/dev/null || true
api_pid=""
log "check API: /api/studies returns ${api_studies} studies; ${ok[0]} has ${api_samples} samples"
(( api_studies == studies_in_db )) || die "API returns ${api_studies} studies, expected ${studies_in_db}"
(( api_samples > 0 )) || die "API returns no samples for ${ok[0]}"
log "step sanity check done in $(elapsed "$t")"

# --- 7. hand-off -----------------------------------------------------------------
cat > "$WORKDIR/state.env" <<EOF
CBIOPORTAL_DB=${TARGET_DB}
CBIOPORTAL_RAW_DB=${TARGET_RAW_DB}
CBIOPORTAL_REF_DB=${TARGET_REF_DB}
EOF
log "wrote $WORKDIR/state.env:"
stamp < "$WORKDIR/state.env"
log "import finished in $(elapsed "$job_start"): ${#ok[@]}/${#STUDIES[@]} studies into ${COLOR}${failed:+; failed: ${failed[*]}}"
