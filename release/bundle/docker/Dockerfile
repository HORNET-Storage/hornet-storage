FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 hornets

COPY bin/ /opt/hornets/bin/
COPY relay/config.example.yaml /opt/hornets/relay/config.example.yaml
COPY relay/web/ /opt/hornets/relay/web/
COPY airlock/config.example.yaml /opt/hornets/airlock/config.example.yaml
COPY compliance/ /opt/hornets/compliance/
COPY BUILD-METADATA.txt /opt/hornets/BUILD-METADATA.txt
COPY docker/entrypoint.sh /opt/hornets/docker/entrypoint.sh
RUN chmod 0755 /opt/hornets/bin/hornets-relay \
    /opt/hornets/bin/airlock \
    /opt/hornets/bin/hornets-hyperswarm \
    /opt/hornets/docker/entrypoint.sh \
    && test -f /opt/hornets/compliance/THIRD-PARTY-NOTICES.json \
    && test -f /opt/hornets/compliance/SOURCE-MANIFEST.json \
    && test -f /opt/hornets/compliance/FIRST_PARTY/airlock/LICENSE \
    && mkdir -p /data/relay /data/airlock \
    && chown -R hornets:hornets /opt/hornets /data

USER hornets
VOLUME ["/data/relay", "/data/airlock"]
EXPOSE 11000 11002 11006 11007 11012
ENTRYPOINT ["/opt/hornets/docker/entrypoint.sh"]
