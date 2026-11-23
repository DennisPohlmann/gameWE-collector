# gameWE - collector as a container image.
#   docker build --build-arg VERSION=$(cat VERSION) -t gamewe-collector .
# The config (listen + database) is mounted at /etc/gamewe/collector/config.yaml.
# Needs geoip-data/ (make geoip): the DB-IP City Lite database shipped in the
# image at /var/lib/gamewe/collector, where the collector also updates it
# monthly (the volume /var/lib/gamewe, owned by nonroot, uid 65532).
# Every base image is pinned by digest; Dependabot proposes new digests.

FROM busybox:1.38@sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4 AS dirs
RUN mkdir -p /out/var/lib/gamewe/collector

FROM golang:1.27@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
      -ldflags "-s -w -X gamewe-collector/internal/version.Version=${VERSION}" \
      -o /out/gamewe ./cmd/gamewe

# distroless: CA certificates (for database TLS) and a non-root user, no shell.
# No package manager, so its patches come only with a new digest.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
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
