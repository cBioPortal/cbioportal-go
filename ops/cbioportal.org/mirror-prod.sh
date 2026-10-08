#!/usr/bin/env bash
# Rebuilds go.cbioportal.org's databases from a clone of the live production
# database: the hourly mirror CronJob. With TARGET_DB (and SRC_DB) it builds
# other copies of production.
#
# The work is done by `cbioportal-rewrite-test java-v7-to-go mirror`, a tool
# of the migration from Java cBioPortal; its settings come from the
# environment (see README.md).
exec "${CBIOPORTAL_REWRITE_TEST:-cbioportal-rewrite-test}" java-v7-to-go mirror "$@"
