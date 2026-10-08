#!/usr/bin/env bash
# Imports the studies a cBioPortal/datahub pull request changes into
# hub.cbioportal.org and reports the result on the pull request: the per-PR
# Job of the `datahub-pr-import-set` ApplicationSet.
#
#   PR_NUMBER=2327 HEAD_SHA=<sha> /app/deploy/hub-import.sh
#
# The work is done by `cbioportal-ops hub import-pr`; its settings come from
# the environment (see README.md).
exec "${CBIOPORTAL_OPS:-cbioportal-ops}" hub import-pr "$@"
