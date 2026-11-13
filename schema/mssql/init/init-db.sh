#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# gameWE - collector: create a new, empty gameWE database on Microsoft SQL Server.
#
#   COLLECTOR_PASSWORD=... WEB_PASSWORD=... WEB_ADMIN_PASSWORD=... ./init-db.sh [sqlcmd options]
#
# Runs 00_create_database.sql (logins, database, users) as a sysadmin, e.g.
#   ./init-db.sh -S db.example.org -U sa -C
# (sqlcmd reads the sa password from SQLCMDPASSWORD.) The tables, the
# reference data and the web users' table rights are then created by the
# collector:  gamewe db init (or at its first start)
#
# Environment:
#   DB_NAME             database name (default gamewe)
#   COLLECTOR_PASSWORD  password of the collector login 'gamewe' (required)
#   WEB_PASSWORD        password of the read-only login 'gamewe_web' (required)
#   WEB_ADMIN_PASSWORD  password of the web admin login 'gamewe_web_admin' (required)
#   SQLCMD              client command (default: sqlcmd)
set -euo pipefail

dir=$(cd "$(dirname "$0")" && pwd)
db=${DB_NAME:-gamewe}
sqlcmd=${SQLCMD:-sqlcmd}

if [[ -z "${COLLECTOR_PASSWORD:-}" || -z "${WEB_PASSWORD:-}" || -z "${WEB_ADMIN_PASSWORD:-}" ]]; then
	echo "set COLLECTOR_PASSWORD, WEB_PASSWORD and WEB_ADMIN_PASSWORD (see the header of $0)" >&2
	exit 2
fi
if [[ ! "$db" =~ ^[a-z0-9_]+$ ]]; then
	echo "DB_NAME may only contain lower-case letters, digits and _" >&2
	exit 2
fi

# Never touch an existing database with tables (it would also reset the passwords).
tables=$($sqlcmd "$@" -h -1 -W -Q "SET NOCOUNT ON; IF DB_ID(N'$db') IS NOT NULL EXEC (N'SELECT COUNT(*) FROM [$db].INFORMATION_SCHEMA.TABLES') ELSE SELECT 0" | tr -d '[:space:]')
if [[ "$tables" != "0" ]]; then
	echo "database $db already has $tables tables; init-db.sh only creates new databases" >&2
	exit 1
fi

# sqlcmd takes environment variables as scripting variables: the passwords
# are not on the command line. A ' in a password is doubled for the N'...' literal.
quote() { printf '%s' "$1" | sed "s/'/''/g"; }
db=$db collector_pw=$(quote "$COLLECTOR_PASSWORD") web_pw=$(quote "$WEB_PASSWORD") admin_pw=$(quote "$WEB_ADMIN_PASSWORD") \
	$sqlcmd "$@" -b -i /dev/stdin < "$dir/00_create_database.sql"
echo "database $db, logins and users created"
echo "next: set database.type: mssql, name: $db, user gamewe and its password in /etc/gamewe/collector/config.yaml, then run: gamewe db init (creates the tables; or the first start of the collector does it) and gamewe check"
