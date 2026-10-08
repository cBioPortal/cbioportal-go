## Summary

## Preview Configuration

<!--
With the `preview` label, this PR gets its own instance at
https://pr-<number>.gopreview.cbioportal.org, built by the PR's code
(ops/cbioportal.org/pr-preview-setup.sh). Pick its data with one line:

preview-data: prod               a copy of www.cbioportal.org's data (the default)
preview-data: datahub-pr 2372    the studies a cBioPortal/datahub PR changes
preview-data: studies            the datahub studies listed below

For `studies` (and optionally to narrow `datahub-pr`), list the studies:

preview-studies:
- public/msk_impact_2017

Editing this section rebuilds the preview within a few minutes.
-->
preview-data: prod
