#!/usr/bin/env bash
set -e

# Always operate from the script's folder (repo root)
cd "$(dirname "$0")"

# --- Config ---
REPO_URL="https://github.com/HORNET-Storage/HORNETS-Relay-Panel.git"
PANEL_DIR="panel-source"
BACKEND_EXE="./hornet-storage"
CONFIG_FILE="config.yaml"
export NODE_OPTIONS="--openssl-legacy-provider --max-old-space-size=4096"
# -------------

echo
echo "==============================="
echo "HORNETS-Relay-Panel Dev Runner"
echo "==============================="
echo

# Check if config.yaml exists and read port early
CONFIG_EXISTS=0
BASE_PORT=""
if [ -f "$CONFIG_FILE" ]; then
  CONFIG_EXISTS=1
  PARSED_PORT=$(grep "port:" "$CONFIG_FILE" | grep -v "http" | head -1 | sed 's/.*port:\s*//' | tr -d '[:space:]')
  if [ -n "$PARSED_PORT" ]; then
    BASE_PORT="$PARSED_PORT"
    WEB_PORT=$((BASE_PORT + 2))
    DEV_PORT=$((BASE_PORT + 3))
    echo "Config found - Base port: $BASE_PORT - API port: $WEB_PORT - Dev server port: $DEV_PORT"
  fi
fi

if [ "$CONFIG_EXISTS" -eq 0 ]; then
  echo "No config.yaml found - relay will generate it on first run."
fi

# 1) Clone panel if missing (no pull/update if it already exists)
if [ ! -d "$PANEL_DIR" ]; then
  if ! command -v git >/dev/null 2>&1; then
    echo "ERROR: git not found in PATH."
    exit 1
  fi
  echo "Cloning panel to $PANEL_DIR ..."
  git clone "$REPO_URL" "$PANEL_DIR" || { echo "ERROR: clone failed."; exit 1; }
fi

# Sanity check the panel project exists
if [ ! -f "$PANEL_DIR/package.json" ]; then
  echo "ERROR: $PANEL_DIR/package.json not found."
  exit 1
fi

# 2) Build the RELAY using root build.sh
if [ ! -f "build.sh" ]; then
  echo "ERROR: Root build.sh not found."
  exit 1
fi
echo "Running root build.sh (relay)..."
./build.sh

# 3) Run the backend exe from the root
if [ ! -f "$BACKEND_EXE" ]; then
  echo "ERROR: $BACKEND_EXE not found in repo root after build."
  echo "Tip: confirm build.sh outputs the binary to the root, or adjust BACKEND_EXE path."
  exit 1
fi
echo "Starting backend: $BACKEND_EXE"
$BACKEND_EXE &

# 4) If config didn't exist before, wait for relay to generate it and read port
if [ "$CONFIG_EXISTS" -eq 0 ]; then
  echo "Waiting for relay to generate config.yaml..."
  sleep 3

  if [ -f "$CONFIG_FILE" ]; then
    PARSED_PORT=$(grep "port:" "$CONFIG_FILE" | grep -v "http" | head -1 | sed 's/.*port:\s*//' | tr -d '[:space:]')
    if [ -n "$PARSED_PORT" ]; then
      BASE_PORT="$PARSED_PORT"
    fi
  fi

  if [ -z "$BASE_PORT" ]; then
    echo "WARNING: Could not read port from config.yaml, using default 11000"
    BASE_PORT="11000"
  fi

  WEB_PORT=$((BASE_PORT + 2))
  DEV_PORT=$((BASE_PORT + 3))
  echo "Config generated - Base port: $BASE_PORT - API port: $WEB_PORT - Dev server port: $DEV_PORT"
fi

# 5) Update .env.development with the correct web port
echo "Updating .env.development with port $WEB_PORT..."
if [ -f "$PANEL_DIR/.env.development" ]; then
  if grep -q "REACT_APP_BASE_URL=" "$PANEL_DIR/.env.development"; then
    sed -i "s|REACT_APP_BASE_URL=http://localhost:[0-9]*|REACT_APP_BASE_URL=http://localhost:$WEB_PORT|g" "$PANEL_DIR/.env.development"
  else
    echo "REACT_APP_BASE_URL=http://localhost:$WEB_PORT" >> "$PANEL_DIR/.env.development"
  fi
fi

# 6) Start the panel in dev mode (current process)
echo
echo "Starting panel dev server (dev mode)..."
cd "$PANEL_DIR"

# Install exactly the repository-pinned dependency graph.
corepack enable
corepack prepare yarn@1.22.19 --activate
yarn install --frozen-lockfile

# Create themes directory if it doesn't exist and build themes
echo "Building themes for development..."
mkdir -p "public/themes"
node_modules/.bin/lessc --js --clean-css="--s1 --advanced" src/styles/themes/main.less public/themes/main.css

echo "Starting React dev server on port $DEV_PORT..."
PORT=$DEV_PORT NODE_ENV=development exec yarn craco start
