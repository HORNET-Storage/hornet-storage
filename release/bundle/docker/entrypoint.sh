#!/bin/sh
set -eu

INSTALL_DIR=${HORNETS_INSTALL_DIR:-/opt/hornets}
RELAY_DIR=${HORNETS_RELAY_DIR:-/data/relay}
AIRLOCK_DIR=${HORNETS_AIRLOCK_DIR:-/data/airlock}
SETUP_HOST=${HORNETS_SETUP_HOST:-0.0.0.0}
SETUP_PORT=${HORNETS_SETUP_PORT:-11012}
SETUP_MARKER="$RELAY_DIR/data/.hornets_setup_complete"

mkdir -p "$RELAY_DIR" "$AIRLOCK_DIR"
if [ ! -f "$RELAY_DIR/config.example.yaml" ]; then
  cp "$INSTALL_DIR/relay/config.example.yaml" "$RELAY_DIR/config.example.yaml"
fi
if [ ! -f "$AIRLOCK_DIR/config.example.yaml" ]; then
  cp "$INSTALL_DIR/airlock/config.example.yaml" "$AIRLOCK_DIR/config.example.yaml"
fi
mkdir -p "$RELAY_DIR/web"
cp -R "$INSTALL_DIR/relay/web/." "$RELAY_DIR/web/"
export AIRLOCK_CONFIG_PATH="$AIRLOCK_DIR/config.yaml"

relay_pid=
airlock_pid=
cleanup() {
  trap - EXIT INT TERM
  [ -z "$airlock_pid" ] || kill "$airlock_pid" 2>/dev/null || true
  [ -z "$relay_pid" ] || kill "$relay_pid" 2>/dev/null || true
  [ -z "$airlock_pid" ] || wait "$airlock_pid" 2>/dev/null || true
  [ -z "$relay_pid" ] || wait "$relay_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 143' INT TERM

(
  cd "$RELAY_DIR"
  exec "$INSTALL_DIR/bin/hornets-relay" --bootstrap-setup --setup-profile operator --setup-host "$SETUP_HOST" --setup-port "$SETUP_PORT"
) &
relay_pid=$!
echo "First run: open the host-mapped operator setup URL on port $SETUP_PORT and review the public relay defaults."

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
  exec "$INSTALL_DIR/bin/airlock"
) &
airlock_pid=$!

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
