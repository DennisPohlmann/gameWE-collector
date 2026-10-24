#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# gameWE - collector: create a new, empty gameWE database and its users.
#
#   COLLECTOR_PASSWORD=... WEB_PASSWORD=... WEB_ADMIN_PASSWORD=... ./init-db.sh [mysql options]
#
# Runs 00_create_database.sql (database, users and their database rights)
# with a MySQL / MariaDB admin account, e.g.  ./init-db.sh -u root -p
# The tables, the reference data and the web users' table rights are then
# created by the collector:  gamewe db init (or at its first start)
#
# Environment:
#   DB_NAME             database name (default gamewe)
#   COLLECTOR_PASSWORD  password of the collector user 'gamewe' (required)
#   WEB_PASSWORD        password of the read-only user 'gamewe_web' (required)
#   WEB_ADMIN_PASSWORD  password of the web admin user 'gamewe_web_admin' (required)
#   MYSQL               client command (default: mysql)
set -euo pipefail

dir=$(cd "$(dirname "$0")" && pwd)
db=${DB_NAME:-gamewe}
mysql=${MYSQL:-mysql}

if [[ -z "${COLLECTOR_PASSWORD:-}" || -z "${WEB_PASSWORD:-}" || -z "${WEB_ADMIN_PASSWORD:-}" ]]; then
	echo "set COLLECTOR_PASSWORD, WEB_PASSWORD and WEB_ADMIN_PASSWORD (see the header of $0)" >&2
	exit 2
fi
if [[ ! "$db" =~ ^[A-Za-z0-9_]+$ ]]; then
	echo "DB_NAME may only contain letters, digits and _" >&2
	exit 2
fi
for pw in "$COLLECTOR_PASSWORD" "$WEB_PASSWORD" "$WEB_ADMIN_PASSWORD"; do
	if [[ "$pw" == *"'"* || "$pw" == *"\\"* ]]; then
		echo "passwords must not contain ' or \\" >&2
		exit 2
	fi
done

# Never touch an existing database (it would also reset the user passwords).
existing=$($mysql "$@" -N -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '$db'")
if [[ "$existing" != "0" ]]; then
	echo "database $db already has $existing tables; init-db.sh only creates new databases" >&2
	exit 1
fi

# Replace the placeholders without passing the passwords on the command line.
esc() { printf '%s' "$1" | sed 's/[&|]/\\&/g'; }
sed -e "s|\`gamewe\`|\`$db\`|g" \
	-e "s|CHANGE_ME_COLLECTOR|$(esc "$COLLECTOR_PASSWORD")|g" \
	-e "s|CHANGE_ME_WEB_ADMIN|$(esc "$WEB_ADMIN_PASSWORD")|g" \
	-e "s|CHANGE_ME_WEB|$(esc "$WEB_PASSWORD")|g" \
	"$dir/00_create_database.sql" | $mysql "$@"
echo "database $db and users created"
echo "next: set database.name: $db, user gamewe and its password in /etc/gamewe/collector/config.yaml, then run: gamewe db init (creates the tables; or the first start of the collector does it), register the game servers in the web admin and run: gamewe check"
