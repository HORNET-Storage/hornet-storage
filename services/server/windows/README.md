# Hornet Storage Windows Service

Windows-specific entry point for the HORNETS relay, built as `hornet-storage.exe` for the Windows installer payload. It wraps the shared run core in `services/server/core` (the same core the default `services/server/port` build uses) with Windows Service Control Manager integration.

## What it does

- **Service mode** (started by the SCM as the `HornetsRelay` service): reports `START_PENDING`, initializes config and logging, starts the relay lifecycle, and reports `RUNNING`. The first-run bootstrap setup phase (`--bootstrap-setup`) executes inside `RUNNING` and short-circuits once the setup marker exists. UPnP initializes only after setup has applied and reloaded the final configuration, preventing template defaults from mapping ports prematurely. `Stop`/`Shutdown` requests trigger the same graceful shutdown as SIGINT/SIGTERM on the port build (websocket drain, database cleanup, sidecar close) with `STOP_PENDING` checkpoints until complete. Lifecycle and fatal events go to the Windows Event Log (source `HornetsRelay`).
- **Console mode** (run from a terminal): behaves like the `services/server/port` build - same flags, same startup sequence, and Ctrl+C performs the same graceful shutdown.

## Environment resolution

Replaces the retired `start-relay.ps1` wrapper, in-process, in both modes:

1. Changes into the relay working directory: `%ProgramData%\Hornet Storage\relay` by default, overridable with `HORNETS_RELAY_DIR`. Config (`config.yaml`), `data\`, and logs all live there.
2. Prepends the executable's own directory to the process `PATH` so the bundled hyperswarm sidecar tooling resolves.
3. Defaults `AIRLOCK_CONFIG_PATH` to `<workdir>\..\airlock\config.yaml` when unset.

## Flags

Identical to the port build: `--compact`, `--profile`, `--bootstrap-setup`, `--setup-host` (default `127.0.0.1`), `--setup-port` (default `11012`), and `--setup-profile` (`nosis` or `operator`, default `nosis`). The installer intentionally registers the service with `--bootstrap-setup` and no profile override, preserving the Nosis desktop/local-relay bootstrap contract. Portable release launchers pass `--setup-profile operator` explicitly. Setup is skipped once the setup marker exists.

## Building

```
GOOS=windows go build -o hornet-storage.exe ./services/server/windows
```

On non-Windows platforms the package builds a stub (`main_stub.go`) so `go build ./...` stays green; the real entry is `//go:build windows`.

## Installation

Service registration (create/delete, recovery ladder, DACL grant for interactive start/stop) lives in `hornets-installer/scripts/windows/manage-windows-service.ps1`.
