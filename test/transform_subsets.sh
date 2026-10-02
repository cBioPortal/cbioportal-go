#!/usr/bin/env bash
# Imports single studies (and one two-study mix) into fresh scratch databases,
# transforms them, starts the API over each result and asserts that a broad set
# of endpoints never answers 5xx, nor a JSON object with a null property (the
# Java backend serializes NON_NULL). Guards the transform's promise that every
# object the API reads exists for any subset of data types.
#
# Usage: test/transform_subsets.sh [case ...]
#   case = name=dir[,dir...]; default: the cases below.
# Env: CLICKHOUSE_URL (default http://localhost:18123), REF_DB (cbioportal_ref,
# read only), PORT (default 18101), KEEP_DBS=1 keeps the tsub_* databases.
# API_URL=http://host:port exercises that running API once instead (no
# import). Skips (exit 0) when ClickHouse is unreachable.
set -uo pipefail

CH=${CLICKHOUSE_URL:-http://localhost:18123}
REF_DB=${REF_DB:-cbioportal_ref}
PORT=${PORT:-18101}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
DATAHUB=${DATAHUB:-$HOME/git/datahub/public}
FIXTURES=${FIXTURES:-$HOME/git/cbioportal-test/data/studies}

if ! curl -sf "$CH/ping" >/dev/null; then
  echo "SKIP: ClickHouse unreachable at $CH"
  exit 0
fi

if [ $# -gt 0 ]; then
  CASES=("$@")
else
  CASES=(
    "acbc=$DATAHUB/acbc_mskcc_2015"
    "ascn=$FIXTURES/ascn-test-study"
    "hg38=$FIXTURES/study_hg38"
    "genepanel=$FIXTURES/genepanel_test_study"
    "es0=$FIXTURES/study_es_0"
    "lgg_ga=$FIXTURES/lgg_ucsf_2014_test_generic_assay"
    "mix=$DATAHUB/acbc_mskcc_2015,$FIXTURES/lgg_ucsf_2014_test_generic_assay"
  )
fi

WORK=$(mktemp -d "${TMPDIR:-/tmp}/tsub.XXXXXX")
API_PID=""
DBS=()

chq() { curl -sS "$CH/" --data-binary "$1"; }

cleanup() {
  if [ -n "$API_PID" ]; then kill "$API_PID" 2>/dev/null; wait "$API_PID" 2>/dev/null; fi
  if [ "${KEEP_DBS:-}" != 1 ]; then
    for db in "${DBS[@]}"; do chq "DROP DATABASE IF EXISTS \`$db\`" >/dev/null; done
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

if [ -z "${API_URL:-}" ]; then
  echo "building into $WORK"
  (cd "$ROOT" && GOTOOLCHAIN=local go build -p 2 -o "$WORK/cbioportal-import" ./cmd/cbioportal-import &&
    GOTOOLCHAIN=local go build -p 2 -o "$WORK/cbioportal-api" ./cmd/cbioportal-api) || exit 1
fi

BASE=""
CASE_HITS=0
CASE_FAILS=()

# hit METHOD PATH [JSON]: records the call; any 5xx (or no answer), or a JSON
# null property in the answer, fails it. /config_service is a property map,
# whose null values Java writes.
hit() {
  local method=$1 path=$2 body=${3:-} code
  if [ "$method" = GET ]; then
    code=$(curl -s -o "$WORK/resp" -w '%{http_code}' --max-time 120 "$BASE$path")
  else
    code=$(curl -s -o "$WORK/resp" -w '%{http_code}' --max-time 120 -X "$method" \
      -H 'Content-Type: application/json' --data-binary "$body" "$BASE$path")
  fi
  CASE_HITS=$((CASE_HITS + 1))
  if [ "$code" = 000 ] || [ "$code" -ge 500 ]; then
    CASE_FAILS+=("$code $method $path :: $(head -c 300 "$WORK/resp" | tr '\n' ' ')")
  elif [ "$path" != /config_service ] && grep -q '":null' "$WORK/resp" &&
    jq -e '[.. | objects | to_entries[] | select(.value == null)] | length > 0' "$WORK/resp" >/dev/null 2>&1; then
    CASE_FAILS+=("null $method $path :: $(jq -c '[paths(. == null)][:3]' "$WORK/resp")")
  fi
}

# get PATH JQ: GET a path and extract values (empty on failure).
get() { curl -s --max-time 120 "$BASE$1" | jq -r "$2" 2>/dev/null; }

json_list() { jq -cn '$ARGS.positional' --args "$@"; }

exercise() {
  local studies=() s
  mapfile -t studies < <(get /api/studies '.[].studyId')
  hit GET /
  hit GET /api/studies
  hit GET /api/cancer-types
  hit GET /api/info
  hit GET /config_service
  hit GET /api/molecular-profiles
  hit GET /api/sample-lists
  hit GET /api/gene-panels
  hit GET /api/clinical-attributes
  hit GET /api/public_virtual_studies
  [ ${#studies[@]} -eq 0 ] && { CASE_FAILS+=("no studies listed"); return; }
  local sids; sids=$(json_list "${studies[@]}")
  local filter="{\"studyIds\":$sids}"
  hit POST /api/studies/fetch "$sids"
  hit POST '/api/studies/fetch?projection=DETAILED' "$sids"
  hit POST /api/studies/tags/fetch "$sids"
  hit POST /api/clinical-attributes/fetch "$sids"
  hit POST /api/clinical-attributes/counts/fetch "{\"sampleListIds\":[\"${studies[0]}_all\"]}"
  hit POST /api/resource-definitions/fetch "$sids"
  hit POST /api/molecular-profiles/fetch "$filter"

  # Study-view endpoints over every study at once.
  hit POST /api/filtered-samples/fetch "$filter"
  hit POST /api/clinical-data-counts/fetch "{\"attributes\":[{\"attributeId\":\"CANCER_TYPE\"},{\"attributeId\":\"SAMPLE_COUNT\"},{\"attributeId\":\"MUTATION_COUNT\"}],\"studyViewFilter\":$filter}"
  hit POST '/api/clinical-data-bin-counts/fetch?dataBinMethod=STATIC' "{\"attributes\":[{\"attributeId\":\"AGE\"},{\"attributeId\":\"MUTATION_COUNT\"},{\"attributeId\":\"FRACTION_GENOME_ALTERED\"}],\"studyViewFilter\":$filter}"
  hit POST /api/clinical-data-table/fetch "{\"studyIds\":$sids}"
  hit POST /api/mutated-genes/fetch "$filter"
  hit POST /api/cna-genes/fetch "$filter"
  hit POST /api/structuralvariant-genes/fetch "$filter"
  hit POST /api/structuralvariant-counts/fetch "$filter"
  hit POST /api/molecular-profile-sample-counts/fetch "$filter"
  hit POST /api/sample-lists-counts/fetch "$filter"
  hit POST /api/clinical-event-type-counts/fetch "$filter"
  hit POST /api/treatments/patient-counts/fetch "$filter"
  hit POST /api/treatments/sample-counts/fetch "$filter"
  hit POST /api/treatments/display-patient "$sids"
  hit POST /api/treatments/display-sample "$sids"
  hit POST /api/treatments/patient "$filter"
  hit POST /api/treatments/sample "$filter"
  hit POST /api/custom-driver-annotation-report/fetch "$(json_list $(get /api/molecular-profiles '.[].molecularProfileId'))"
  hit POST /api/genomic-data-counts/fetch "{\"genomicDataFilters\":[{\"hugoGeneSymbol\":\"TP53\",\"profileType\":\"gistic\"}],\"studyViewFilter\":$filter}"
  hit POST /api/genomic-data-bin-counts/fetch "{\"genomicDataBinFilters\":[{\"hugoGeneSymbol\":\"TP53\",\"profileType\":\"mrna\"}],\"studyViewFilter\":$filter}"
  hit POST /api/mutation-data-counts/fetch "{\"genomicDataFilters\":[{\"hugoGeneSymbol\":\"TP53\",\"profileType\":\"mutations\"}],\"studyViewFilter\":$filter}"
  hit POST /api/namespace-attributes/fetch "{\"studyIds\":$sids}"
  hit POST /api/survival-data/fetch "{\"attributeIdPrefix\":\"OS\",\"studyViewFilter\":$filter}"
  # Results-view calls the frontend makes whatever profiles the studies have.
  hit POST /api/mutation-counts-by-position/fetch '[{"entrezGeneId":7157,"proteinPosStart":1,"proteinPosEnd":400}]'
  hit GET /api/genesets
  hit GET /api/genesets/HALLMARK_APOPTOSIS
  hit POST /api/genesets/fetch '["HALLMARK_APOPTOSIS"]'
  hit POST /api/geneset-hierarchy/fetch "[]"
  hit POST /api/generic-assay-meta/fetch '{"genericAssayStableIds":["x"]}'
  hit POST /api/generic_assay_meta/generic-assay/fetch '{"genericAssayStableIds":["x"]}'

  for s in "${studies[@]}"; do
    local samples=() patients=() profiles=() lists=()
    mapfile -t samples < <(get "/api/studies/$s/samples" '.[].sampleId')
    mapfile -t patients < <(get "/api/studies/$s/patients" '.[].patientId')
    mapfile -t lists < <(get "/api/studies/$s/sample-lists" '.[].sampleListId')
    hit GET "/api/studies/$s"
    hit GET "/api/studies/$s/samples"
    hit GET "/api/studies/$s/patients"
    hit GET "/api/studies/$s/clinical-data"
    hit GET "/api/studies/$s/clinical-data?clinicalDataType=PATIENT"
    hit GET "/api/studies/$s/clinical-attributes"
    hit GET "/api/studies/$s/molecular-profiles"
    hit GET "/api/studies/$s/sample-lists"
    hit GET "/api/studies/$s/clinical-events"
    hit GET "/api/studies/$s/resource-definitions"
    hit GET "/api/studies/$s/resource-data"
    hit GET "/api/studies/$s/resource-data-all"
    hit GET "/api/studies/$s/significant-copy-number-regions"
    hit GET "/api/studies/$s/significantly-mutated-genes"
    hit GET "/api/studies/$s/tags"
    hit POST "/api/studies/$s/clinical-data/fetch" '{"attributeIds":["CANCER_TYPE","AGE","OS_STATUS"]}'
    hit POST /api/samples/fetch "{\"sampleListIds\":[\"${s}_all\"]}"
    hit POST '/api/samples/fetch?projection=DETAILED' "{\"sampleListIds\":[\"${s}_all\"]}"
    hit POST /api/sample-lists/fetch "$(json_list "${lists[@]}")"
    for l in "${lists[@]:0:3}"; do
      hit GET "/api/sample-lists/$l"
      hit GET "/api/sample-lists/$l/sample-ids"
    done
    if [ ${#patients[@]} -gt 0 ]; then
      local p=${patients[0]}
      hit GET "/api/studies/$s/patients/$p"
      hit GET "/api/studies/$s/patients/$p/samples"
      hit GET "/api/studies/$s/patients/$p/clinical-data"
      hit GET "/api/studies/$s/patients/$p/clinical-events"
      hit GET "/api/studies/$s/patients/$p/resource-data"
      hit POST /api/patients/fetch "{\"patientIdentifiers\":[{\"studyId\":\"$s\",\"patientId\":\"$p\"}]}"
      hit POST /api/clinical-events-meta/fetch "{\"patientIdentifiers\":[{\"studyId\":\"$s\",\"patientId\":\"$p\"}]}"
      hit POST /api/clinical-data/fetch?clinicalDataType=PATIENT "{\"identifiers\":[{\"entityId\":\"$p\",\"studyId\":\"$s\"}]}"
    fi
    if [ ${#samples[@]} -gt 0 ]; then
      local sm=${samples[0]}
      hit GET "/api/studies/$s/samples/$sm"
      hit GET "/api/studies/$s/samples/$sm/clinical-data"
      hit GET "/api/studies/$s/samples/$sm/copy-number-segments"
      hit GET "/api/studies/$s/samples/$sm/resource-data"
      hit POST /api/clinical-data/fetch?clinicalDataType=SAMPLE "{\"identifiers\":[{\"entityId\":\"$sm\",\"studyId\":\"$s\"}]}"
      hit POST /api/copy-number-segments/fetch "[{\"studyId\":\"$s\",\"sampleId\":\"$sm\"}]"
    fi

    local sl="${s}_all" mp kind
    mapfile -t profiles < <(get "/api/studies/$s/molecular-profiles" '.[] | "\(.molecularProfileId) \(.molecularAlterationType)"')
    local genes='[7157,1956,672,2064,4609]'
    local ids; ids=$(json_list $(printf '%s\n' "${profiles[@]}" | cut -d' ' -f1))
    hit POST /api/gene-panel-data/fetch "{\"molecularProfileIds\":$ids}"
    hit POST /api/molecular-data/fetch "{\"entrezGeneIds\":$genes,\"molecularProfileIds\":$ids}"
    hit POST /api/mutations/fetch "{\"entrezGeneIds\":$genes,\"molecularProfileIds\":$ids}"
    for row in "${profiles[@]}"; do
      mp=${row%% *}; kind=${row#* }
      hit GET "/api/molecular-profiles/$mp"
      hit POST "/api/molecular-profiles/$mp/gene-panel-data/fetch" "{\"sampleListId\":\"$sl\"}"
      case $kind in
        MUTATION_EXTENDED|MUTATION_UNCALLED)
          hit GET "/api/molecular-profiles/$mp/mutations?sampleListId=$sl"
          hit POST "/api/molecular-profiles/$mp/mutations/fetch" "{\"sampleListId\":\"$sl\",\"entrezGeneIds\":$genes}"
          hit POST '/api/mutations/fetch?projection=DETAILED' "{\"sampleMolecularIdentifiers\":[{\"molecularProfileId\":\"$mp\",\"sampleId\":\"${samples[0]:-x}\"}],\"entrezGeneIds\":$genes}"
          hit POST "/api/molecular-profiles/$mp/mutation-spectrums/fetch" "{\"sampleListId\":\"$sl\"}"
          hit POST "/api/molecular-profiles/$mp/variant-counts/fetch" "{\"entrezGeneIds\":[7157],\"variants\":[\"R273H\"]}"
          hit POST /api/alteration-enrichments/fetch "[{\"name\":\"a\",\"molecularProfileCaseIdentifiers\":[{\"caseId\":\"${samples[0]:-x}\",\"molecularProfileId\":\"$mp\"}]},{\"name\":\"b\",\"molecularProfileCaseIdentifiers\":[{\"caseId\":\"${samples[1]:-x}\",\"molecularProfileId\":\"$mp\"}]}]"
          ;;
        COPY_NUMBER_ALTERATION)
          hit GET "/api/molecular-profiles/$mp/discrete-copy-number?sampleListId=$sl"
          hit POST "/api/molecular-profiles/$mp/discrete-copy-number/fetch" "{\"sampleListId\":\"$sl\",\"entrezGeneIds\":$genes}"
          hit POST "/api/molecular-profiles/$mp/discrete-copy-number-counts/fetch" "{\"entrezGeneIds\":$genes}"
          hit POST "/api/molecular-profiles/$mp/molecular-data/fetch" "{\"sampleListId\":\"$sl\",\"entrezGeneIds\":$genes}"
          hit POST "/api/molecular-profiles/co-expressions/fetch?molecularProfileIdA=$mp&molecularProfileIdB=$mp" "{\"entrezGeneId\":7157,\"sampleListId\":\"$sl\"}"
          ;;
        STRUCTURAL_VARIANT)
          hit POST /api/structural-variant/fetch "{\"molecularProfileIds\":[\"$mp\"],\"entrezGeneIds\":$genes}"
          hit POST /api/structural-variant/fetch "{\"sampleMolecularIdentifiers\":[{\"molecularProfileId\":\"$mp\",\"sampleId\":\"${samples[0]:-x}\"}]}"
          ;;
        GENERIC_ASSAY)
          local ent
          hit GET "/api/generic-assay-meta/$mp"
          hit POST /api/generic-assay-meta/fetch "{\"molecularProfileIds\":[\"$mp\"]}"
          ent=$(get "/api/generic-assay-meta/$mp" '.[0].stableId // empty')
          hit POST "/api/generic_assay_data/$mp/fetch" "{\"sampleListId\":\"$sl\"}"
          hit POST /api/generic_assay_data/fetch "{\"molecularProfileIds\":[\"$mp\"]}"
          hit POST /api/generic-assay-data-counts/fetch "{\"genericAssayDataFilters\":[{\"stableId\":\"${ent:-x}\",\"profileType\":\"${mp#"${s}_"}\"}],\"studyViewFilter\":{\"studyIds\":[\"$s\"]}}"
          hit POST /api/generic-assay-data-bin-counts/fetch "{\"genericAssayDataBinFilters\":[{\"stableId\":\"${ent:-x}\",\"profileType\":\"${mp#"${s}_"}\"}],\"studyViewFilter\":{\"studyIds\":[\"$s\"]}}"
          [ -n "$ent" ] && hit GET "/api/generic-assay-data/$mp/generic-assay/$ent"
          [ -n "$ent" ] && hit GET "/api/generic-assay-meta/generic-assay/$ent"
          ;;
        GENESET_SCORE)
          local gs
          gs=$(get "/api/genesets" '.[0].genesetId // empty')
          hit POST "/api/geneset-hierarchy/fetch?geneticProfileId=$mp&sampleListId=$sl" ""
          hit POST "/api/genetic-profiles/$mp/geneset-genetic-data/fetch" "{\"sampleListId\":\"$sl\"}"
          hit POST "/api/genesets/${gs:-x}/expression-correlation/fetch?geneticProfileId=$mp" ""
          hit POST "/api/molecular-profiles/co-expressions/fetch?molecularProfileIdA=$mp&molecularProfileIdB=$mp" "{\"genesetId\":\"${gs:-x}\",\"sampleListId\":\"$sl\"}"
          ;;
        *)
          hit POST "/api/molecular-profiles/$mp/molecular-data/fetch" "{\"sampleListId\":\"$sl\",\"entrezGeneIds\":$genes}"
          hit GET "/api/molecular-profiles/$mp/molecular-data?sampleListId=$sl&entrezGeneId=7157"
          hit POST "/api/molecular-profiles/co-expressions/fetch?molecularProfileIdA=$mp&molecularProfileIdB=$mp" "{\"entrezGeneId\":7157,\"sampleListId\":\"$sl\"}"
          ;;
      esac
    done
  done
}

if [ -n "${API_URL:-}" ]; then
  BASE=$API_URL
  exercise
  for f in "${CASE_FAILS[@]}"; do echo "  fail: $f"; done
  printf '%s: %d endpoints, %d failures\n' "$API_URL" "$CASE_HITS" "${#CASE_FAILS[@]}"
  [ ${#CASE_FAILS[@]} -eq 0 ] && echo PASS || { echo FAIL; exit 1; }
  exit 0
fi

TOTAL_FAILS=0
SUMMARY=()
for spec in "${CASES[@]}"; do
  name=${spec%%=*}; dirs=${spec#*=}
  raw="tsub_${name}_raw"; tgt="tsub_${name}"
  DBS+=("$raw" "$tgt")
  chq "DROP DATABASE IF EXISTS \`$raw\`" >/dev/null
  chq "DROP DATABASE IF EXISTS \`$tgt\`" >/dev/null
  echo "=== $name: $dirs"
  ok=1
  IFS=, read -ra dirlist <<<"$dirs"
  for d in "${dirlist[@]}"; do
    if ! "$WORK/cbioportal-import" ingest "$d" --url "$CH" --db "$raw" >"$WORK/ingest.log" 2>&1; then
      echo "ingest failed:"; tail -5 "$WORK/ingest.log"; ok=0
    fi
  done
  if [ $ok = 1 ] && ! "$WORK/cbioportal-import" transform --url "$CH" --raw-db "$raw" --db "$tgt" --ref-db "$REF_DB" >"$WORK/transform.log" 2>&1; then
    echo "transform failed:"; tail -5 "$WORK/transform.log"; ok=0
  fi
  if [ $ok = 0 ]; then
    SUMMARY+=("$(printf '%-10s import/transform FAILED' "$name")")
    TOTAL_FAILS=$((TOTAL_FAILS + 1))
    [ "${KEEP_DBS:-}" = 1 ] || { chq "DROP DATABASE IF EXISTS \`$raw\`" >/dev/null; chq "DROP DATABASE IF EXISTS \`$tgt\`" >/dev/null; }
    continue
  fi

  CLICKHOUSE_URL=$CH CBIOPORTAL_DB=$tgt CBIOPORTAL_RAW_DB=$raw CBIOPORTAL_REF_DB=$REF_DB PORT=$PORT \
    "$WORK/cbioportal-api" >"$WORK/api.log" 2>&1 &
  API_PID=$!
  BASE="http://localhost:$PORT"
  for _ in $(seq 1 50); do curl -sf "$BASE/health" >/dev/null && break; sleep 0.2; done

  CASE_HITS=0; CASE_FAILS=()
  exercise
  kill "$API_PID" 2>/dev/null; wait "$API_PID" 2>/dev/null; API_PID=""

  SUMMARY+=("$(printf '%-10s %4d endpoints, %3d failures' "$name" "$CASE_HITS" "${#CASE_FAILS[@]}")")
  for f in "${CASE_FAILS[@]}"; do echo "  fail: $f"; done
  TOTAL_FAILS=$((TOTAL_FAILS + ${#CASE_FAILS[@]}))
  if [ "${KEEP_DBS:-}" != 1 ]; then
    chq "DROP DATABASE IF EXISTS \`$raw\`" >/dev/null
    chq "DROP DATABASE IF EXISTS \`$tgt\`" >/dev/null
  fi
done

echo
printf '%s\n' "${SUMMARY[@]}"
[ "$TOTAL_FAILS" -eq 0 ] && echo PASS || { echo "FAIL ($TOTAL_FAILS)"; exit 1; }
