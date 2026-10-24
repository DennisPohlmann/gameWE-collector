# gameWE - collector: development

Building, testing and releasing the collector. The code layout and the rules
for changes are in [CLAUDE.md](../CLAUDE.md).

## Build and test

You need Go (version in `go.mod`, currently 1.27), `make` and GNU tar.

```bash
make test vet                 # tests and static checks
make build                    # build/linux-amd64/gamewe
make package                  # dist/gamewe-collector-<version>-linux-amd64.tar.gz (+ .sha256)
make schema                   # after editing internal/schema/mysql/*.sql: regenerate internal/schema/postgres/
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
created with `init-db.sh` and `gamewe db migrate -f`:

```bash
GAMEWE_TEST_DB_TYPE=mysql GAMEWE_TEST_DB_HOST=127.0.0.1 GAMEWE_TEST_DB_PORT=3306 GAMEWE_TEST_DB_USER=gamewe \
GAMEWE_TEST_DB_PASSWORD=... GAMEWE_TEST_DB_NAME=hlstatsx_test GAMEWE_TEST_DB_WIPE=yes \
go test -race -tags integration ./internal/collector/
# PostgreSQL: GAMEWE_TEST_DB_TYPE=postgres GAMEWE_TEST_DB_PORT=5432 ...
```

`internal/database/init_integration_test.go` checks databases freshly created
by both `init-db.sh` scripts and `gamewe db migrate` (it changes nothing that it does not undo): the
rights of `gamewe_web` and `gamewe_web_admin`, rows inserted without id as
`gamewe_web_admin`, and that both schemas have the same columns, NOT NULLs,
defaults, identity columns and primary keys:

```bash
GAMEWE_TEST_INIT_MYSQL=127.0.0.1:3306/gamewe_init GAMEWE_TEST_INIT_PG=127.0.0.1:5432/gamewe_init \
GAMEWE_TEST_WEB_PASSWORD=... GAMEWE_TEST_WEB_ADMIN_PASSWORD=... \
go test -tags integration -run Init ./internal/database/
```

The migration tests (`internal/database/migrate_integration_test.go`,
`cmd/gamewe/migrate_integration_test.go`) create and drop their own databases
`mig_*` with the server's admin account and run `gamewe db migrate` on a new
database, on the old HLstatsX structure gameWE started from
(`internal/database/testdata/hlstatsx-old.sql` plus synthetic rows), on
gameWE 0.2.0 / 0.2.1 databases, with missing rights, with two runs at the
same time, and the binary end to end (`run` refuses an old schema with exit
code 1 with `database.auto_migrate: false`, `db migrate -n` / `-f` / the
question, `run` starts afterwards; `run` migrates a new database itself with
`auto_migrate`, refuses an HLstatsX database without versions and stops on
missing rights). They
also set the passwords of the users `gamewe`, `gamewe_web` and
`gamewe_web_admin` (`COLLECTOR_PASSWORD`, `WEB_PASSWORD`,
`WEB_ADMIN_PASSWORD`): only point them at a throwaway server.

```bash
GAMEWE_TEST_MIGRATE_TYPE=mysql GAMEWE_TEST_MIGRATE_HOST=127.0.0.1 GAMEWE_TEST_MIGRATE_PORT=3306 \
GAMEWE_TEST_MIGRATE_ADMIN_USER=root GAMEWE_TEST_MIGRATE_ADMIN_PASSWORD=... \
go test -p 1 -tags integration -run Migrate ./internal/database/ ./cmd/gamewe/
# PostgreSQL: GAMEWE_TEST_MIGRATE_TYPE=postgres ... ADMIN_USER=postgres
```

CI runs all of them against both MariaDB / PostgreSQL pairs of service
containers.

### Adding a migration

The target schema lives in `internal/schema/mysql/` (`schema.sql`,
`seed.sql`, `grants.sql`, compiled into the binary with go:embed); the
PostgreSQL files in `internal/schema/postgres/` are generated from them. The
versions are the list `Migrations` in `internal/database/migrations.go`.
There are no SQL files per migration and no dialect variants to write: a
migration names *what* an older database needs, the definitions come from the
schema files, and the migrator builds the DDL per dialect (one `ALTER TABLE`
per table on MariaDB, `ADD COLUMN IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`
on PostgreSQL), each step only if the information_schema says it is missing.

Example, a new column `hlstats_Servers.log_secret`:

1. Add the column (or index, or table) to `internal/schema/mysql/schema.sql`,
   in the `mariadb-dump` format of the file (one column or key per line,
   backquoted names). A new table also needs its rights in `grants.sql`
   (`TestWebUserGrants` fails without); a column the public web must not read
   (a secret) goes into the column list exclusion of `gamewe_web` there, like
   `rcon_password`.
2. `make schema` (regenerates the PostgreSQL files; `TestPostgresFilesUpToDate`
   fails if you forget).
3. Append to `Migrations`, with the next version number (no gaps, never reuse
   one), a short kebab-case `Name` and a one-line `Desc` for the plan:
   ```go
   {Version: 4, Name: "server-log-secret", Desc: "column hlstats_Servers.log_secret (...)", Sync: true},
   ```
   `Sync` adds every table, column and index of the schema the database
   lacks. `Seed: true` additionally loads `seed.sql` (only `INSERT IGNORE`:
   missing rows; use it when you added reference data). `Drop` lists indexes
   a new one replaces (dropped only once the replacement exists). Anything
   else (data fixes, type changes) needs new code in `migrate.go` and must
   stay idempotent and never delete user data.
4. Never change a migration that was released: its checksum is recorded,
   `gamewe db status` warns about a difference. Write a new one.
5. Tests: `go test ./internal/schema/ ./internal/database/ ./tools/...` (schema
   parsing, both dialects equal, plan steps), then the integration tests
   against throwaway MariaDB and PostgreSQL containers (above,
   `-run Migrate`): a new database and every upgrade path must end up to
   date, a second run must be a no-op. Add a case to
   `internal/database/migrate_integration_test.go` if the migration does more
   than `Sync`.
6. Docs: the version table in [DATABASE.md](DATABASE.md#schema-versions-and-upgrades)
   and, for a new column, [DB-SEMANTICS.md](DB-SEMANTICS.md).

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
