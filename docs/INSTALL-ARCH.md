# gameWE - collector on Arch Linux

Two ways: the **package** built from `packaging/arch/PKGBUILD` (recommended:
pacman knows the files, user and rights are set up), or the plain release
**tar.gz** as on any Linux. The binary is static, so no other packages are
needed for the collector itself.

## 1. The collector

### As a package (makepkg)

You need `base-devel` and the release archive from the GitHub release (the
PKGBUILD takes the archive from its own directory):

```bash
sudo pacman -S --needed base-devel
mkdir -p ~/build/gamewe && cd ~/build/gamewe
cp /path/to/collector/packaging/arch/* .                   # PKGBUILD, .sysusers, .tmpfiles
cp /path/to/gamewe-collector-0.1.0-linux-amd64.tar.gz .    # the release archive
makepkg -si
```

The package installs

| Path | |
|------|--|
| `/opt/gamewe/collector/bin/gamewe`, `/usr/bin/gamewe` | the binary (and a link to it) |
| `/opt/gamewe/collector/doc/` | documentation and SQL (schema, init scripts) |
| `/etc/gamewe/collector/config.yaml` | the configuration (kept on updates and removal, `pacman -Qii` lists it as backup file) |
| `/usr/lib/systemd/system/gamewe-collector.service` | the systemd unit |
| `/usr/lib/sysusers.d/gamewe-collector.conf` | creates the system user and group `gamewe` |
| `/var/lib/gamewe/collector/dbip-city-lite.mmdb` | the GeoIP database (DB-IP City Lite), updated there by the collector |
| `/usr/lib/tmpfiles.d/gamewe-collector.conf` | `/var/lib/gamewe/collector` `gamewe:gamewe`, and `/etc/gamewe/collector` `0750 root:gamewe`, `config.yaml` `0640 root:gamewe` |

A new release: copy the new archive next to the PKGBUILD, set `pkgver` and the
first `sha256sums` entry (the `.sha256` file of the release, or
`updpkgsums`), then `makepkg -si` again.

### From the tar.gz

The archive is a tree for `/` (`etc/`, `opt/`, `var/`, see README) without a
`config.yaml`:

```bash
mkdir gamewe && tar -xzf gamewe-collector-0.1.0-linux-amd64.tar.gz -C gamewe && cd gamewe
sudo cp -r etc opt /
sudo cp -rn var /
sudo useradd --system --no-create-home --shell /usr/bin/nologin gamewe
sudo cp -n /etc/gamewe/collector/config.example.yaml /etc/gamewe/collector/config.yaml
sudo chown root:gamewe /etc/gamewe/collector /etc/gamewe/collector/config.yaml
sudo chmod 750 /etc/gamewe/collector && sudo chmod 640 /etc/gamewe/collector/config.yaml
sudo chown -R gamewe:gamewe /var/lib/gamewe
sudo systemctl daemon-reload
```

(the unit is then in `/etc/systemd/system/`). For an update copy `etc` and
`opt` again and `var` with `-n` (the collector updates the database itself).

## 2. The database

MariaDB on the same host:

```bash
sudo pacman -S mariadb
sudo mariadb-install-db --user=mysql --basedir=/usr --datadir=/var/lib/mysql
sudo systemctl enable --now mariadb
```

A new gameWE database with its users, tables and default data:

```bash
cd /opt/gamewe/collector/doc/schema/init
sudo env MYSQL=mariadb COLLECTOR_PASSWORD='...' WEB_PASSWORD='...' WEB_ADMIN_PASSWORD='...' ./init-db.sh
```

(as root the `mariadb` client logs in over the socket; a database you created yourself:
`mariadb -u USER -p DBNAME < /opt/gamewe/collector/doc/schema/hlstatsx.sql`).
An existing database: `gamewe check` says what it lacks;
see [DATABASE.md](DATABASE.md), "Incomplete schemas".

PostgreSQL instead:

```bash
sudo pacman -S postgresql
sudo -iu postgres initdb -D /var/lib/postgres/data
sudo systemctl enable --now postgresql
cd /opt/gamewe/collector/doc/schema/postgres/init
sudo -u postgres env COLLECTOR_PASSWORD='...' WEB_PASSWORD='...' WEB_ADMIN_PASSWORD='...' ./init-db.sh
```

and `type: postgres` in the config.

## 3. Configure and start

```bash
sudoedit /etc/gamewe/collector/config.yaml     # database: host 127.0.0.1, user, password, name
sudo -u gamewe gamewe check                    # database, TLS, servers, settings
sudo systemctl enable --now gamewe-collector
journalctl -u gamewe-collector -f
```

Register the game servers in `hlstats_Servers` (web admin, or SQL as in the
README). With `features.log_address` (on by default) the collector registers
itself on every server over RCON; check with `gamewe logaddress`.

Firewall: the game servers send their logs to UDP port 41337 (`listen.port`):

```bash
sudo ufw allow from <game-server-ip> to any port 41337 proto udp       # ufw
sudo firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=<game-server-ip> port port=41337 protocol=udp accept' && sudo firewall-cmd --reload   # firewalld
```

## 4. GeoIP

Nothing to do: the package contains the DB-IP City Lite database at
`/var/lib/gamewe/collector/dbip-city-lite.mmdb` (licence CC BY 4.0, see
`/opt/gamewe/collector/doc/GEOIP-NOTICE`), and the collector updates it there
monthly (systemd `StateDirectory`, owned by `gamewe`).

To use MaxMind GeoLite2 instead (free account, `sudo pacman -S geoipupdate`,
AccountID and LicenseKey in `/etc/GeoIP.conf`, `geoipupdate.timer`), set it in
the config:

```yaml
geoip:
  path: /var/lib/GeoIP/GeoLite2-City.mmdb
  auto_update: false
```

The journal says `GeoIP database loaded` when the collector uses it, or once
`GeoIP database not available` if not.
