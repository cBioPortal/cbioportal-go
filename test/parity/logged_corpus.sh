#!/usr/bin/env bash
# Writes the logged-traffic parity corpus:
# `cbioportal-rewrite-test java-v7-to-go parity corpus` (see its -h and
# docs/rewrite/parity.md).
#
#   test/parity/logged_corpus.sh [OUT]     # default .parity/nightly/corpus.txt
set -euo pipefail
out=()
if [ $# -gt 0 ]; then out=("$(realpath -m "$1")"); fi
cd "$(dirname "$0")/../.."
exec go run ./rewrite/cmd/cbioportal-rewrite-test java-v7-to-go parity corpus "${out[@]}"
