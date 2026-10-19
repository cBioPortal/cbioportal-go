#!/usr/bin/env bash
# Publishes studies already staged in a local ClickHouse to another ClickHouse
# (e.g. ClickHouse Cloud): copies their staging rows into <prefix>_raw, loads
# the reference data into <prefix>_ref, and builds the portal database
# <prefix> with `cbioportal-import transform` — the databases the API then
# serves with -db <prefix> -raw-db <prefix>_raw -ref-db <prefix>_ref.
#
#   deploy/publish-local-studies.sh <prefix> <seed.sql.gz> <study-id>...
#
# Target: CLICKHOUSE_URL, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD (set them in
# your shell; they are never printed). The three target databases must exist;
# the user needs SELECT, INSERT, ALTER, CREATE TABLE, CREATE VIEW, DROP TABLE,
# DROP VIEW and TRUNCATE on them.
# Source: SOURCE_URL (default http://localhost:18123), SOURCE_DB (default
# cbioportal_raw) — a staging database written by `cbioportal-import ingest`.
#
# Example:
#   export CLICKHOUSE_URL=https://<host>:8443 CLICKHOUSE_USER=go_import
#   read -rs CLICKHOUSE_PASSWORD && export CLICKHOUSE_PASSWORD
#   deploy/publish-local-studies.sh my_portal \
#     seed-cbioportal_hg19_hg38_v2.14.5.sql.gz msk_impact_2017 acbc_mskcc_2015
set -euo pipefail

if [ $# -lt 3 ]; then
  sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'
  exit 2
fi
PREFIX=$1 SEED=$2
shift 2
STUDIES=("$@")
SOURCE_URL=${SOURCE_URL:-http://localhost:18123}
SOURCE_DB=${SOURCE_DB:-cbioportal_raw}
: "${CLICKHOUSE_URL:?set CLICKHOUSE_URL to the target ClickHouse}"
RAW_DB=${PREFIX}_raw REF_DB=${PREFIX}_ref

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

# Target requests carry the credentials as headers so they stay out of URLs.
target() {
  local auth=()
  if [ -n "${CLICKHOUSE_USER:-}" ]; then
    auth=(-H "X-ClickHouse-User: $CLICKHOUSE_USER" -H "X-ClickHouse-Key: ${CLICKHOUSE_PASSWORD:-}")
  fi
  local query=()
  [ -n "$1" ] && query=(--url-query "query=$1")
  curl -sS --fail-with-body "${auth[@]}" "${query[@]}" "$CLICKHOUSE_URL/" --data-binary "${2:-@-}"
}
source_query() { curl -sS --fail-with-body "$SOURCE_URL/" --data-binary "$1"; }

cd "$(dirname "$0")/.."
go build -o bin/cbioportal-import ./cmd/cbioportal-import

in_list=$(printf "'%s'," "${STUDIES[@]}"); in_list=${in_list%,}
found=$(source_query "SELECT count() FROM $SOURCE_DB.cancer_study WHERE study_id IN ($in_list) FORMAT TSV")
# Rows without a study (gene panels from `load-gene-panels`) are shared by
# every study and copied along.
rows="study_id IN ($in_list, '')"
[ "$found" -eq "${#STUDIES[@]}" ] || { log "only $found of ${#STUDIES[@]} studies are staged in $SOURCE_DB"; exit 1; }

log "copying ${STUDIES[*]} from $SOURCE_DB to $RAW_DB"
for table in $(source_query "SELECT name FROM system.tables WHERE database = '$SOURCE_DB' AND engine LIKE '%MergeTree' ORDER BY name FORMAT TSV"); do
  ddl=$(source_query "SHOW CREATE TABLE $SOURCE_DB.$table FORMAT TSVRaw")
  ddl=${ddl/CREATE TABLE $SOURCE_DB.$table/CREATE TABLE IF NOT EXISTS $RAW_DB.$table}
  target "" "$ddl" >/dev/null
  # Columns added to the source table by later ingests (schema evolution).
  add=$(source_query "SELECT arrayStringConcat(groupArray(concat('ADD COLUMN IF NOT EXISTS \`', name, '\` ', type)), ', ') FROM (SELECT name, type FROM system.columns WHERE database = '$SOURCE_DB' AND table = '$table' ORDER BY position) FORMAT TSVRaw")
  target "" "ALTER TABLE $RAW_DB.$table $add" >/dev/null
  target "" "DELETE FROM $RAW_DB.$table WHERE $rows" >/dev/null
  cols=$(source_query "SELECT arrayStringConcat(groupArray(concat('\`', name, '\`')), ', ') FROM (SELECT name FROM system.columns WHERE database = '$SOURCE_DB' AND table = '$table' ORDER BY position) FORMAT TSVRaw")
  source_query "SELECT $cols FROM $SOURCE_DB.$table WHERE $rows FORMAT Native" |
    target "INSERT INTO $RAW_DB.$table ($cols) FORMAT Native" >/dev/null
  log "  $table: $(target "" "SELECT count() FROM $RAW_DB.$table WHERE $rows FORMAT TSV") rows"
done

log "loading reference data into $REF_DB"
bin/cbioportal-import load-reference "$SEED" --db "$REF_DB"

log "building $PREFIX"
bin/cbioportal-import transform --raw-db "$RAW_DB" --db "$PREFIX" --ref-db "$REF_DB"
log "done: serve with -db $PREFIX -raw-db $RAW_DB -ref-db $REF_DB"
