# gameWE - collector as a container image.
#   docker build --build-arg VERSION=$(cat VERSION) -t gamewe-collector .
# The config (listen + database) is mounted at /etc/gamewe/collector/config.yaml.
# Needs geoip-data/ (make geoip): the DB-IP City Lite database shipped in the
# image at /var/lib/gamewe/collector, where the collector also updates it
# monthly (the volume /var/lib/gamewe, owned by nonroot, uid 65532).

FROM busybox:1.38 AS dirs
RUN mkdir -p /out/var/lib/gamewe/collector

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
      -ldflags "-s -w -X gamewe-collector/internal/version.Version=${VERSION}" \
      -o /out/gamewe ./cmd/gamewe

# distroless: CA certificates (for database TLS) and a non-root user, no shell.
FROM gcr.io/distroless/static-debian13:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="gameWE - collector" \
      org.opencontainers.image.description="Collects Source game-server logs into an HLstatsX database" \
      org.opencontainers.image.source="https://github.com/DennisPohlmann/gameWE-collector" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="GPL-3.0-or-later"
COPY --from=build /out/gamewe /opt/gamewe/collector/bin/gamewe
COPY README.md CHANGELOG.md LICENSE docs/ /opt/gamewe/collector/doc/
COPY schema/ /opt/gamewe/collector/doc/schema/
COPY config.example.yaml /opt/gamewe/collector/doc/config.example.yaml
COPY geoip-data/NOTICE /opt/gamewe/collector/doc/GEOIP-NOTICE
COPY THIRD-PARTY-LICENSES.md /opt/gamewe/collector/doc/
COPY licenses/ /opt/gamewe/collector/doc/licenses/
COPY --from=dirs --chown=nonroot:nonroot /out/var/lib/gamewe /var/lib/gamewe
COPY --chown=nonroot:nonroot geoip-data/dbip-city-lite.mmdb /var/lib/gamewe/collector/
VOLUME /var/lib/gamewe
USER nonroot:nonroot
EXPOSE 41337/udp 41338/tcp
ENTRYPOINT ["/opt/gamewe/collector/bin/gamewe"]
CMD ["run", "-config", "/etc/gamewe/collector/config.yaml"]
