---
name: release-security-gate
description: Full security pass over the gameWE collector before release-relevant commits are pushed to main (semantic-release then bumps, tags and releases). Use whenever a release is being prepared; ends with a go/no-go report and never tags or pushes on its own.
---

# Release security gate (gameWE collector)

Run all steps over the whole repository, not only the last diff. Report a
table with the 20 points (ok / finding / n.a.) and a go/no-go.

Releases are made by semantic-release in CI: every push to `main` that
contains a `feat`, `fix` or breaking-change commit (Conventional Commits)
bumps the version, writes `CHANGELOG.md`, tags `vX.Y.Z` and publishes the
GitHub release (and, where there is one, the container image). So this gate
runs **before pushing release-relevant commits to `main`**, without asking
first. Never tag by hand, never edit the version files or `CHANGELOG.md` by
hand, and do not push to `main` yourself: report the gate result and the
version semantic-release will make (`feat` → minor, `fix` → patch,
`!`/`BREAKING CHANGE` → major), then ask Dennis for the go.

## Steps

1. **Clean tree and version:** `git status` is clean; `VERSION` and the
   top `CHANGELOG.md` entry agree with the latest tag
   (`git describe --tags --abbrev=0`), and `make -s version` (the value
   linked into `internal/version`) prints it on that tag.
2. **Tests:** `go vet ./...`, `go test -race ./...`; if a database is
   available, the integration tests from `.github/workflows/build.yml`.
3. **Secrets in the whole history:** prefer
   `gitleaks detect --source . --log-opts="--all"` (or
   `trufflehog git file://. --only-verified`). Without those tools:
   `git log -p --all | grep -nEi '(password|secret|token|BEGIN [A-Z ]*PRIVATE KEY)\s*[:=]' | grep -v change-me`.
   Check that `dist/`, `build/`, `config.yaml`, `*.pem`, `*.key` are
   ignored and not tracked (`git ls-files dist build | head`).
4. **Dependencies:** `govulncheck ./...` (v1.9.0, as in CI), `go list -m -u all` for outdated
   modules; report each vulnerability with module and fixed version.
5. **SQL:** `grep -rn 'Sprintf(' --include=*.go internal | grep -Ei 'select|insert|update|delete|where'`
   and confirm every hit only formats constant identifiers.
6. **Grants:** `go test ./internal/database/ -run Grant` passes and the
   exclusions in `schema/init/03_grants.sql` and the Postgres grants still
   match (no SELECT for the web user on users, RCON events, chat, RCON
   password, connect IPs).
7. **Input fuzz:** for parsers that changed since the last tag
   (`git diff $(git describe --tags --abbrev=0) -- internal/logparse internal/collector`), run or
   add a short fuzz/table test with hostile lines
   (`go test -fuzz=. -fuzztime=30s ./internal/logparse` if a fuzz target exists).
8. **Packaging:** `packaging/*.service` keeps its hardening
   (`NoNewPrivileges`, `ProtectSystem`, dedicated user); Dockerfile runs as
   non-root; example configs contain only placeholders.
9. **The 20 points:** go through the `security-check` skill's list for the
   whole code base, with extra attention on what changed since the last tag
   (`git log --oneline <last-tag>..HEAD`).
10. **Security and privacy audit:** run the `security-privacy-audit`
   skill over the whole repository, all branches and tags since the last
   release, the release commit messages and the `CHANGELOG.md` entry that
   semantic-release will write. Any `BLOCKED` item is a no-go.
11. **Report:** go/no-go, the `security-privacy-audit` summary, findings with file:line and fix, the version semantic-release will make, then ask Dennis
    for the go to push to `main`.
