#!/bin/sh
set -eu

RELAY_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SUITE_ROOT=$(CDPATH= cd -- "$RELAY_ROOT/.." && pwd)
IMAGE=${1:-hornetstorage/hornets-relay:local}

for project in hornets-nostr-relay airlock nosis-cli hornets-hyperswarm hornets-relay-panel; do
  if [ ! -d "$SUITE_ROOT/$project" ]; then
    echo "Missing sibling project: $SUITE_ROOT/$project" >&2
    exit 1
  fi
done

revision_for() {
  project=$1
  if [ -n "$(git -C "$project" status --porcelain)" ]; then
    echo working-tree
  else
    git -C "$project" rev-parse HEAD
  fi
}

docker build \
  -f "$RELAY_ROOT/Dockerfile" \
  --build-arg "RELAY_REVISION=$(revision_for "$RELAY_ROOT")" \
  --build-arg "AIRLOCK_REVISION=$(revision_for "$SUITE_ROOT/airlock")" \
  --build-arg "HYPERSWARM_REVISION=$(revision_for "$SUITE_ROOT/hornets-hyperswarm")" \
  --build-arg "NOSIS_CLI_REVISION=$(revision_for "$SUITE_ROOT/nosis-cli")" \
  --build-arg "RELAY_PANEL_REVISION=$(revision_for "$SUITE_ROOT/hornets-relay-panel")" \
  -t "$IMAGE" \
  "$SUITE_ROOT"
