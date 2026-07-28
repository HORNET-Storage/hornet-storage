# HORNETS Relay Bundle

This archive contains the relay, its web panel, Airlock, and the hyperswarm sidecar as one self-contained stack. No component paths or duplicate Airlock identity are required in the default layout.

## Start

Windows: double-click `start.bat` or run it from Command Prompt.

Linux/macOS:

```sh
chmod +x start.sh bin/hornets-relay bin/airlock bin/hornets-hyperswarm
./start.sh
```

On the first run, open <http://127.0.0.1:11012>. The portable launchers deliberately select the operator setup profile, which starts with public relay admission and UPnP disabled. Review the relay bind address, base port, Airlock loopback settings, and data paths, then apply setup. The launcher starts Airlock automatically; later launches use the saved configuration immediately.

Leave the private-key fields blank for the normal flow. The backend generates the relay identity and an unrelated shared secret, derives the relay DHT identity, and derives Airlock's domain-separated DHT identity without returning secrets to the browser. Manually entered identity values are redacted from the payload preview. Back up `relay/config.yaml` after setup because it contains the generated relay identity and is written with restricted permissions.

Keep the launcher open. It supervises the relay and Airlock and stops the other component when either exits.

## What is automatic

- The relay and Airlock share one hyperswarm sidecar on `127.0.0.1:9100`. Each process discovers `bin/hornets-hyperswarm` automatically.
- When Airlock's top-level `private_key` is blank, it reads `relay.private_key` from `relay/config.yaml` and derives an Airlock-specific, domain-separated DHT seed. The raw key is not duplicated or reused directly as an Ed25519 seed.
- Relay public and DHT identities are derived from the relay private key. A secure relay key is generated server-side when one is not supplied.
- Fresh operator setup uses `allowed_users.mode: public` with read/write admission set to `all_users`, and leaves UPnP off. Repository permission events, private-repository isolation, Airlock authorization, signatures, bundles, and DAG verification remain separate enforcement layers.
- The version-matched relay panel is served from `relay/web`.
- Generated configs and runtime data stay under `relay/` and `airlock/`; custom relay data and repository paths can be selected during setup.

Explicit `sidecar.executable`, `HORNETS_SIDECAR_EXECUTABLE`, Airlock's top-level `private_key`, `AIRLOCK_PRIVATE_KEY`, and the server/launcher-owned `AIRLOCK_CONFIG_PATH` remain supported for custom layouts. The Airlock config target is server-owned during operator setup, so a browser payload cannot redirect protected configuration writes.

## Ports

- `11000`: default relay base endpoint (configurable during setup)
- `11002`: default relay web endpoint
- `11006`: default Airlock base service setting (configurable during setup)
- `11007`: default Airlock Nostr proxy when enabled
- `11012`: loopback-only first-time setup for the Windows/POSIX launchers

Only the first-time setup endpoint is deliberately bound to loopback by the Windows and POSIX launchers. The relay itself defaults to `0.0.0.0`; configure firewall, reverse-proxy, TLS, and public relay exposure for your deployment before opening ports. Airlock defaults to `127.0.0.1` and should remain private unless you have a deliberate protected topology.

## Docker

The `linux-amd64` release bundle can be turned into an image without compilers. Platform-native Windows and macOS binaries cannot run inside this Linux image:

```sh
docker compose up --build
```

The Docker entrypoint also selects the operator profile. The compose file binds first-time setup to host loopback; open <http://127.0.0.1:11012>, review the public relay defaults, complete setup, then keep the container running. Persistent relay and Airlock data are stored in named volumes. The image build uses an allowlist so generated configs, keys, repositories, logs, and runtime data cannot be copied into the image context.

Docker cannot change published host ports from the browser. If setup uses a relay base port other than `11000`, update the compose `ports` entries for the relay base port and web port (`base + 2`), then recreate the container.

For a public P2P deployment, verify UDP/NAT behavior on the Docker host. Host networking can improve HyperDHT reachability on Linux, but changes the isolation and port-publishing model.
