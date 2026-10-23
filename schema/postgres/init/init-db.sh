#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# gameWE - collector: create a new, empty gameWE database on PostgreSQL.
#
#   COLLECTOR_PASSWORD=... WEB_PASSWORD=... WEB_ADMIN_PASSWORD=... ./init-db.sh [psql options]
#
# Runs 00_create_database.sql (roles and database), the schema
# (01_schema.sql, or ../hlstatsx.sql in the source tree), 02_seed.sql and
# 03_grants.sql as a PostgreSQL superuser, e.g.  ./init-db.sh -U postgres -h db.example.org
#
# Environment:
#   DB_NAME             database name (default gamewe)
#   COLLECTOR_PASSWORD  password of the collector role 'gamewe' (required)
#   WEB_PASSWORD        password of the read-only role 'gamewe_web' (required)
#   WEB_ADMIN_PASSWORD  password of the web admin role 'gamewe_web_admin' (required)
#   PSQL                client command (default: psql)
set -euo pipefail

dir=$(cd "$(dirname "$0")" && pwd)
db=${DB_NAME:-gamewe}
psql=${PSQL:-psql}

if [[ -z "${COLLECTOR_PASSWORD:-}" || -z "${WEB_PASSWORD:-}" || -z "${WEB_ADMIN_PASSWORD:-}" ]]; then
	echo "set COLLECTOR_PASSWORD, WEB_PASSWORD and WEB_ADMIN_PASSWORD (see the header of $0)" >&2
	exit 2
fi
if [[ ! "$db" =~ ^[a-z0-9_]+$ ]]; then
	echo "DB_NAME may only contain lower-case letters, digits and _" >&2
	exit 2
fi

schema=$dir/01_schema.sql
[[ -f "$schema" ]] || schema=$dir/../hlstatsx.sql

# Never touch an existing database (it would also reset the role passwords).
existing=$($psql "$@" -d postgres -tAX -v db="$db" <<'SQL'
SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_database WHERE datname = :'db') THEN 'exists' ELSE '' END;
SQL
)
if [[ "$existing" == "exists" ]]; then
	tables=$($psql "$@" -d "$db" -tAX -c "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'")
	if [[ "$tables" != "0" ]]; then
		echo "database $db already has $tables tables; init-db.sh only creates new databases" >&2
		exit 1
	fi
fi

# The passwords are passed as psql variables on stdin, not on the command line.
{
	printf '\\set collector_pw %s\n' "$(printf '%s' "$COLLECTOR_PASSWORD" | sed "s/'/''/g; s/^/'/; s/$/'/")"
	printf '\\set web_pw %s\n' "$(printf '%s' "$WEB_PASSWORD" | sed "s/'/''/g; s/^/'/; s/$/'/")"
	printf '\\set admin_pw %s\n' "$(printf '%s' "$WEB_ADMIN_PASSWORD" | sed "s/'/''/g; s/^/'/; s/$/'/")"
	printf '\\set db %s\n' "$db"
	cat "$dir/00_create_database.sql"
} | $psql "$@" -d postgres -q -v ON_ERROR_STOP=1
echo "database $db and roles created"
$psql "$@" -d "$db" -q -v ON_ERROR_STOP=1 <"$schema"
echo "schema loaded ($(basename "$schema"))"
$psql "$@" -d "$db" -q -v ON_ERROR_STOP=1 <"$dir/02_seed.sql"
echo "reference data checked (02_seed.sql)"
$psql "$@" -d "$db" -q -v ON_ERROR_STOP=1 <"$dir/03_grants.sql"
echo "rights granted (03_grants.sql)"
echo "next: set database.type: postgres, name: $db, user gamewe and its password in /etc/gamewe/collector/config.yaml, then run: gamewe check"
