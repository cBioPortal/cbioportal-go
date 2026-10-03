#!/usr/bin/env bash
# Runs the Java backend on cBioPortal's e2e fixture studies, as the reference
# for test/parity/fixtures.txt (docs/testing.md, "Java oracle"):
#
#   test/java_oracle.sh db <cbioportal-test checkout>   # build ClickHouse database java_e2e
#   test/java_oracle.sh start                          # Java API on :18095
#   test/java_oracle.sh stop
#
# The database is built the way the cbioportal/cbioportal image's own tools
# build one: its ClickHouse schema and seed, cbioportal-core's importer for
# the gene panels, gene sets and studies, then its derived-table script.
# Env: IMAGE (cbioportal/cbioportal:master), CLICKHOUSE_URL
# (http://localhost:18123; reached from the containers over the host
# network), CLICKHOUSE_CONTAINER (the ClickHouse server container, for
# clickhouse-client), JAVA_PORT (18095), PORTAL_PROPERTIES (the frontend
# submodule's e2e portal.properties).
set -euo pipefail
IMAGE=${IMAGE:-cbioportal/cbioportal:master}
CH=${CLICKHOUSE_URL:-http://localhost:18123}
CH_CONTAINER=${CLICKHOUSE_CONTAINER:-ch-dge}
PORT=${JAVA_PORT:-18095}
DB=java_e2e
WORK="$(cd "$(dirname "$0")/.." && pwd)/bin/java-oracle"
mkdir -p "$WORK"
chq() { curl -sf "$CH" --data-binary "$1"; }

case "${1:-}" in
db)
  FIX="$(cd "${2:?usage: test/java_oracle.sh db <cbioportal-test checkout>}" && pwd)/data"
  chq "DROP DATABASE IF EXISTS $DB"
  chq "CREATE DATABASE $DB"
  chq "CREATE USER IF NOT EXISTS java_oracle IDENTIFIED WITH plaintext_password BY 'oracle'"
  chq "GRANT ALL ON $DB.* TO java_oracle"
  rm -rf "$WORK/scripts"
  docker create --name java-oracle-extract "$IMAGE" >/dev/null
  docker cp java-oracle-extract:/cbioportal/db-scripts/clickhouse "$WORK/scripts"
  docker rm java-oracle-extract >/dev/null
  docker exec -i "$CH_CONTAINER" clickhouse-client --database $DB --multiquery < "$WORK/scripts/init/schema.sql"
  gunzip -c "$WORK/scripts/init/"seed-*.sql.gz | docker exec -i "$CH_CONTAINER" clickhouse-client --database $DB --multiquery
  host=$(echo "$CH" | sed -E 's#^https?://##')
  mkdir -p "$WORK/portalhome"
  printf 'spring.datasource.url=jdbc:clickhouse://%s/%s\nspring.datasource.username=java_oracle\nspring.datasource.password=oracle\nspring.datasource.driver-class-name=com.clickhouse.jdbc.ClickHouseDriver\n' \
    "$host" "$DB" > "$WORK/portalhome/application.properties"
  # As cbioportal-test's import-data.sh: panels and gene sets, then the studies.
  docker run --rm --network host -v "$FIX/studies:/study:ro" -v "$FIX/genesets:/genesets:ro" \
    -v "$WORK/portalhome:/portalhome:ro" -e PORTAL_HOME=/portalhome -e JAVA_OPTS=-Xmx1g \
    --entrypoint bash "$IMAGE" -c '
set -e
cd /core/scripts
./importGenePanel.pl --data /study/study_es_0/data_gene_panel_testpanel1.txt
./importGenePanel.pl --data /study/study_es_0/data_gene_panel_testpanel2.txt
./importGenesetData.pl --data /genesets/study_es_0_genesets.gmt --new-version msigdb_7.5.1 \
  --sup /genesets/study_es_0_supp-genesets.txt --confirm-delete-all-genesets-hierarchy-genesetprofiles
./importGenesetHierarchy.pl --data /genesets/study_es_0_tree.yaml
for s in ascn-test-study study_hg38 genepanel_test_study study_es_0 lgg_ucsf_2014_test_generic_assay; do
  python3 -m importer.metaImport -s /study/$s -n -o --no-derive-tables
done'
  docker exec -i "$CH_CONTAINER" clickhouse-client --database $DB --multiquery --param_optimize_backoff_secs=0 \
    < "$WORK/scripts/populate_derived_tables.sql"
  ;;
start)
  props=${PORTAL_PROPERTIES:-"$(cd "$(dirname "$0")/.." && pwd)/frontend/end-to-end-test/local/runtime-config/portal.properties"}
  host=$(echo "$CH" | sed -E 's#^https?://##')
  # The e2e portal properties, pointed at java_e2e.
  sed -E -e "s#^(spring\.datasource(\.[a-z]+)?\.url)=.*#\1=jdbc:clickhouse://$host/$DB#" \
    -e 's#^(spring\.datasource(\.[a-z]+)?\.username)=.*#\1=java_oracle#' \
    -e 's#^(spring\.datasource(\.[a-z]+)?\.password)=.*#\1=oracle#' \
    -e 's#^(spring\.datasource\.driver-class-name)=.*#\1=com.clickhouse.jdbc.ClickHouseDriver#' \
    "$props" > "$WORK/application.properties"
  docker run -d --name java-oracle --network host -v "$WORK/application.properties:/cbioportal-webapp/application.properties:ro" \
    --entrypoint java "$IMAGE" -Xms512m -Xmx2g -cp '/cbioportal-webapp:/cbioportal-webapp/lib/*' org.cbioportal.PortalApplication \
    --spring.config.location=file:/cbioportal-webapp/application.properties --authenticate=false --session.service.url= \
    --clickhouse_mode=true --spring.profiles.active=clickhouse --server.port=$PORT
  timeout 300 bash -c "until curl -sf -o /dev/null localhost:$PORT/api/info; do sleep 3; done"
  echo "Java backend on :$PORT"
  ;;
stop)
  # `docker stop` can be refused under snap's AppArmor profile; end PID 1.
  docker exec java-oracle sh -c 'kill 1' 2>/dev/null || docker stop java-oracle
  docker wait java-oracle >/dev/null
  docker rm java-oracle
  ;;
*)
  sed -n '2,7p' "$0"
  exit 1
  ;;
esac
