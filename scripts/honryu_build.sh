#!/bin/bash
# scripts/honryu_build.sh — build + push all honryu images for a phase tag.
# Lives in the repo because ct117 /tmp is wiped on every reboot.
#
# Usage: bash scripts/honryu_build.sh phaseNN
#
# Push path is skopeo, NOT `docker push`: ct117's `docker login` 401s
# against the registry (htpasswd rotated at some point; the exact cause
# never root-caused because the skopeo bypass is strictly simpler) while
# the same creds work fine over the registry HTTP API. skopeo reads the
# image straight from the docker daemon, so the socket must be mounted.
#
# Image paths: control-plane is DOUBLE-honryu (honryu/honryu-<C>) but grafana
# is SINGLE (honryu/grafana) — the chart repositories differ; check values.yaml.
# The chart's image.repository values say honryu/honryu-api (namespace
# honryu, repo honryu-api) — a build script that pushes honryu/api feeds
# helm an ImagePullBackOff 300s timeout (phase97 lesson, twice).
#
# Every push is verified against the registry tags API before success is
# declared — silent push failure is otherwise invisible until deploy.
set -euo pipefail
TAG="${1:?usage: honryu_build.sh phaseNN}"
cd "$(dirname "$0")/.."
export PATH="$HOME/.bun/bin:$HOME/.nvm/versions/node/v24.18.0/bin:/usr/local/go/bin:$HOME/go/bin:$PATH"
REG=registry.pve.heri.life

echo "== creds (from cluster pull secret) =="
AUTH=$(kubectl -n honryu get secret registry-pve-heri-life -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d | python3 -c 'import json,sys,base64; d=json.load(sys.stdin); a=d["auths"]["registry.pve.heri.life"]; print(base64.b64decode(a["auth"]).decode())')
U="${AUTH%%:*}"; P="${AUTH#*:}"

echo "== SPA build =="
(cd web && bun run build 2>&1 | tail -2)

echo "== control-plane images (single Dockerfile, --build-arg CMD) =="
for C in api calibrator scheduler sidecar; do
  docker build -f deploy/honryu/Dockerfile --build-arg CMD="$C" -t "$REG/honryu/honryu-$C:$TAG" . || exit 1
  echo "$C: built"
done

echo "== grafana image =="
docker build -f grafana/Dockerfile -t "$REG/honryu/grafana:$TAG" grafana/ || exit 1
echo "grafana: built"

echo "== push via skopeo (docker login is broken on this host) =="
for C in api calibrator scheduler sidecar; do
  docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
    quay.io/skopeo/stable:latest copy --dest-tls-verify=false \
    --dest-creds "$U:$P" \
    "docker-daemon:$REG/honryu/honryu-$C:$TAG" \
    "docker://$REG/honryu/honryu-$C:$TAG" >/dev/null || { echo "PUSH_FAIL $C"; exit 1; }
  echo "$C: pushed $TAG"
done
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  quay.io/skopeo/stable:latest copy --dest-tls-verify=false \
  --dest-creds "$U:$P" \
  "docker-daemon:$REG/honryu/grafana:$TAG" \
  "docker://$REG/honryu/grafana:$TAG" >/dev/null || { echo "PUSH_FAIL grafana"; exit 1; }
echo "grafana: pushed $TAG"

echo "== verify tags landed in registry =="
for C in api calibrator scheduler sidecar; do
  PRESENT=$(curl -sk -u "$AUTH" "https://$REG/v2/honryu/honryu-$C/tags/list" | python3 -c "import json,sys; print('$TAG' in json.load(sys.stdin).get('tags',[]))")
  echo "$C tag $TAG present: $PRESENT"
  [ "$PRESENT" = "True" ] || { echo "VERIFY_FAIL $C"; exit 1; }
done
PRESENT=$(curl -sk -u "$AUTH" "https://$REG/v2/honryu/grafana/tags/list" | python3 -c "import json,sys; print('$TAG' in json.load(sys.stdin).get('tags',[]))")
echo "grafana tag $TAG present: $PRESENT"
[ "$PRESENT" = "True" ] || { echo "VERIFY_FAIL grafana"; exit 1; }
echo "BUILD_PUSH_OK $TAG (verified in registry)"

echo "== local image GC (keep current + previous phase only) =="
# ct117 disk filled to 95% (2026-09-21) from phase images piling up locally.
# Everything is verified in the registry above, so old local tags are pure
# ballast. Keep $TAG and the phase right before it (rollback safety).
PREV="phase$((10#${TAG#phase} - 1))"
(
  docker images --format '{{.Repository}}:{{.Tag}}' \
    | grep -E 'heri\.life/honryu/.*:phase[0-9]+$' \
    | grep -vE ":($TAG|$PREV)$" \
    | xargs -r docker rmi -f >/dev/null 2>&1 || true
  docker builder prune -f >/dev/null 2>&1 || true
  docker volume prune -f >/dev/null 2>&1 || true
) || true
echo "GC_OK (kept $TAG + $PREV)"
