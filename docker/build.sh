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

docker build -f "$RELAY_ROOT/Dockerfile" -t "$IMAGE" "$SUITE_ROOT"
