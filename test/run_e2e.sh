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
#   E2E_SKIP='toggles gene panel modal from sample icon in copy number' test/run_e2e.sh 3
#
# It deliberately does NOT pass --reporter, so the config's json reporter writes
# test-results/report.json; the summary is parsed from THAT (reliable), not from
# the line reporter's stdout (which yields 0-byte logs when backgrounded).
set -u
GO="$(cd "$(dirname "$0")/.." && pwd)"
E2E="${E2E_DIR:-$GO/frontend/end-to-end-test-playwright}"
WORKERS="${1:-2}"; shift 2>/dev/null || true
SPECS=("$@"); [ "${#SPECS[@]}" -eq 0 ] && SPECS=("tests/local/")

# The suite drives the portal through the API origin, which serves the UI — a
# production build (FRONTEND_DIR=<frontend>/dist) or a proxied dev server
# (FRONTEND_URL) — so specs that call /api relative to the page work as against
# a real deployment.
URL="${E2E_URL:-http://localhost:18090}"
# LOCALDEV=1 as in the reference CI: the fixtures append ?localdist=true to
# every URL (ignored by this backend), which turns hash-only navigations in
# some specs into full page loads. CI=1 also as in the reference CI: one
# retry per test, reported as flaky when the retry passes.

# Preflight: frontend + API must be up (fail fast rather than a slow all-red run).
curl -sf -o /dev/null --max-time 30 "$URL/" || { echo "portal UI not served at $URL (FRONTEND_DIR or FRONTEND_URL on the API)"; exit 2; }
curl -sf -o /dev/null http://localhost:18090/health || { echo "api :18090 not up"; exit 2; }

cd "$E2E" || exit 1
# Stop a leftover run of this checkout's suite (its browsers exit with it).
for pid in $(pgrep -f '@playwright/test' 2>/dev/null); do
  [ "$(readlink "/proc/$pid/cwd" 2>/dev/null)" = "$(pwd -P)" ] && kill -9 "$pid" 2>/dev/null
done
sleep 1
rm -rf test-results/* 2>/dev/null

# E2E_SKIP: a test-title regex to leave out (e.g. a known environment gap).
EXTRA=(); [ -n "${E2E_SKIP:-}" ] && EXTRA=(--grep-invert "$E2E_SKIP")
CI=1 CBIOPORTAL_URL="$URL" LOCALDEV=1 PW_LOCAL=1 HEADLESS_CHROME=true \
  corepack pnpm exec playwright test "${SPECS[@]}" --update-snapshots --workers="$WORKERS" "${EXTRA[@]}"
rc=$?

echo "======================================================================"
(cd "$GO" && PATH="$PATH:$HOME/go/bin:$HOME/go-local/go/bin" go run ./cmd/e2ereport --by-spec "$E2E/test-results/report.json") 2>/dev/null || echo "(no report.json — run may have crashed/OOMed)"
exit $rc
