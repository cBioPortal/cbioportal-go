#!/usr/bin/env bash
# Nightly API parity check of go.cbioportal.org against api.cbioportal.org:
# `cbioportal-rewrite-test java-v7-to-go parity run` (see its -h and
# docs/rewrite/parity.md). Kept at this path for cron jobs that run it from
# a checkout.
#
#   test/parity/nightly.sh [--regen] [--dry-run]
set -euo pipefail
cd "$(dirname "$0")/../.."
exec go run ./rewrite/cmd/cbioportal-rewrite-test java-v7-to-go parity run "$@"
