![nostr Badge](https://img.shields.io/badge/nostr-8e30eb?style=flat) ![Go Badge](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white) <img src="https://static.wixstatic.com/media/e9326a_3823e7e6a7e14488954bb312d11636da~mv2.png" height="20">

# H.O.R.N.E.T Storage Nostr Relay 🐝

Unleashing the power of Nostr with a ***configurable all-in-one relay*** supporting unchunked files as Blossom Blobs, chunked files as Scionic Merkle Trees, and various social media features as Nostr kind numbers.

### Choose Kind Numbers and File Extensions
Relay operators can select which file types and nostr features to enable in the [H.O.R.N.E.T Storage Relay Panel](https://github.com/HORNET-Storage/hornet-storage-panel) with elegant GUI toggles, displayed alongside diagrams and graphs to visualize the amount of data hosted over time.

### 17 Supported Nostr Features (NIPs)
**✅ - Implemented:** Features that are currently available and fully operational.
**⚠️ - In-Progress:** Features that are currently under development and not yet released.

| NIP Number | NIP Description                        | Kind Number Description                                                      |
|------------|------------------------------------|-------------------------------------------------------------------|
| NIP-01     | Basic Nostr Protocol               | [***kind0***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind0) → User Metadata ✅<br><br>[***kind1***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind1) → Short Text Post [Immutable] ✅ |
| NIP-02     | Following List                        | [***kind3***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind3) → List of Users You Follow ✅                                         |
| NIP-05     | Mapping Nostr Address to DNS   | No Specific Kinds Listed ✅                                       |
| NIP-09     | Delete Note                        | [***kind5***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind5) → Delete Request ✅                                         |
| NIP-11     | Relay Info Document                | No Specific Kinds Listed ✅                                       |
| NIP-18     | Reposts                            | [***kind6***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind6) → Repost of Kind1 Notes ✅<br><br>[***kind16***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind16) → Repost of All Other Kind Notes ✅ |
| NIP-23     | Formatted Articles                 | [***kind30023***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind30023) → Markdown Post [Updatable] ✅                        |
| NIP-25     | Reactions                          | [***kind7***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind7) → Like, Heart, or Custom Reaction ✅                        |
| NIP-45     | Counting Followers & more...          | No Specific Kinds Listed ✅                                       |
| NIP-50     | Search Capability                  | No Specific Kinds Listed ✅                                       |
| NIP-51     | Custom Lists                       | [***kind10000***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind10000) → Mute List ✅<br><br>[***kind10001***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind10001) → Pinned Note ✅<br><br>*kindxxxx* → Private Follow List [Encrypted] ⚠️<br><br>*kindxxxx* → Private Bookmark [Encrypted] ⚠️<br><br>[***kind30000***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind30000) → Public Follow List [Unencrypted] ✅ |
| NIP-56     | Reporting                          | [***kind1984***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind1984) → Report a User, Post, or Relay ✅                       |
| NIP-57     | Lightning Zaps                     | [***kind9735***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind9735) → Lightning Zap Receipt ✅                                         |
| NIP-58     | Badges                             | [***kind8***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind8) → Badge Award ✅<br><br>[***kind30008***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind30008) → Profile Badge ✅<br><br>[***kind30009***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind30009) → Badge Definition ✅ |
| NIP-65     | Propagate Tiny Relay Lists         | [***kind10002***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind10002) → Tiny Relay List [Outbox Model] ✅                          |
| NIP-84     | Highlights                         | [***kind9802***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind9802) → Snippet of a Post or Article ✅                       |
| NIP-116    | Event Paths                        | [***kind30079***](https://github.com/HORNET-Storage/hornet-storage/tree/main/lib/handlers/nostr/kind30079) → Paths Instead of Kind Numbers ✅                     |



## Run the complete relay stack

The GitHub release archives are the normal deployment path. Each platform archive contains:

- `hornets-relay`, Airlock, and the platform-matched hyperswarm sidecar.
- Native sidecar prebuilds.
- The version-matched relay web panel.
- Relay and Airlock example configuration.
- `start.bat` for Windows and `start.sh` for Linux/macOS.
- Docker runtime assets and a build manifest containing all source revisions.

Extract one archive, keep its directory structure intact, and run:

```powershell
.\start.bat
```

or:

```bash
chmod +x start.sh
./start.sh
```

The launcher starts the relay first. On a new installation it waits for the first-run setup to create the relay and Airlock configuration, then starts Airlock. It supervises only the processes it started and cleans them up on exit.

Open `http://127.0.0.1:11012` for first-run setup. The setup port is loopback-only in the supplied launchers and Compose files. The default relay endpoints are `11000` and `11002`; Airlock's optional HTTP endpoint is configured for loopback on `11006` and is not published by the Docker configuration.

### Configuration contract

The standard release layout is:

```text
hornets-relay-<platform>/
├── bin/
│   ├── hornets-relay
│   ├── airlock
│   ├── hornets-hyperswarm
│   └── prebuilds/
├── relay/
│   ├── config.example.yaml
│   └── web/
├── airlock/
│   └── config.example.yaml
├── start.bat
├── start.sh
└── README.md
```

No private key or executable path is copied between processes:

- The relay owns the relay Nostr identity.
- Airlock uses its explicit `private_key` when configured; otherwise it reads the relay key through `relay_config_path` and derives an Airlock-specific, domain-separated DHT seed. The raw relay key is not copied or reused directly as an Ed25519 seed.
- The shared Go client discovers a sidecar beside the caller, in the working directory, on `PATH`, or in standard Hornet Storage install locations.
- Explicit config and environment values always take precedence, preserving standalone and service deployments.

Generated configuration contains secrets. Restrict it to the service account and do not commit it.

## Docker

From a `linux-amd64` release archive:

```bash
docker compose up -d
```

From the sibling source checkout (`hornets-suite/hornets-nostr-relay`, `airlock`, `nosis-cli`, `hornets-hyperswarm`, and `hornets-relay-panel`):

```bash
./docker/build.sh
docker compose up -d
```

On Windows use `docker\build.bat`, then `docker compose up -d`. Persistent relay and Airlock data live in named volumes. The setup UI is mapped only to `127.0.0.1:11012`. Operators who need inbound DHT/UDP behavior beyond Docker bridge networking should configure host networking or explicit deployment-specific networking deliberately.

## Build the complete stack from source

Requirements: Go 1.25+, Git, Node.js 22+, npm 11.10+, Corepack, Yarn Classic, and a C compiler for the relay and Airlock CGO builds. The relay keeps CGO enabled because its existing statistics store uses SQLite; Bleve remains embedded and does not add another native runtime service. Keep Airlock, Nosis CLI, hyperswarm, and the relay panel as sibling repositories because the development build uses the local checked-out sources.

Badger remains the authoritative event database. NIP-50 uses an embedded, derived Bleve index beside the configured Badger path; it adds no service, port, or separately managed process and is rebuilt automatically if it is missing, incompatible, or corrupt.

Linux/macOS:

```bash
./build-stack.sh
```

Windows:

```powershell
.\build-stack.bat
```

The development bundle is written to `dist/hornets-relay-dev` with the same layout as a release archive. Individual relay-only build scripts remain available for standalone development and the existing service installer continues to use its explicit configuration paths.

## Release workflow

Pushing a `v*` tag runs `.github/workflows/release.yml`. It checks out the matching component repositories, builds each binary and the relay panel natively on Linux x64, Windows x64, macOS Intel, and macOS Apple Silicon, validates the complete runtime layout, and publishes ready-to-extract archives with SHA-256 checksum files. If sibling repositories are private, configure `HORNETS_REPO_TOKEN` with read access; public repositories can use the workflow token.

Manual workflow runs build and retain the archives without publishing a GitHub release. Component refs are explicit inputs so a release can pin reviewed Airlock, hyperswarm, Nosis CLI, and relay-panel revisions.

## Additional services

The relay can also integrate with:

- [Super Neutrino Wallet](https://github.com/HORNET-Storage/Super-Neutrino-Wallet) for paid relay features.
- [NestShield](https://github.com/HORNET-Storage/NestShield) for content moderation.
- [Ollama](https://ollama.com/download) for advanced local moderation.

## Security and verification

Bundling changes process discovery and deployment ergonomics only. Airlock remains the verification boundary for repository pushes and pulls; authentication, signatures, permissions, DAG verification, and relay/Airlock trust rules are unchanged. The relay and Airlock can still run separately, can use separate identities, and can connect to an externally managed persistent sidecar.
