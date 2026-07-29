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
  npx --yes npm@11.10.0 ci
  npx --yes npm@11.10.0 run build
)
(
  cd "$PANEL_ROOT"
  corepack enable
  corepack prepare yarn@1.22.19 --activate
  yarn install --frozen-lockfile
  yarn build
)
cp "$SIDECAR_ROOT/dist/hornets-hyperswarm" "$OUT/bin/hornets-hyperswarm"
cp -R "$SIDECAR_ROOT/dist/prebuilds" "$OUT/bin/prebuilds"
cp "$RELAY_ROOT/config.example.yaml" "$OUT/relay/config.example.yaml"
cp "$AIRLOCK_ROOT/config.example.yaml" "$OUT/airlock/config.example.yaml"
cp -R "$RELAY_ROOT/release/bundle/." "$OUT/"
cp -R "$PANEL_ROOT/build/." "$OUT/relay/web/"

revision_for() {
  project=$1
  if [ -n "$(git -C "$project" status --porcelain)" ]; then
    echo working-tree
  else
    git -C "$project" rev-parse HEAD
  fi
}

RELAY_REVISION=$(revision_for "$RELAY_ROOT")
AIRLOCK_REVISION=$(revision_for "$AIRLOCK_ROOT")
HYPERSWARM_REVISION=$(revision_for "$SIDECAR_ROOT")
NOSIS_CLI_REVISION=$(revision_for "$SUITE_ROOT/nosis-cli")
PANEL_REVISION=$(revision_for "$PANEL_ROOT")

node "$RELAY_ROOT/scripts/compliance/generate.mjs" \
  --workspace "$SUITE_ROOT" \
  --output "$OUT/compliance" \
  --artifact hornets-relay-dev \
  --relay-revision "$RELAY_REVISION" \
  --airlock-revision "$AIRLOCK_REVISION" \
  --hyperswarm-revision "$HYPERSWARM_REVISION" \
  --nosis-cli-revision "$NOSIS_CLI_REVISION" \
  --panel-revision "$PANEL_REVISION"

{
  echo "Relay: $RELAY_REVISION"
  echo "Airlock: $AIRLOCK_REVISION"
  echo "Hyperswarm: $HYPERSWARM_REVISION"
  echo "Nosis CLI: $NOSIS_CLI_REVISION"
  echo "Relay panel: $PANEL_REVISION"
} > "$OUT/BUILD-METADATA.txt"

node "$RELAY_ROOT/scripts/compliance/verify.mjs" "$OUT"
chmod +x "$OUT/start.sh" "$OUT/bin/hornets-relay" "$OUT/bin/airlock" "$OUT/bin/hornets-hyperswarm" "$OUT/docker/entrypoint.sh"
echo "Built and compliance-verified self-contained stack at $OUT"
