#!/usr/bin/env bash
# Run the OIDC tests against a real Keycloak (#708 PR 6b): start Keycloak
# in a container with the test realm (realm.json: synthetic users alice,
# an engineer by group eng, and bob, an operator by group ops), run the
# Go tests that need it (NAUTILUS_TEST_KEYCLOAK names the realm's issuer),
# and stop it. Needs docker. The tests skip without the variable, so
# `go test ./...` never needs a provider.
#
#   tools/keycloak/run.sh            # start, test, stop
#   KEEP=1 tools/keycloak/run.sh     # leave Keycloak running afterwards
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
image=${KEYCLOAK_IMAGE:-quay.io/keycloak/keycloak:26.0.7}
port=${KEYCLOAK_PORT:-8180}
name=naut-keycloak-test-$$
issuer="http://127.0.0.1:$port/realms/nautilus-test"

docker run -d --rm --name "$name" -p "127.0.0.1:$port:8080" \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
  -v "$here/realm.json:/opt/keycloak/data/import/realm.json:ro" \
  "$image" start-dev --import-realm >/dev/null
stop() { [ -n "${KEEP:-}" ] && { echo "Keycloak left running: $name ($issuer)"; return; }; docker stop "$name" >/dev/null 2>&1 || true; }
trap stop EXIT

for _ in $(seq 1 120); do
  curl -fsS "$issuer/.well-known/openid-configuration" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS "$issuer/.well-known/openid-configuration" >/dev/null || { docker logs "$name" | tail -40; echo "Keycloak did not come up"; exit 1; }

cd "$repo"
NAUTILUS_TEST_KEYCLOAK="$issuer" go test -count=1 -timeout 300s -run 'Keycloak' ./cmd/naut/ "$@"
