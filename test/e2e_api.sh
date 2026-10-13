#!/usr/bin/env bash
# Builds and runs the API the e2e suite and contract tests run against, set
# up as upstream CI's backend: the fixture databases, the e2e portal
# properties, and login through Keycloak (test/keycloak/start.sh, started
# here when it is not running) as testuser.
#
#   test/e2e_api.sh             # foreground, on :18090
#   KEYCLOAK=false test/e2e_api.sh   # anonymous, AUTH_DEFAULT_USER=testuser
#   DAT=true test/e2e_api.sh    # also data access tokens (dat.method=oauth2)
#   OPTIONAL=true test/e2e_api.sh   # authenticate=optional_oauth2: open, sign-in optional
#   BASIC=true test/e2e_api.sh  # also the shared user basicuser / basicpass, role ascn_test_study
#   REDIS=localhost:16379 REDIS_PASSWORD=pw test/e2e_api.sh   # login sessions in Redis
#
# Anonymous mode is the contract tests' backend: it serves study_hg38, which
# the e2e portal properties lock (study_availability.enabled), since the
# contract tests read its data.
set -euo pipefail
GO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$GO"
PORT=${PORT:-18090}
KC_PORT=${KEYCLOAK_PORT:-8084}
go build -o bin/cbioportal-api ./cmd/cbioportal-api

auth=()
if [ "${KEYCLOAK:-true}" != false ]; then
  curl -sf -o /dev/null "http://localhost:$KC_PORT/auth/realms/cbio/.well-known/openid-configuration" ||
    KEYCLOAK_PORT=$KC_PORT test/keycloak/start.sh
  reg=spring.security.oauth2.client.registration.keycloak
  method=oauth2
  [ "${OPTIONAL:-false}" = true ] && method=optional_oauth2
  auth=(
    --authenticate=$method
    --authorization=false
    --$reg.client-id=cbioportal_oauth2
    --$reg.client-secret=client_secret
    --$reg.client-authentication-method=client_secret_post
    --$reg.scope=openid,email,roles
    --spring.security.oauth2.client.provider.keycloak.issuer-uri=http://localhost:$KC_PORT/auth/realms/cbio
    --spring.security.oauth2.client.provider.keycloak.user-name-attribute=email
  )
  if [ -n "${REDIS:-}" ]; then
    # The password stays in the environment, resolved by the placeholder.
    export REDIS_PASSWORD=${REDIS_PASSWORD:-}
    auth+=(
      --spring.session.store-type=redis
      --spring.data.redis.host=${REDIS%:*}
      --spring.data.redis.port=${REDIS##*:}
      '--spring.data.redis.password=${REDIS_PASSWORD}'
    )
  fi
  if [ "${BASIC:-false}" = true ]; then
    # The bcrypt hash of the test password basicpass.
    export CBIOPORTAL_LOGIN_BASIC_PASSWORD_HASH='$2a$10$q83DQS6tzo78dg94gkIQvOulBL/uUTnkuy8oY9PTq.ZHc7BmpQGfG'
    auth+=(--login-basic-user=basicuser --login-basic-roles=ascn_test_study)
  fi
  if [ "${DAT:-false}" = true ]; then
    realm=http://localhost:$KC_PORT/auth/realms/cbio
    oidc=$realm/protocol/openid-connect
    auth+=(
      --dat.method=oauth2
      --dat.oauth2.clientId=cbioportal_oauth2
      --dat.oauth2.clientSecret=client_secret
      --dat.oauth2.issuer=$realm
      --dat.oauth2.accessTokenUri=$oidc/token
      --dat.oauth2.userAuthorizationUri=$oidc/auth
      --dat.oauth2.jwkUrl=$oidc/certs
      --dat.oauth2.redirectUri=http://localhost:$PORT/api/data-access-token/oauth2
    )
  fi
else
  export AUTH_DEFAULT_USER=testuser
  auth=(--study_availability.enabled=false)
fi

FRONTEND_DIR=${FRONTEND_DIR:-frontend/dist} PUBLISHER_API_KEY=SECRETKEY \
  SESSION_STORE_FILE=${SESSION_STORE_FILE:-.sessions.json} PORT=$PORT \
  CBIOPORTAL_DB=${CBIOPORTAL_DB:-cbioportal_e2e} CBIOPORTAL_RAW_DB=${CBIOPORTAL_RAW_DB:-cbioportal_e2e_raw} \
  exec bin/cbioportal-api --portal-properties=frontend/end-to-end-test/local/runtime-config/portal.properties "${auth[@]}"
