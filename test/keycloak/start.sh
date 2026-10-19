#!/usr/bin/env bash
# Starts the Keycloak the e2e suite logs in through, as upstream CI does:
# Keycloak 16.1.1 (on :8084; CI uses :8081) under /auth, realm "cbio", user testuser /
# P@ssword1, with one "cbioportal" client role per study in STUDIES; datuser /
# P@ssword1 has only the first (test/dat_keycloak_test.go).
#
#   test/keycloak/start.sh            # (re)start with a fresh realm
#   test/keycloak/start.sh stop
#
# The API then logs in with:
#   authenticate=oauth2
#   spring.security.oauth2.client.registration.keycloak.client-id=cbioportal_oauth2
#   spring.security.oauth2.client.registration.keycloak.client-secret=client_secret
#   spring.security.oauth2.client.provider.keycloak.issuer-uri=http://localhost:8084/auth/realms/cbio
set -euo pipefail
NAME=cbioportal-go-keycloak
PORT=${KEYCLOAK_PORT:-8084}
# The studies upstream CI grants the test user (.circleci/config.yml).
STUDIES=${STUDIES:-ascn_test_study study_hg38 teststudy_genepanels study_es_0 lgg_ucsf_2014_test_generic_assay}

# Stop from inside when the daemon may not signal containers (some Docker
# installs, e.g. snap under AppArmor).
docker rm -f "$NAME" >/dev/null 2>&1 ||
  { docker exec "$NAME" sh -c 'kill 1' >/dev/null 2>&1; sleep 3; docker rm "$NAME" >/dev/null 2>&1; } || true
for _ in $(seq 1 30); do docker inspect "$NAME" >/dev/null 2>&1 || break; sleep 1; done
[ "${1:-}" = stop ] && exit 0

realm="$(cd "$(dirname "$0")" && pwd)/realm.generated.json"
jq --arg s "$STUDIES" '($s | split(" ") | map(select(. != ""))) as $ids
  | .roles.client.cbioportal = ($ids | map({name: .}))
  | .groups |= map(if .name == "PUBLIC_STUDIES" then .clientRoles.cbioportal = $ids else . end)
  | .users |= map(if .username == "datuser" then .clientRoles.cbioportal = $ids[:1] else . end)' \
  "$(dirname "$0")/realm.json" >"$realm"
chmod 644 "$realm"

docker run -d --name "$NAME" -p "$PORT:8080" \
  -e KEYCLOAK_USER=admin -e KEYCLOAK_PASSWORD=admin \
  -e KEYCLOAK_IMPORT=/tmp/realm.json -v "$realm:/tmp/realm.json:ro" \
  quay.io/keycloak/keycloak:16.1.1 >/dev/null

for _ in $(seq 1 90); do
  curl -sf -o /dev/null "http://localhost:$PORT/auth/realms/cbio/.well-known/openid-configuration" && {
    echo "keycloak ready on :$PORT (realm cbio, roles: $STUDIES)"; exit 0; }
  sleep 2
done
echo "keycloak did not start; docker logs $NAME" >&2
exit 1
