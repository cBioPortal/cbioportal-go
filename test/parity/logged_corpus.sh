#!/usr/bin/env bash
# Writes the logged-traffic parity corpus: about CORPUS_N read-only requests
# sampled from cbioportal_qc.logged_requests by test/parity/loggedgen,
# stratified by endpoint (each endpoint's share grows with the square root of
# its volume, at least CORPUS_MIN), with the known differences marked. The
# sample seed is the ISO week, so a weekly regeneration follows current
# traffic. The corpus is real user traffic: keep it out of git (.parity/ is
# ignored); it is written mode 600.
#
#   test/parity/logged_corpus.sh [OUT]     # default .parity/nightly/corpus.txt
#
# ClickHouse credentials come from CLICKHOUSE_URL/USER/PASSWORD, or else from
# CLOUD_ENV (default ~/.config/cbioportal-go/cloud.env).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${1:-$ROOT/.parity/nightly/corpus.txt}"
N="${CORPUS_N:-5000}"
MIN="${CORPUS_MIN:-3}"
SEED="${CORPUS_SEED:-$(date -u +%G%V)}"

if [ -z "${CLICKHOUSE_URL:-}" ]; then
  set -a
  # shellcheck disable=SC1090
  . "${CLOUD_ENV:-$HOME/.config/cbioportal-go/cloud.env}"
  set +a
fi

mkdir -p "$(dirname "$OUT")"
tmp="$(mktemp "$OUT.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
chmod 600 "$tmp"
cd "$ROOT"
go run ./test/parity/loggedgen -n "$N" -min "$MIN" -seed "$SEED" -alloc sqrt |
  go run ./test/parity/loggedgen -annotate >"$tmp"
n=$(grep -c '^## ' "$tmp" || true)
if [ "$n" -eq 0 ]; then
  echo "logged_corpus: no requests sampled" >&2
  exit 1
fi
mv "$tmp" "$OUT"
trap - EXIT
echo "logged_corpus: $n requests in $OUT" >&2
