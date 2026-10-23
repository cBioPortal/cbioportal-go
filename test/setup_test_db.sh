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
# ascn_test_study is imported with a license (meta_study `license:`, as the
# datahub studies of cBioPortal/datahub#2372 carry one).
LICENSED="$(mktemp -d)"
trap 'rm -rf "$LICENSED"' EXIT
cp -r "$FIXTURES/data/studies/ascn-test-study" "$LICENSED/"
echo "license: CC-BY-NC-ND-4.0" >> "$LICENSED/ascn-test-study/meta_study.txt"
for s in "${STUDIES[@]}"; do
  dir="$FIXTURES/data/studies/$s"
  [ "$s" = ascn-test-study ] && dir="$LICENSED/$s"
  bin/cbioportal-import ingest "$dir" --db "$RAW_DB" --url "$CH"
done
# study_hg38 is mid-(re)import (cancer_study.status 0), as in upstream CI's
# localdb database: the e2e portal answers 423 for it (study_availability).
curl -sf "$CH" --data-binary "ALTER TABLE \`$RAW_DB\`.cancer_study ADD COLUMN IF NOT EXISTS status String"
curl -sf "$CH" --data-binary "ALTER TABLE \`$RAW_DB\`.cancer_study UPDATE status = '0' WHERE study_id = 'study_hg38' SETTINGS mutations_sync = 1"
bin/cbioportal-import transform --raw-db "$RAW_DB" --db "$DB" --ref-db "$REF_DB" --url "$CH"
