#!/usr/bin/env bash
# Builds the databases the contract and e2e tests run against: the reference
# data from the cBioPortal seed dump plus the e2e fixture studies of
# cBioPortal/cbioportal-test.
#
#   test/setup_test_db.sh <cbioportal-test checkout> <seed.sql.gz>
#
# Env: RAW_DB (default cbioportal_e2e_raw), DB (cbioportal_e2e),
# REF_DB (cbioportal_ref), CLICKHOUSE_URL (http://localhost:18123).
set -euo pipefail

FIXTURES="${1:?usage: test/setup_test_db.sh <cbioportal-test checkout> <seed.sql.gz>}"
SEED="${2:?usage: test/setup_test_db.sh <cbioportal-test checkout> <seed.sql.gz>}"
RAW_DB="${RAW_DB:-cbioportal_e2e_raw}"
DB="${DB:-cbioportal_e2e}"
REF_DB="${REF_DB:-cbioportal_ref}"
CH="${CLICKHOUSE_URL:-http://localhost:18123}"
STUDIES=(ascn-test-study study_hg38 genepanel_test_study study_es_0 lgg_ucsf_2014_test_generic_assay)

cd "$(dirname "$0")/.."
go build -o bin/cbioportal-import ./cmd/cbioportal-import

bin/cbioportal-import load-reference "$SEED" --db "$REF_DB" --url "$CH"
GS="$FIXTURES/data/genesets"
bin/cbioportal-import load-genesets "$GS/study_es_0_genesets.gmt" --version msigdb_7.5.1 \
  --supp "$GS/study_es_0_supp-genesets.txt" --hierarchy "$GS/study_es_0_tree.yaml" --db "$REF_DB" --url "$CH"
for s in "${STUDIES[@]}"; do
  bin/cbioportal-import ingest "$FIXTURES/data/studies/$s" --db "$RAW_DB" --url "$CH"
done
bin/cbioportal-import transform --raw-db "$RAW_DB" --db "$DB" --ref-db "$REF_DB" --url "$CH"
