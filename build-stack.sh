#!/bin/sh
set -eu

RELAY_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SUITE_ROOT=$(CDPATH= cd -- "$RELAY_ROOT/.." && pwd)
AIRLOCK_ROOT="$SUITE_ROOT/airlock"
SIDECAR_ROOT="$SUITE_ROOT/hornets-hyperswarm"
PANEL_ROOT="$SUITE_ROOT/hornets-relay-panel"
OUT="$RELAY_ROOT/dist/hornets-relay-dev"

for project in "$AIRLOCK_ROOT" "$SIDECAR_ROOT" "$PANEL_ROOT" "$SUITE_ROOT/nosis-cli"; do
  if [ ! -d "$project" ]; then
    echo "Missing sibling project: $project" >&2
    exit 1
  fi
done

rm -rf "$OUT"
mkdir -p "$OUT/bin" "$OUT/relay/web" "$OUT/airlock"
(
  cd "$RELAY_ROOT"
  CGO_ENABLED=1 go build -buildvcs=false -trimpath -o "$OUT/bin/hornets-relay" ./services/server/port
)
(
  cd "$AIRLOCK_ROOT"
  CGO_ENABLED=1 go build -buildvcs=false -trimpath -o "$OUT/bin/airlock" .
)
(
  cd "$SIDECAR_ROOT"
  if [ -f package-lock.json ]; then
    npx --yes npm@11.10.0 ci
  else
    npx --yes npm@11.10.0 install
  fi
  npx --yes npm@11.10.0 run build
)
(
  cd "$PANEL_ROOT"
  corepack enable
  corepack prepare yarn@1.22.22 --activate
  yarn install --frozen-lockfile
  yarn build
)
cp "$SIDECAR_ROOT/dist/hornets-hyperswarm" "$OUT/bin/hornets-hyperswarm"
cp -R "$SIDECAR_ROOT/dist/prebuilds" "$OUT/bin/prebuilds"
cp "$RELAY_ROOT/config.example.yaml" "$OUT/relay/config.example.yaml"
cp "$AIRLOCK_ROOT/config.example.yaml" "$OUT/airlock/config.example.yaml"
cp -R "$RELAY_ROOT/release/bundle/." "$OUT/"
cp -R "$PANEL_ROOT/build/." "$OUT/relay/web/"
chmod +x "$OUT/start.sh" "$OUT/bin/hornets-relay" "$OUT/bin/airlock" "$OUT/bin/hornets-hyperswarm" "$OUT/docker/entrypoint.sh"
echo "Built self-contained stack at $OUT"
