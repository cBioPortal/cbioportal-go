#!/usr/bin/env bash
# Refreshes the API descriptions and Swagger UI the API serves
# (internal/api/apidocs): the v3 OpenAPI descriptions and Swagger UI from a
# Java cBioPortal API (default https://api.cbioportal.org, the reference the
# server URL in them is taken from), and the legacy v2 description from the
# cbioportal repository.
#
#   scripts/refresh-api-docs.sh [source-origin]
set -euo pipefail
SRC=${1:-https://api.cbioportal.org}
D="$(cd "$(dirname "$0")/.." && pwd)/internal/api/apidocs"
# The public API refuses browser user agents.
get() { curl -fsS -A 'cbioportal-go refresh-api-docs' "$SRC/$1" -o "$2"; }

get api/v3/api-docs "$D/v3.json"
get api/v3/api-docs/public "$D/v3-public.json"
get api/v3/api-docs/internal "$D/v3-internal.json"
get api/v3/api-docs.yaml "$D/v3.yaml"
get api/v3/api-docs.yaml/public "$D/v3-public.yaml"
get api/v3/api-docs.yaml/internal "$D/v3-internal.yaml"
for f in index.html index.css swagger-ui.css swagger-ui-bundle.js swagger-ui-standalone-preset.js \
  favicon-32x32.png favicon-16x16.png oauth2-redirect.html swagger-initializer.js; do
  get "api/swagger-ui/$f" "$D/swagger-ui/$f"
done
curl -fsS https://raw.githubusercontent.com/cBioPortal/cbioportal/master/src/main/resources/legacy-api.json \
  -o "$D/legacy-api.json"

# The API puts the requesting origin in place of the snapshot's.
for f in "$D"/v3*.json "$D"/v3*.yaml; do
  n=$(grep -c -F "$SRC" "$f" || true)
  [ "$n" -eq 1 ] || { echo "$f: expected the server URL $SRC once, found $n" >&2; exit 1; }
done
[ "$SRC" = https://api.cbioportal.org ] ||
  sed -i "s|$SRC|https://api.cbioportal.org|" "$D"/v3*.json "$D"/v3*.yaml

# The API serves these files with the paging parameters go adds laid over
# them (internal/api/apidocs/overlay.go); the snapshots stay as fetched.
# Check the overlay still finds its endpoints in the new snapshots.
(cd "$D/../../.." && go test ./internal/api/apidocs -run 'TestAPIDocsOverlay' -count=1)
echo "refreshed $D from $SRC"
