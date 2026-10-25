# Third-party components

gameWE - collector is licensed under GPL-3.0-or-later (`LICENSE`). It
contains, or ships with, the components below. Their full license texts are
in `licenses/` (installed as `/opt/gamewe/collector/doc/licenses/`). All are
used unchanged; their sources are at the URLs given.

`go test ./cmd/gamewe -run ThirdParty` checks that every Go module compiled into
the `gamewe` binary is listed here with its version and license text.

## Compiled into the `gamewe` binary

Go version go1.27.3; modules as `go version -m gamewe` lists them.

| Name | Version | License (SPDX) | URL | Use in gameWE | License text |
|---|---|---|---|---|---|
| `Go standard library and runtime` | 1.27.3 | BSD-3-Clause | https://go.dev | compiled into every Go program (runtime, net, crypto/tls, database/sql, ...) | [`LICENSE`](licenses/go/LICENSE) |
| `filippo.io/edwards25519` | v1.2.0 | BSD-3-Clause | https://github.com/FiloSottile/edwards25519 | used by the MySQL driver (ed25519 authentication plugin) | [`LICENSE`](licenses/filippo.io_edwards25519/LICENSE) |
| `github.com/go-sql-driver/mysql` | v1.10.1 | MPL-2.0 | https://github.com/go-sql-driver/mysql | MariaDB / MySQL database driver | [`LICENSE`](licenses/github.com_go-sql-driver_mysql/LICENSE) |
| `github.com/jackc/pgpassfile` | v1.0.0 | MIT | https://github.com/jackc/pgpassfile | used by pgx (.pgpass files) | [`LICENSE`](licenses/github.com_jackc_pgpassfile/LICENSE) |
| `github.com/jackc/pgservicefile` | v0.0.0-20240606120523-5a60cdf6a761 | MIT | https://github.com/jackc/pgservicefile | used by pgx (service files) | [`LICENSE`](licenses/github.com_jackc_pgservicefile/LICENSE) |
| `github.com/jackc/pgx/v5` | v5.11.0 | MIT | https://github.com/jackc/pgx | PostgreSQL database driver | [`LICENSE`](licenses/github.com_jackc_pgx_v5/LICENSE) |
| `github.com/jackc/puddle/v2` | v2.2.3 | MIT | https://github.com/jackc/puddle | connection pool of pgx | [`LICENSE`](licenses/github.com_jackc_puddle_v2/LICENSE) |
| `github.com/oschwald/maxminddb-golang` | v1.13.1 | ISC | https://github.com/oschwald/maxminddb-golang | reads the GeoIP city database (.mmdb) | [`LICENSE`](licenses/github.com_oschwald_maxminddb-golang/LICENSE) |
| `go.yaml.in/yaml/v3` | v3.0.5 | MIT AND Apache-2.0 | https://github.com/yaml/go-yaml | reads config.yaml | [`LICENSE`](licenses/go.yaml.in_yaml_v3/LICENSE), [`NOTICE`](licenses/go.yaml.in_yaml_v3/NOTICE) |
| `golang.org/x/sync` | v0.24.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/sync | used by pgx (its connection pool puddle) | [`LICENSE`](licenses/golang.org_x_sync/LICENSE) |
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/sys | system calls: the terminal mode of the `gamewe` questions (Linux), used by maxminddb-golang | [`LICENSE`](licenses/golang.org_x_sys/LICENSE) |
| `golang.org/x/text` | v0.43.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/text | text encoding and Unicode handling of pgx | [`LICENSE`](licenses/golang.org_x_text/LICENSE) |

`github.com/go-sql-driver/mysql` is under the Mozilla Public License 2.0: its
source code (unchanged) is available at the URL above; the rest of gameWE is
not affected by the MPL (file-level copyleft).

## Bundled data

| Name | Version | License (SPDX) | URL | Use in gameWE |
|---|---|---|---|---|
| DB-IP IP to City Lite (`dbip-city-lite.mmdb`) | the month of the release (the file's build date), updated monthly by the collector | CC-BY-4.0 | https://db-ip.com/db/download/ip-to-city-lite | player and server locations; shipped as `/var/lib/gamewe/collector/dbip-city-lite.mmdb` |
| HLstatsX:CE game definitions (`sql/install.sql`) | – | GPL-2.0-or-later | https://github.com/A1mDev/hlstatsx-community-edition | teams, roles, actions, weapons, awards and ribbons of cstrike, dod, dods, tfc, valve, ns, tf, hl2mp and csgo in `internal/schema/*/seed.sql`, adapted (docs/GAMES.md, "Default data") |
| HLstatsX:CE with Counter-Strike 2 support, by lovasatt | as of October 2026 | GPL-2.0-or-later | https://github.com/lovasatt/hlstatsx-community-edition | the cs2 rows of `seed.sql` were cross-checked against its cs2 definitions, and its CS2 support (HTTP logs, Source 2 log and status format) was the reference for gameWE's own Source 2 code; no code was copied |

The HLstatsX:CE data is used under the "or any later version" clause of
GPL-2.0-or-later, as part of gameWE under GPL-3.0-or-later (`LICENSE`).
Unlike the components above, it is not used unchanged.

**Attribution required:** pages that show locations from this database must
credit DB-IP, e.g. "IP Geolocation by DB-IP" linking to https://db-ip.com
(`GEOIP-NOTICE`).

## Docker image

| Name | Version | License | URL | Use in gameWE |
|---|---|---|---|---|
| distroless `static-debian13:nonroot` | as of the image build | Apache-2.0 (distroless); the Debian packages in it (`base-files`, `ca-certificates`, `netbase`, `tzdata`) under their own licenses (e.g. GPL-2.0-or-later, MPL-2.0, public domain), see `/usr/share/doc/*/copyright` in the image | https://github.com/GoogleContainerTools/distroless | runtime base of the image (CA certificates for database TLS, the `nonroot` user) |
| `busybox:1.38` | 1.38 | GPL-2.0-only | https://busybox.net | build stage only: creates `/var/lib/gamewe/collector`; not in the final image |
| `golang:1.27` | 1.27 | BSD-3-Clause (Go), and the Debian packages of the image | https://hub.docker.com/_/golang | build stage only: compiles `gamewe`; not in the final image |

## Tests only (not in the binary or the packages)

| Name | Version | License (SPDX) | URL | Use in gameWE | License text |
|---|---|---|---|---|---|
| `github.com/maxmind/mmdbwriter` | v1.2.0 | Apache-2.0 OR MIT | https://github.com/maxmind/mmdbwriter | tests only: writes small test GeoIP databases | [`LICENSE-APACHE`](licenses/github.com_maxmind_mmdbwriter/LICENSE-APACHE), [`LICENSE-MIT`](licenses/github.com_maxmind_mmdbwriter/LICENSE-MIT) |
| `github.com/oschwald/maxminddb-golang/v2` | v2.8.0 | ISC | https://github.com/oschwald/maxminddb-golang | tests only: used by mmdbwriter | [`LICENSE`](licenses/github.com_oschwald_maxminddb-golang_v2/LICENSE) |
| `go4.org/netipx` | v0.0.0-20260823151212-3075585bcbeb | BSD-3-Clause | https://github.com/go4org/netipx | tests only: used by mmdbwriter | [`LICENSE`](licenses/go4.org_netipx/LICENSE) |
