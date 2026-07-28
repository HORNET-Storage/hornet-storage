#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BIN_DIR="$ROOT_DIR/bin"
RELAY_DIR="$ROOT_DIR/relay"
AIRLOCK_DIR="$ROOT_DIR/airlock"
RELAY_BIN="$BIN_DIR/hornets-relay"
AIRLOCK_BIN="$BIN_DIR/airlock"
SIDECAR_BIN="$BIN_DIR/hornets-hyperswarm"
SETUP_MARKER="$RELAY_DIR/data/.hornets_setup_complete"

for required in "$RELAY_BIN" "$AIRLOCK_BIN" "$SIDECAR_BIN"; do
  if [ ! -x "$required" ]; then
    echo "Missing or non-executable component: $required" >&2
    exit 1
  fi
done
if [ ! -d "$BIN_DIR/prebuilds" ]; then
  echo "Missing hyperswarm native prebuilds: $BIN_DIR/prebuilds" >&2
  exit 1
fi
if [ ! -f "$RELAY_DIR/web/index.html" ]; then
  echo "Missing relay web panel: $RELAY_DIR/web/index.html" >&2
  exit 1
fi

mkdir -p "$RELAY_DIR" "$AIRLOCK_DIR"
export AIRLOCK_CONFIG_PATH="$AIRLOCK_DIR/config.yaml"

relay_pid=
airlock_pid=
cleanup() {
  trap - EXIT INT TERM
  if [ -n "$airlock_pid" ] && kill -0 "$airlock_pid" 2>/dev/null; then
    kill "$airlock_pid" 2>/dev/null || true
  fi
  if [ -n "$relay_pid" ] && kill -0 "$relay_pid" 2>/dev/null; then
    kill "$relay_pid" 2>/dev/null || true
  fi
  [ -z "$airlock_pid" ] || wait "$airlock_pid" 2>/dev/null || true
  [ -z "$relay_pid" ] || wait "$relay_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM

(
  cd "$RELAY_DIR"
  exec "$RELAY_BIN" --bootstrap-setup --setup-profile operator --setup-host 127.0.0.1 --setup-port 11012
) &
relay_pid=$!

echo "HORNETS relay is starting in operator setup mode."
echo "On the first run, open http://127.0.0.1:11012, review the public relay defaults, and apply setup."
echo "Relay identity, Airlock identity, and the shared sidecar are handled automatically; keep this terminal open."

while [ ! -f "$SETUP_MARKER" ] || [ ! -f "$AIRLOCK_CONFIG_PATH" ]; do
  if ! kill -0 "$relay_pid" 2>/dev/null; then
    set +e
    wait "$relay_pid"
    status=$?
    set -e
    if [ "$status" -eq 0 ]; then
      status=1
    fi
    echo "Relay exited before first-time setup completed (exit $status)." >&2
    exit "$status"
  fi
  sleep 1
done

(
  cd "$AIRLOCK_DIR"
  exec "$AIRLOCK_BIN"
) &
airlock_pid=$!
echo "Airlock started using the relay address saved during setup."

while kill -0 "$relay_pid" 2>/dev/null && kill -0 "$airlock_pid" 2>/dev/null; do
  sleep 2
done

set +e
if ! kill -0 "$relay_pid" 2>/dev/null; then
  component=Relay
  wait "$relay_pid"
  status=$?
else
  component=Airlock
  wait "$airlock_pid"
  status=$?
fi
if [ "$status" -eq 0 ]; then
  status=1
fi
echo "$component stopped unexpectedly (exit $status)." >&2
set -e
exit "$status"
