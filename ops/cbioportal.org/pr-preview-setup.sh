#!/usr/bin/env bash
# Builds the databases of a cbioportal-go pull request's preview
# (pr-<N>.gopreview.cbioportal.org) and reports the result on the PR: the
# setup CronJob of the preview chart.
#
#   PR_NUMBER=42 HEAD_SHA=<sha> /app/deploy/pr-preview-setup.sh
#
# The work is done by `cbioportal-ops preview setup`; its settings come from
# the environment (see README.md).
exec "${CBIOPORTAL_OPS:-cbioportal-ops}" preview setup "$@"
