#!/usr/bin/env bash
# Times one study import with the Java importer (cbioportal-core in the
# cbioportal/cbioportal image) or go's, each into its own empty ClickHouse
# database with reference data and gene panels already loaded
# (docs/rewrite/benchmark.md, "Import"):
#
#   scripts/import-benchmark.sh fetch <dir> public/<study>...   # datahub master
#   scripts/import-benchmark.sh java <study-dir> <panels-dir> <tag>
#   scripts/import-benchmark.sh go   <study-dir> <panels-dir> <tag>
#   scripts/import-benchmark.sh counts <db>
#   scripts/import-benchmark.sh clean                 # drop every bench_* database
#
# `fetch` checks out datahub's master (DATAHUB_REF) into <dir>/datahub with
# `cbioportal-import fetch-datahub` (LFS files from datahub's own store
# only), and writes <dir>/panels-<study> with the gene panels each study
# uses. Keep <dir> outside /tmp: snap Docker cannot see it.
#
# Java: validateData.py, then cbioportalImporter.py (one JVM per data file)
# in a container capped at 8 GiB, -Xmx6g; then populate_derived_tables.sql
# through clickhouse-client. go: `ingest` and `transform` under a 4 GiB
# systemd scope. Peak RSS is GNU time's maximum resident set size (the
# largest process: for Java, the biggest JVM). Results append to
# bin/import-benchmark/results.tsv.
# Env: SEED (go: datahub's seedDB/seed-cbioportal_hg19_hg38_v2.14.5.sql.gz,
# in `fetch`'s checkout; the MySQL dump of the seed in Java's image), IMAGE
# (cbioportal/cbioportal:v7.1.2), CLICKHOUSE_URL (http://localhost:18123),
# CLICKHOUSE_CONTAINER (ch-dge), JAVA_XMX (6g).
set -euo pipefail
IMAGE=${IMAGE:-cbioportal/cbioportal:v7.1.2}
CH=${CLICKHOUSE_URL:-http://localhost:18123}
CH_CONTAINER=${CLICKHOUSE_CONTAINER:-ch-dge}
XMX=${JAVA_XMX:-6g}
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$ROOT/bin/import-benchmark"
RESULTS="$WORK/results.tsv"
mkdir -p "$WORK"
chq() { curl -sfS "$CH" --data-binary "$1"; }
now() { date +%s.%N; }
secs() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.1f", b - a }'; }
maxrss() { awk -F': ' '/Maximum resident set size/ { printf "%d", $2 / 1024 }' "$1"; }
record() { printf '%s\n' "$(IFS=$'\t'; echo "$*")" >> "$RESULTS"; }

# Java's ClickHouse scripts (schema, seed, derived tables), from the image.
scripts() {
  if [ ! -d "$WORK/scripts" ]; then
    docker create --name import-bench-extract "$IMAGE" >/dev/null
    docker cp import-bench-extract:/cbioportal/db-scripts/clickhouse "$WORK/scripts"
    docker rm import-bench-extract >/dev/null
  fi
  # GNU time for the container; snap Docker cannot mount /usr/bin.
  cp /usr/bin/time "$WORK/time"
}

# Main tables of each model, as a check that both imported the same data.
counts() {
  chq "SELECT name, total_rows FROM system.tables WHERE database = '$1' AND engine NOT LIKE '%View%'
       AND name IN ('patient', 'sample', 'clinical_patient', 'clinical_sample', 'mutation', 'mutation_event',
                    'sample_cna_event', 'cna_event', 'structural_variant', 'copy_number_seg', 'genetic_alteration',
                    'clinical_event', 'clinical_event_data', 'genomic_event_derived', 'clinical_data_derived',
                    'clinical_event_derived', 'sample_derived', 'genetic_alteration_derived',
                    'generic_assay_data_derived', 'sample_to_gene_panel_derived')
       ORDER BY name FORMAT TSV"
}

case "${1:-}" in
fetch)
  WORKDIR="$(mkdir -p "$2" && cd "$2" && pwd)" REPO="$(cd "$2" && pwd)/datahub"
  shift 2
  (cd "$ROOT" && go run ./cmd/cbioportal-import fetch-datahub "$REPO" --ref "${DATAHUB_REF:-master}" "$@")
  for s in "$@"; do
    panels="$WORKDIR/panels-$(basename "$s")"
    rm -rf "$panels"
    mkdir -p "$panels"
    { sed -n 's/^gene_panel:[[:space:]]*//p' "$REPO/$s"/meta_*.txt
      for m in "$REPO/$s"/data_gene_panel_matrix*.txt; do
        if [ -f "$m" ]; then tail -n +2 "$m" | cut -f2- | tr '\t' '\n'; fi
      done
    } | tr -d '\r' | grep -v -x -e '' -e NA | sort -u | while read -r id; do
      cp "$(grep -l -E "^stable_id:[[:space:]]*$id[[:space:]]*\$" "$REPO"/reference_data/gene_panels/*.txt | head -1)" "$panels/"
    done
    echo "$s: $(ls "$panels" | wc -l) gene panels" >&2
  done
  ;;
java)
  STUDY="$(cd "$2" && pwd)" PANELS="$(cd "$3" && pwd)" DB=bench_java_$4
  scripts
  chq "DROP DATABASE IF EXISTS $DB"
  chq "CREATE DATABASE $DB"
  chq "CREATE USER IF NOT EXISTS import_bench IDENTIFIED WITH plaintext_password BY 'bench'"
  chq "GRANT ALL ON $DB.* TO import_bench"
  docker exec -i "$CH_CONTAINER" clickhouse-client --database "$DB" --multiquery < "$WORK/scripts/init/schema.sql"
  gunzip -c "$WORK/scripts/init/"seed-*.sql.gz | docker exec -i "$CH_CONTAINER" clickhouse-client --database "$DB" --multiquery
  host=$(echo "$CH" | sed -E 's#^https?://##')
  mkdir -p "$WORK/portalhome-$DB"
  printf 'spring.datasource.url=jdbc:clickhouse://%s/%s\nspring.datasource.username=import_bench\nspring.datasource.password=bench\nspring.datasource.driver-class-name=com.clickhouse.jdbc.ClickHouseDriver\n' \
    "$host" "$DB" > "$WORK/portalhome-$DB/application.properties"
  run() {
    docker run --rm --network host --memory 8g --memory-swap 8g --user "$(id -u):$(id -g)" \
      -v "$STUDY:/study:ro" -v "$PANELS:/panels:ro" -v "$WORK/portalhome-$DB:/portalhome:ro" -v "$WORK:/out" \
      -v "$WORK/time:/usr/bin/time:ro" -e PORTAL_HOME=/portalhome -e JAVA_OPTS="-Xmx$XMX" \
      -w /core/scripts --entrypoint bash "$IMAGE" -c "$1"
  }
  # Setup, untimed: the gene panels the study uses.
  run 'set -e; for p in /panels/*.txt; do ./importGenePanel.pl --data "$p" >/dev/null; done'
  log="$WORK/$DB"
  t0=$(now)
  run "/usr/bin/time -v -o /out/$DB.validate.time python3 -m importer.validateData -s /study -n >/out/$DB.validate.log 2>&1" || true
  t1=$(now)
  run "set -o pipefail; /usr/bin/time -v -o /out/$DB.import.time python3 -m importer.cbioportalImporter -s /study 2>&1 | tail -n 200 >/out/$DB.import.log"
  t2=$(now)
  docker exec -i "$CH_CONTAINER" clickhouse-client --database "$DB" --multiquery --param_optimize_backoff_secs=0 \
    --log_comment=import-bench-derived < "$WORK/scripts/populate_derived_tables.sql"
  t3=$(now)
  record "$(date -u +%FT%TZ)" java "$(basename "$STUDY")" "$DB" "$(secs "$t1" "$t2")" "$(secs "$t2" "$t3")" \
    "$(maxrss "$log.import.time")" "validate $(secs "$t0" "$t1")s $(maxrss "$log.validate.time")MB"
  tail -1 "$RESULTS"
  counts "$DB"
  ;;
go)
  STUDY="$(cd "$2" && pwd)" PANELS="$(cd "$3" && pwd)" DB=bench_go_$4
  scripts
  BIN="$ROOT/bin/cbioportal-import"
  for d in "$DB" "${DB}_raw" "${DB}_ref"; do chq "DROP DATABASE IF EXISTS $d"; chq "CREATE DATABASE $d"; done
  # Setup, untimed: genes and cancer types from the same seed as Java's, and the gene panels.
  "$BIN" load-reference "${SEED:?set SEED to a datahub seedDB/seed-*.sql.gz}" --url "$CH" --db "${DB}_ref"
  "$BIN" load-gene-panels "$PANELS" --url "$CH" --db "${DB}_raw"
  capped() { systemd-run --user --scope --quiet -p MemoryMax=4G -p MemorySwapMax=0 /usr/bin/time -v -o "$@"; }
  log="$WORK/$DB"
  t0=$(now)
  capped "$log.ingest.time" "$BIN" ingest "$STUDY" --url "$CH" --db "${DB}_raw" > "$log.ingest.log"
  t1=$(now)
  capped "$log.transform.time" "$BIN" transform --url "$CH" --raw-db "${DB}_raw" --db "$DB" --ref-db "${DB}_ref" > "$log.transform.log"
  t2=$(now)
  record "$(date -u +%FT%TZ)" go "$(basename "$STUDY")" "$DB" "$(secs "$t0" "$t1")" "$(secs "$t1" "$t2")" \
    "$(maxrss "$log.ingest.time")" "transform $(maxrss "$log.transform.time")MB"
  tail -1 "$RESULTS"
  counts "$DB"
  ;;
counts)
  counts "$2"
  ;;
clean)
  for d in $(chq "SELECT name FROM system.databases WHERE name LIKE 'bench\\_java\\_%' OR name LIKE 'bench\\_go\\_%'"); do
    chq "DROP DATABASE $d SYNC"
    echo "dropped $d"
  done
  chq "DROP USER IF EXISTS import_bench"
  ;;
*)
  sed -n '2,11p' "$0"
  exit 1
  ;;
esac
