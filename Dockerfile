# gameWE - collector as a container image.
#   docker build --build-arg VERSION=$(cat VERSION) -t gamewe-collector .
# The config (listen + database) is mounted at /etc/gamewe/collector/config.yaml.
# Needs geoip-data/ (make geoip): the DB-IP City Lite database shipped in the
# image at /var/lib/gamewe/collector, where the collector also updates it
# monthly (the volume /var/lib/gamewe, owned by nonroot, uid 65532).
# Every base image is pinned by digest; Dependabot proposes new digests.

FROM busybox:1.38@sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4 AS dirs
RUN mkdir -p /out/var/lib/gamewe/collector

FROM golang:1.27@sha256:01aa404fa56b604b6f48e90c1d6e2c79fe5cd78f9592f78d80303fbe171cdcc0 AS build
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
FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
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
