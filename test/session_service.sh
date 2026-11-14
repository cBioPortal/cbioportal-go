#!/usr/bin/env bash
# Runs cBioPortal's session service with its MongoDB, set up as the public
# portal's (session service 0.6.5 behind basic authentication, MongoDB 8.2),
# for testing the session endpoints of go and Java against the real service
# (docs/testing.md, "Session service"):
#
#   test/session_service.sh start   # on :15000, user cbio, password cbio-local-test
#   test/session_service.sh stop
#
# The credentials are for this local test service only.
# Env: SESSION_PORT (15000), MONGO_PORT (27117), SESSION_IMAGE, MONGO_IMAGE.
set -euo pipefail
PORT=${SESSION_PORT:-15000}
MONGO_PORT=${MONGO_PORT:-27117}
SESSION_IMAGE=${SESSION_IMAGE:-cbioportal/session-service:0.6.5}
MONGO_IMAGE=${MONGO_IMAGE:-mongo:8.2}

case "${1:-}" in
start)
  docker run -d --name session-mongo --network host --memory 1g "$MONGO_IMAGE" \
    mongod --port "$MONGO_PORT" --bind_ip 127.0.0.1 >/dev/null
  docker run -d --name session-service --network host --memory 1g --entrypoint java "$SESSION_IMAGE" \
    -Xmx512m -Dserver.port="$PORT" \
    -Dspring.data.mongodb.uri="mongodb://127.0.0.1:$MONGO_PORT/session_service" \
    -Dsecurity.basic.enabled=true -Dspring.security.user.name=cbio -Dspring.security.user.password=cbio-local-test \
    -jar /app.war >/dev/null
  timeout 180 bash -c "until curl -s -o /dev/null -u cbio:cbio-local-test localhost:$PORT/api/sessions/test/main/query?field=x\&value=y; do sleep 2; done"
  echo "session service on :$PORT (session.service.url=http://localhost:$PORT/api/sessions/public_portal/)"
  ;;
stop)
  for c in session-service session-mongo; do
    docker exec "$c" sh -c 'kill 1' 2>/dev/null || docker stop "$c" >/dev/null || true
    docker wait "$c" >/dev/null 2>&1 || true
    docker rm "$c" >/dev/null 2>&1 || true
  done
  ;;
*)
  sed -n '2,10p' "$0"
  exit 1
  ;;
esac
