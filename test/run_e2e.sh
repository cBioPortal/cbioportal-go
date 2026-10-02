#!/usr/bin/env bash
# Fast, reliable e2e runner for cbioportal-go.
#
#   test/run_e2e.sh [WORKERS] [spec ...]
#     WORKERS   parallel workers (default 2; the box's swap is tight — 3 is the
#               practical ceiling before OOM with other sessions running).
#     spec      one or more spec paths/patterns (default: the whole tests/local/).
#
# Examples:
#   test/run_e2e.sh 2 tests/local/annotation-filter-menu.spec.ts
#   test/run_e2e.sh 3                      # full suite, 3 workers
#   nohup test/run_e2e.sh 2 >/tmp/e2e.log 2>&1 &   # long full run, background
#
# It deliberately does NOT pass --reporter, so the config's json reporter writes
# test-results/report.json; the summary is parsed from THAT (reliable), not from
# the line reporter's stdout (which yields 0-byte logs when backgrounded).
set -u
E2E="$HOME/projects/cbioportal-rust/cbioportal-frontend/end-to-end-test-playwright"
GO="$(cd "$(dirname "$0")/.." && pwd)"
WORKERS="${1:-2}"; shift 2>/dev/null || true
SPECS=("$@"); [ "${#SPECS[@]}" -eq 0 ] && SPECS=("tests/local/")

# The suite drives the portal through the API origin, which serves the UI — a
# production build (FRONTEND_DIR=<frontend>/dist) or a proxied dev server
# (FRONTEND_URL) — so specs that call /api relative to the page work as against
# a real deployment.
URL="${E2E_URL:-http://localhost:18090}"

# Preflight: frontend + API must be up (fail fast rather than a slow all-red run).
curl -sf -o /dev/null --max-time 30 "$URL/" || { echo "portal UI not served at $URL (FRONTEND_DIR or FRONTEND_URL on the API)"; exit 2; }
curl -sf -o /dev/null http://localhost:18090/health || { echo "api :18090 not up"; exit 2; }

cd "$E2E" || exit 1
pkill -9 -f '@playwright/test' 2>/dev/null; pkill -9 -f chrome-headless 2>/dev/null; sleep 1
rm -rf test-results/* 2>/dev/null

CBIOPORTAL_URL="$URL" LOCALDEV=0 PW_LOCAL=1 HEADLESS_CHROME=true \
  corepack pnpm exec playwright test "${SPECS[@]}" --update-snapshots --workers="$WORKERS" --timeout=60000
rc=$?

echo "======================================================================"
(cd "$GO" && PATH="$PATH:/home/ino/go-local/go/bin" go run ./cmd/e2ereport --by-spec "$E2E/test-results/report.json") 2>/dev/null || echo "(no report.json — run may have crashed/OOMed)"
exit $rc
