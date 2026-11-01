#!/usr/bin/env bash
# The checks of the CI job "test" (.github/workflows/build.yml), run locally,
# e.g. from the pre-push hook (tools/install-hooks.sh): go vet, go test,
# govulncheck, actionlint and the Helm chart (lint, template, package).
# Not here: the integration tests (they need MariaDB and PostgreSQL, see
# docs/DEVELOPMENT.md), the image scan and TestWebSourceKeys unless
# GAMEWE_WEB_SRC points at a gameWE-web checkout.
#
# Uses go from PATH, otherwise the golang:1.27 image with podman (or docker).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

if command -v go >/dev/null; then
  gorun() { go "$@"; }
else
  engine=$(command -v podman || command -v docker || true)
  if [ -z "$engine" ]; then
    echo "check-all: neither go nor podman/docker found; install Go 1.27 (go.mod)" >&2
    exit 1
  fi
  mkdir -p "$HOME/go/pkg/mod" "$HOME/.cache/go-build-pod"
  web=()
  if [ -n "${GAMEWE_WEB_SRC:-}" ]; then web=(-v "$GAMEWE_WEB_SRC:/web:ro" -e GAMEWE_WEB_SRC=/web); fi
  gorun() {
    "$engine" run --rm --network host -v "$PWD:/src" -w /src \
      -v "$HOME/go/pkg/mod:/go/pkg/mod" -v "$HOME/.cache/go-build-pod:/root/.cache/go-build" \
      "${web[@]}" golang:1.27 go "$@"
  }
fi

step() { printf '\n== %s\n' "$*"; }

step "go vet";        gorun vet ./...
step "go test";       gorun test ./...
if [ -n "${GAMEWE_WEB_SRC:-}" ]; then
  step "web keys ($GAMEWE_WEB_SRC)"
  gorun test -count=1 -run TestWebSourceKeys ./internal/settings
fi
step "govulncheck";   gorun run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
step "actionlint";    gorun run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
step "helm chart"
if command -v helm >/dev/null; then
  helm lint charts/gamewe-collector --strict --set config=x
  helm template gamewe charts/gamewe-collector --set existingSecret=x --set hostNetwork=false --set service.http.enabled=true > /dev/null
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  helm package charts/gamewe-collector --destination "$tmp" > /dev/null
else
  echo "check-all: helm not found (CI uses v3.22.1, .github/actions/setup-helm)" >&2
  exit 1
fi
printf '\ncheck-all: ok\n'
