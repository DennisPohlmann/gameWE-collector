# gameWE - collector
#   make build | test | vet | package | schema | clean
#
# Builds for Linux x86_64 (amd64) only.
#
# The version comes from the git tag on HEAD (v1.2.3 -> 1.2.3), otherwise
# from the VERSION file. Override with: make package VERSION=1.2.3-rc1

GO      ?= go
GIT_TAG := $(shell git describe --tags --exact-match --match 'v*' 2>/dev/null)
VERSION ?= $(if $(GIT_TAG),$(patsubst v%,%,$(GIT_TAG)),$(shell cat VERSION))

# Not named GOFLAGS: that is an environment variable read by go itself.
BUILD_FLAGS := -trimpath
LDFLAGS     := -s -w -X gamewe-collector/internal/version.Version=$(VERSION)

BUILD_DIR := build/linux-amd64
BIN       := $(BUILD_DIR)/gamewe
STAGE     := build/stage-amd64
PKG       := gamewe-collector-$(VERSION)-linux-amd64
OPT       := opt/gamewe/collector
DIST      := dist

.PHONY: all build test vet package clean schema docker version geoip licenses

all: vet test build

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/gamewe

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# The DB-IP City Lite database shipped in the package and the image at
# /var/lib/gamewe/collector, where the collector also updates it
# (CC BY 4.0, packaging/geoip-NOTICE): downloaded once into geoip-data/,
# delete the file to fetch a newer one.
GEOIP_DB := geoip-data/dbip-city-lite.mmdb

geoip: $(GEOIP_DB)

$(GEOIP_DB): build
	install -d geoip-data
	$(BIN) geoip-update --force -file $@
	install -m 0644 packaging/geoip-NOTICE geoip-data/NOTICE

package: build geoip
	rm -rf $(STAGE)
	umask 022 && install -d $(STAGE)/$(OPT)/bin $(STAGE)/$(OPT)/doc/schema/init \
		$(STAGE)/var/lib/gamewe/collector $(STAGE)/etc/gamewe/collector $(STAGE)/etc/systemd/system
	install -m 0755 $(BIN) $(STAGE)/$(OPT)/bin/gamewe
	install -m 0644 $(GEOIP_DB) $(STAGE)/var/lib/gamewe/collector/
	install -m 0644 geoip-data/NOTICE $(STAGE)/$(OPT)/doc/GEOIP-NOTICE
	install -m 0644 THIRD-PARTY-LICENSES.md $(STAGE)/$(OPT)/doc/
	cp -r licenses $(STAGE)/$(OPT)/doc/licenses && chmod -R u=rwX,go=rX $(STAGE)/$(OPT)/doc/licenses
	install -m 0644 README.md CHANGELOG.md LICENSE docs/*.md $(STAGE)/$(OPT)/doc/
	install -m 0644 schema/init/00_create_database.sql $(STAGE)/$(OPT)/doc/schema/init/
	install -m 0755 schema/init/init-db.sh $(STAGE)/$(OPT)/doc/schema/init/
	install -m 0640 config.example.yaml $(STAGE)/etc/gamewe/collector/config.example.yaml
	install -m 0644 packaging/gamewe-collector.service $(STAGE)/etc/systemd/system/gamewe-collector.service
	umask 022 && install -d $(STAGE)/$(OPT)/doc/schema/postgres/init
	install -m 0644 schema/postgres/init/00_create_database.sql $(STAGE)/$(OPT)/doc/schema/postgres/init/
	install -m 0755 schema/postgres/init/init-db.sh $(STAGE)/$(OPT)/doc/schema/postgres/init/
	install -d $(DIST)
	tar --sort=name --owner=0 --group=0 --numeric-owner -C $(STAGE) -czf $(DIST)/$(PKG).tar.gz etc opt var
	cd $(DIST) && sha256sum $(PKG).tar.gz > $(PKG).tar.gz.sha256
	@echo "built $(DIST)/$(PKG).tar.gz"

version:
	@echo $(VERSION)

IMAGE ?= gamewe-collector
docker: geoip
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

# Generate the PostgreSQL files (internal/schema/postgres/) from the MariaDB
# ones (internal/schema/mysql/). Both are compiled into the binary; the
# package ships only the init scripts (schema/init/: database and users).
schema:
	$(GO) run ./tools/schemagen

# License texts of the Go modules (licenses/, with modules.json) from the
# module cache; run after a dependency change, then update
# THIRD-PARTY-LICENSES.md (the license test in cmd/gamewe checks both).
licenses:
	$(GO) run ./tools/licensegen

clean:
	rm -rf build $(DIST)
