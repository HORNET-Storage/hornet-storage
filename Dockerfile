# Build context must be the hornets-suite directory so the local Go module
# replacements and the three sibling source repositories are available.
FROM node:24-bookworm AS sidecar-builder

WORKDIR /src/hornets-hyperswarm
COPY hornets-hyperswarm/ ./
RUN npm install --global npm@11.10.0 \
    && npm ci \
    && npm run build

FROM node:24-bookworm AS panel-builder

WORKDIR /src/hornets-relay-panel
COPY hornets-relay-panel/ ./
RUN corepack enable \
    && corepack prepare yarn@1.22.19 --activate \
    && yarn install --frozen-lockfile \
    && yarn build

FROM golang:1.25-bookworm AS go-builder

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential git \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY hornets-nostr-relay/ hornets-nostr-relay/
COPY airlock/ airlock/
COPY nosis-cli/ nosis-cli/
COPY hornets-hyperswarm/ hornets-hyperswarm/

RUN mkdir -p /out/bin \
    && cd /src/hornets-nostr-relay \
    && CGO_ENABLED=1 go build -buildvcs=false -trimpath -o /out/bin/hornets-relay ./services/server/port \
    && cd /src/airlock \
    && CGO_ENABLED=1 go build -buildvcs=false -trimpath -o /out/bin/airlock .

FROM go-builder AS compliance-builder

ARG RELAY_REVISION=working-tree
ARG AIRLOCK_REVISION=working-tree
ARG HYPERSWARM_REVISION=working-tree
ARG NOSIS_CLI_REVISION=working-tree
ARG RELAY_PANEL_REVISION=working-tree

COPY --from=sidecar-builder /usr/local/bin/node /usr/local/bin/node
COPY --from=sidecar-builder /src/hornets-hyperswarm/ /src/hornets-hyperswarm/
COPY --from=panel-builder /src/hornets-relay-panel/ /src/hornets-relay-panel/

RUN node /src/hornets-nostr-relay/scripts/compliance/generate.mjs \
    --workspace /src \
    --output /out/compliance \
    --artifact hornets-relay-container \
    --relay-revision "$RELAY_REVISION" \
    --airlock-revision "$AIRLOCK_REVISION" \
    --hyperswarm-revision "$HYPERSWARM_REVISION" \
    --nosis-cli-revision "$NOSIS_CLI_REVISION" \
    --panel-revision "$RELAY_PANEL_REVISION"

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 hornets

WORKDIR /opt/hornets
COPY --from=go-builder /out/bin/ ./bin/
COPY --from=sidecar-builder /src/hornets-hyperswarm/dist/hornets-hyperswarm ./bin/hornets-hyperswarm
COPY --from=sidecar-builder /src/hornets-hyperswarm/dist/prebuilds ./bin/prebuilds
COPY hornets-nostr-relay/config.example.yaml ./relay/config.example.yaml
COPY --from=panel-builder /src/hornets-relay-panel/build/ ./relay/web/
COPY airlock/config.example.yaml ./airlock/config.example.yaml
COPY --from=compliance-builder /out/compliance/ ./compliance/
COPY hornets-nostr-relay/release/bundle/docker/entrypoint.sh ./entrypoint.sh

RUN chmod 0755 ./bin/hornets-relay ./bin/airlock ./bin/hornets-hyperswarm ./entrypoint.sh \
    && test -f ./compliance/THIRD-PARTY-NOTICES.json \
    && test -f ./compliance/SOURCE-MANIFEST.json \
    && test -f ./compliance/FIRST_PARTY/airlock/LICENSE \
    && mkdir -p /data/relay /data/airlock \
    && chown -R hornets:hornets /opt/hornets /data

USER hornets
VOLUME ["/data/relay", "/data/airlock"]
EXPOSE 11000 11002 11006 11007 11012
ENTRYPOINT ["/opt/hornets/entrypoint.sh"]
