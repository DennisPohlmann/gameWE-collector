# gameWE - collector: development

Building, testing and releasing the collector. The code layout and the rules
for changes are in [CLAUDE.md](../CLAUDE.md).

## Build and test

You need Go (version in `go.mod`, currently 1.27), `make` and GNU tar.

```bash
make test vet                 # tests and static checks
make build                    # build/linux-amd64/gamewe
make package                  # dist/gamewe-collector-<version>-linux-amd64.tar.gz (+ .sha256)
make schema                   # after editing schema/hlstatsx.sql or schema/init/: regenerate schema/postgres/
make docker                   # container image gamewe-collector:<version>
```

The version comes from a `v*` git tag on HEAD, otherwise from `VERSION`. The
binary is static (`CGO_ENABLED=0`). GitHub Actions (`.github/workflows/build.yml`)
builds and tests every push to `main`: the unit tests, govulncheck,
actionlint, the integration tests against MariaDB 11.4 + PostgreSQL 17 and
MariaDB 12.3 + PostgreSQL 18 service containers, the amd64 package (kept as a
workflow artifact) and the container image, which it pushes to
`ghcr.io/dennispohlmann/gamewe-collector` with the tag `edge`.

## Integration tests

`internal/collector/integration_test.go` runs three servers (two games),
interleaved and concurrent, against a real MariaDB / MySQL or PostgreSQL. It
**deletes all rows** of the database it is given, so use a scratch database
created with `init-db.sh`:

```bash
GAMEWE_TEST_DB_TYPE=mysql GAMEWE_TEST_DB_HOST=127.0.0.1 GAMEWE_TEST_DB_PORT=3306 GAMEWE_TEST_DB_USER=gamewe \
GAMEWE_TEST_DB_PASSWORD=... GAMEWE_TEST_DB_NAME=hlstatsx_test GAMEWE_TEST_DB_WIPE=yes \
go test -race -tags integration ./internal/collector/
# PostgreSQL: GAMEWE_TEST_DB_TYPE=postgres GAMEWE_TEST_DB_PORT=5432 ...
```

`internal/database/init_integration_test.go` checks databases freshly created
by both `init-db.sh` scripts (it changes nothing that it does not undo): the
rights of `gamewe_web` and `gamewe_web_admin`, rows inserted without id as
`gamewe_web_admin`, and that both schemas have the same columns, NOT NULLs,
defaults, identity columns and primary keys:

```bash
GAMEWE_TEST_INIT_MYSQL=127.0.0.1:3306/gamewe_init GAMEWE_TEST_INIT_PG=127.0.0.1:5432/gamewe_init \
GAMEWE_TEST_WEB_PASSWORD=... GAMEWE_TEST_WEB_ADMIN_PASSWORD=... \
go test -tags integration -run Init ./internal/database/
```

CI runs both against MariaDB 11.4 and PostgreSQL 17 service containers.

## Releases

Releases are made by [semantic-release](https://semantic-release.gitbook.io/)
(`.releaserc.json`) in the same workflow; nobody tags by hand. To make a
release, push commits written as [Conventional Commits](https://www.conventionalcommits.org/)
to `main`. semantic-release reads the commits since the last tag and decides
the next version:

| Commit | Release |
|---|---|
| `feat: ...` | minor (0.1.0 → 0.2.0) |
| `fix: ...` (also `fix(security): ...`), `perf: ...` | patch (0.1.0 → 0.1.1) |
| `feat!: ...`, `fix!: ...` or a `BREAKING CHANGE:` footer | major (0.1.0 → 1.0.0) |
| `docs`, `chore`, `ci`, `build`, `test`, `refactor`, `style` | no release |

For a release it writes `VERSION` and `CHANGELOG.md`, commits them as
`chore(release): vX.Y.Z [skip ci]`, tags `vX.Y.Z` and creates the GitHub release. The workflow then builds that tag,
uploads the archive and its `.sha256` to the release and pushes the image tags
`<version>`, `<major>.<minor>` and `latest`. Never edit `VERSION` or
`CHANGELOG.md` by hand.
