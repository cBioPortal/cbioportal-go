#!/usr/bin/env bash
# Removes the databases of cbioportal-go pull request previews:
#
#   pr-preview-teardown.sh <N>   the preview of PR N (the chart's delete hook)
#   pr-preview-teardown.sh --gc  every preview whose PR is closed or no longer
#                                labelled `preview` (the gc CronJob)
#
# The work is done by `cbioportal-ops preview teardown|gc` (see README.md).
ops=${CBIOPORTAL_OPS:-cbioportal-ops}
case ${1:-} in
  --gc) exec "$ops" preview gc ;;
  *) exec "$ops" preview teardown "$@" ;;
esac
