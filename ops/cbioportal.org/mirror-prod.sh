#!/usr/bin/env bash
# Rebuilds the inactive blue/green color of go.cbioportal.org from the live
# production database, the way the LibreChat clone job does:
#
#   1. read the live production color and its last import from
#      ${MGMT_DB}.update_status; stop without changes while an import is
#      running, or when the served color was already built from that import
#      (so the job can run hourly and follow each production switch);
#   2. pick the Go color that is not being served (ACTIVE_DB is the database
#      served now; anything else bootstraps into blue);
#   3. empty that color's databases and zero-copy CLONE every production table
#      (minus auth/credential tables) into ${PREFIX}_<color>_ref;
#   4. build the portal database ${PREFIX}_<color> from the clone with
#      `cbioportal-import transform --from-prod`;
#   5. record the production snapshot it was built from in
#      ${PREFIX}_<color>_ref._go_mirror_source, and write $WORKDIR/state.env
#      (CBIOPORTAL_DB, CBIOPORTAL_RAW_DB, CBIOPORTAL_REF_DB) for the step that
#      switches the API over. When nothing is rebuilt, state.env names the
#      served databases, so that step changes nothing.
#
# Env: CLICKHOUSE_URL, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD (never printed);
# PREFIX (default dev_cbioportal_public_go), ACTIVE_DB, WORKDIR (default
# /work), MGMT_DB (default publicdb_update_management_database), SRC_PREFIX
# (default cbioportal_public_), PORTAL_DATABASE (default public).
# The user needs SELECT on ${SRC_PREFIX}{blue,green}.* and the update_status
# table, and SELECT, INSERT, ALTER, CREATE TABLE, CREATE VIEW, DROP TABLE,
# DROP VIEW and TRUNCATE on the six ${PREFIX}_* databases (they must exist).
set -euo pipefail

: "${CLICKHOUSE_URL:?missing}"
PREFIX=${PREFIX:-dev_cbioportal_public_go}
WORKDIR=${WORKDIR:-/work}
MGMT_DB=${MGMT_DB:-publicdb_update_management_database}
SRC_PREFIX=${SRC_PREFIX:-cbioportal_public_}
PORTAL_DATABASE=${PORTAL_DATABASE:-public}
# The mirror is served publicly, so it carries no auth or credential data: an
# explicit list for today's schema plus substring patterns for future tables.
EXCLUDED_TABLES="('authorities','data_access_tokens','users')"
EXCLUDED_SUBSTRINGS="'_backup','token','oauth','session','login','password','credential','secret'"

log() { printf '[mirror %s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

# Credentials travel as headers, so they appear in neither URLs nor logs.
# Transient failures (DNS, connection, timeouts) are retried; every statement
# here is safe to repeat.
ch() {
  local auth=()
  if [ -n "${CLICKHOUSE_USER:-}" ]; then
    auth=(-H "X-ClickHouse-User: $CLICKHOUSE_USER" -H "X-ClickHouse-Key: ${CLICKHOUSE_PASSWORD:-}")
  fi
  curl -sS --fail-with-body --connect-timeout 30 --retry 5 --retry-delay 10 --retry-all-errors \
    "${auth[@]}" "$CLICKHOUSE_URL/" --data-binary "$1"
}

# 1) Production color and the import it serves.
IFS=$'\t' read -r PROD_COLOR PROD_STATUS PROD_COMPLETED < <(ch "SELECT current_database_in_production, update_process_status, toString(time_of_last_update_process_completion) FROM ${MGMT_DB}.update_status WHERE portal_database = '${PORTAL_DATABASE}' FORMAT TSV")
case "$PROD_COLOR" in blue | green) ;; *) log "FAIL: unexpected production color '$PROD_COLOR'"; exit 1 ;; esac
SRC_DB=${SRC_PREFIX}${PROD_COLOR}
SNAPSHOT="$PROD_COLOR $PROD_COMPLETED"
log "production color=$PROD_COLOR, status=$PROD_STATUS, last import completed $PROD_COMPLETED"

# Keeps the served databases: state.env names them, so the switch step is a
# no-op and the pods are not restarted.
keep_active() {
  log "$1; keeping ${ACTIVE_DB}"
  mkdir -p "$WORKDIR"
  printf 'CBIOPORTAL_DB=%s\nCBIOPORTAL_RAW_DB=%s\nCBIOPORTAL_REF_DB=%s\n' "$ACTIVE_DB" "${ACTIVE_DB}_raw" "${ACTIVE_DB}_ref" >"$WORKDIR/state.env"
  exit 0
}
case "${ACTIVE_DB:-}" in
  "${PREFIX}_blue" | "${PREFIX}_green")
    [ "$PROD_STATUS" = complete ] || keep_active "production import in progress (status=$PROD_STATUS)"
    built_from=""
    if [ "$(ch "SELECT count() FROM system.tables WHERE database = '${ACTIVE_DB}_ref' AND name = '_go_mirror_source' FORMAT TSV")" = 1 ]; then
      built_from=$(ch "SELECT any(snapshot) FROM \`${ACTIVE_DB}_ref\`._go_mirror_source FORMAT TSV")
    fi
    [ "$built_from" != "$SNAPSHOT" ] || keep_active "already built from $SNAPSHOT"
    log "served databases built from '${built_from:-unknown}'; rebuilding from $SNAPSHOT"
    ;;
esac

# 2) Target color: the one not being served.
case "${ACTIVE_DB:-}" in
  "${PREFIX}_blue") COLOR=green ;;
  "${PREFIX}_green") COLOR=blue ;;
  *) log "active='${ACTIVE_DB:-}' — bootstrapping into blue"; COLOR=blue ;;
esac
DB=${PREFIX}_${COLOR} RAW_DB=${PREFIX}_${COLOR}_raw REF_DB=${PREFIX}_${COLOR}_ref
log "active=${ACTIVE_DB:-<unset>}; building into $DB"

# 3) Empty the target databases (tables and views; the databases stay, since
#    the job user may not create or drop databases), then clone production.
for d in "$DB" "$RAW_DB" "$REF_DB"; do
  for obj in $(ch "SELECT name FROM system.tables WHERE database = '$d' AND engine = 'View' FORMAT TSV"); do
    ch "DROP VIEW IF EXISTS \`$d\`.\`$obj\`" >/dev/null
  done
  for obj in $(ch "SELECT name FROM system.tables WHERE database = '$d' AND engine != 'View' FORMAT TSV"); do
    ch "DROP TABLE IF EXISTS \`$d\`.\`$obj\` SYNC" >/dev/null
  done
done

TABLES=$(ch "SELECT name FROM system.tables
  WHERE database = '$SRC_DB' AND engine LIKE '%MergeTree'
    AND name NOT IN $EXCLUDED_TABLES
    AND NOT multiSearchAny(lower(name), [$EXCLUDED_SUBSTRINGS])
  ORDER BY name FORMAT TSV")
N=$(printf '%s\n' "$TABLES" | grep -c . || true)
log "cloning $N tables from $SRC_DB into $REF_DB"
i=0
for t in $TABLES; do
  i=$((i + 1))
  ch "CREATE TABLE IF NOT EXISTS \`$REF_DB\`.\`$t\` CLONE AS \`$SRC_DB\`.\`$t\`" >/dev/null
  log "  [$i/$N] $t"
done

# 4) Portal database from the production schema.
if ! cbioportal-import help transform 2>&1 | grep -q -- '-from-prod'; then
  log "FAIL: this image's cbioportal-import has no 'transform --from-prod' yet; leaving the live color as is"
  exit 1
fi
log "building $DB from $REF_DB"
cbioportal-import transform --from-prod --raw-db "$RAW_DB" --ref-db "$REF_DB" --db "$DB"

samples=$(ch "SELECT count() FROM \`$DB\`.sample_derived FORMAT TSV")
[ "$samples" -gt 0 ] || { log "FAIL: $DB has no samples"; exit 1; }
log "sanity check: $samples samples in $DB"

# 5) Record the snapshot, then hand over to the step that switches the API to
#    the new color.
ch "CREATE TABLE \`$REF_DB\`._go_mirror_source (snapshot String, built_at DateTime DEFAULT now()) ENGINE = MergeTree ORDER BY tuple()" >/dev/null
ch "INSERT INTO \`$REF_DB\`._go_mirror_source (snapshot) VALUES ('$SNAPSHOT')" >/dev/null
mkdir -p "$WORKDIR"
printf 'CBIOPORTAL_DB=%s\nCBIOPORTAL_RAW_DB=%s\nCBIOPORTAL_REF_DB=%s\n' "$DB" "$RAW_DB" "$REF_DB" >"$WORKDIR/state.env"
log "done: CBIOPORTAL_DB=$DB"
