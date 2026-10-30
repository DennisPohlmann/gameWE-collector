#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# gameWE - collector: copy all data of a gameWE database from MariaDB / MySQL
# to PostgreSQL, or back (docs/DATABASE.md, "Moving to PostgreSQL or back").
#
#   MY_DB=gamewe PG_DB=gamewe ./move-db.sh to-postgres    MariaDB -> PostgreSQL
#   MY_DB=gamewe PG_DB=gamewe ./move-db.sh to-mariadb     PostgreSQL -> MariaDB
#
# Both databases exist and have the same schema version: the target one is a
# new database (init-db.sh) whose tables `gamewe db migrate` created with the
# same collector version. The target's tables are emptied first, then every
# row is copied table by table through the two clients (no file on the
# database server, no FILE right). The collector is stopped meanwhile.
# hlstats_Livestats (players online now) and the version table are not
# copied. At the end the rows of every table are counted on both sides.
#
# Environment:
#   MY_DB         MariaDB / MySQL database (required)
#   PG_DB         PostgreSQL database (required)
#   MYSQL         MariaDB client with its connection options (default: mariadb),
#                 e.g. 'mariadb -h db1 -u gamewe'; the password from ~/.my.cnf
#                 or MYSQL_PWD; to-mariadb needs local_infile on the server
#   PSQL          PostgreSQL client with its options (default: psql), e.g.
#                 'psql -h db2 -U gamewe'; the password from ~/.pgpass or PGPASSWORD
#   TABLE_PREFIX  the table prefix (database.table_prefix, default hlstats)
set -euo pipefail

dir=${1:-}
if [[ "$dir" != to-postgres && "$dir" != to-mariadb ]]; then
	echo "usage: MY_DB=... PG_DB=... $0 to-postgres|to-mariadb (see the header of $0)" >&2
	exit 2
fi
: "${MY_DB:?set MY_DB, the MariaDB database}"
: "${PG_DB:?set PG_DB, the PostgreSQL database}"
prefix=${TABLE_PREFIX:-hlstats}
for v in "$MY_DB" "$PG_DB" "$prefix"; do
	if [[ ! "$v" =~ ^[A-Za-z0-9_]+$ ]]; then
		echo "database names and TABLE_PREFIX may only contain letters, digits and _" >&2
		exit 2
	fi
done
read -r -a my <<<"${MYSQL:-mariadb}"
read -r -a pg <<<"${PSQL:-psql}"
my+=(--batch --raw --skip-column-names --default-character-set=utf8mb4 "$MY_DB")
pg+=(-X -q -A -t -v ON_ERROR_STOP=1 -d "$PG_DB")
versions=${prefix}_gamewe_schema_migrations
lp=$(printf '%s' "$prefix" | tr 'A-Z' 'a-z')

myq() { "${my[@]}" -e "$1"; }
# A MariaDB string literal of its argument as written in SQL (\\ is one backslash).
lit() { printf "'%s'" "$1"; }
pgq() { PGOPTIONS='--client-min-messages=warning' "${pg[@]}" -c "$1"; }

# The same collector version on both sides.
myv=$(myq "SELECT COALESCE(MAX(version), 0) FROM $versions" 2>/dev/null) || myv=0
pgv=$(pgq "SELECT COALESCE(MAX(version), 0) FROM $versions" 2>/dev/null) || pgv=0
if [[ "$myv" != "$pgv" || "$myv" == 0 ]]; then
	echo "schema version: MariaDB $myv, PostgreSQL $pgv; run gamewe db migrate with the same collector version on both first" >&2
	exit 1
fi

# The tables of the MariaDB side (its names keep their case), without the
# version table and hlstats_Livestats.
mapfile -t tables < <(myq "SELECT table_name FROM information_schema.tables
	WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' AND table_name LIKE '${prefix}\\_%'
	  AND LOWER(table_name) NOT IN (LOWER('$versions'), LOWER('${prefix}_Livestats')) ORDER BY table_name")
for t in "${tables[@]}"; do
	if [[ "$(pgq "SELECT to_regclass('$(printf '%s' "$t" | tr 'A-Z' 'a-z')') IS NOT NULL")" != t ]]; then
		echo "$t is missing on PostgreSQL: are both databases gameWE databases of the same version?" >&2
		exit 1
	fi
done
echo "schema version $myv, ${#tables[@]} tables, $dir"

# Columns of a table in MariaDB's order, comma-separated (the same names on
# PostgreSQL, folded to lower case there).
cols() { myq "SELECT GROUP_CONCAT(column_name ORDER BY ordinal_position SEPARATOR ',') FROM information_schema.columns
	WHERE table_schema = DATABASE() AND table_name = '$1'"; }

if [[ "$dir" == to-postgres ]]; then
	pgq "TRUNCATE $(IFS=,; echo "${tables[*]}")"
	for t in "${tables[@]}"; do
		# Each value as text in COPY's text format: \N for NULL; backslash,
		# tab, newline and CR escaped; the zero dates of MariaDB become
		# 1970-01-01 as in the PostgreSQL schema.
		sel=
		while IFS='|' read -r c type; do
			case $type in
			date) e="IF($c < '1000-01-01', '1970-01-01', $c)" ;;
			datetime | timestamp) e="IF($c < '1000-01-01', '1970-01-01 00:00:00', $c)" ;;
			*) e="COALESCE(REPLACE(REPLACE(REPLACE(REPLACE(CAST($c AS CHAR), $(lit '\\'), $(lit '\\\\')), $(lit '\t'), $(lit '\\t')), $(lit '\n'), $(lit '\\n')), $(lit '\r'), $(lit '\\r')), $(lit '\\N'))" ;;
			esac
			sel+=${sel:+, }$e
		done < <(myq "SELECT CONCAT(column_name, '|', data_type) FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = '$t' ORDER BY ordinal_position")
		"${my[@]}" -e "SELECT $sel FROM $t" | PGOPTIONS='--client-min-messages=warning' "${pg[@]}" -c "\\copy $t ($(cols "$t")) FROM STDIN"
	done
	# Identity columns go on after the highest copied id.
	pgq "SELECT setval(pg_get_serial_sequence(table_name, column_name), 1, false)
		FROM information_schema.columns WHERE table_schema = current_schema() AND is_identity = 'YES' AND table_name LIKE '${lp}\\_%'" >/dev/null
	while IFS='|' read -r t c; do
		pgq "SELECT setval(pg_get_serial_sequence('$t', '$c'), MAX($c)) FROM $t HAVING MAX($c) IS NOT NULL" >/dev/null
	done < <(pgq "SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND is_identity = 'YES' AND table_name LIKE '${lp}\\_%'")
else
	for t in "${tables[@]}"; do myq "DELETE FROM $t"; done
	for t in "${tables[@]}"; do
		# COPY's text format is what LOAD DATA reads by default (tab between
		# the fields, \N for NULL, backslash escapes).
		PGOPTIONS='--client-min-messages=warning' "${pg[@]}" -c "\\copy (SELECT $(cols "$t") FROM $t) TO STDOUT" |
			"${my[@]}" --local-infile=1 -e "SET SESSION sql_mode = 'STRICT_ALL_TABLES,NO_AUTO_VALUE_ON_ZERO';
				LOAD DATA LOCAL INFILE '/dev/stdin' INTO TABLE $t CHARACTER SET utf8mb4 ($(cols "$t"))"
	done
fi

# Count both sides.
bad=0
for t in "${tables[@]}"; do
	a=$(myq "SELECT COUNT(*) FROM $t")
	b=$(pgq "SELECT COUNT(*) FROM $t")
	if [[ "$a" != "$b" ]]; then
		echo "$t: MariaDB $a rows, PostgreSQL $b" >&2
		bad=1
	fi
done
if [[ $bad != 0 ]]; then
	echo "the row counts differ (above): do not switch the collector over yet" >&2
	exit 1
fi
echo "done: the rows of all ${#tables[@]} tables are the same on both sides"
echo "next: point database.type, host, port, name of the collector (and DB_TYPE, DB_HOST, DB_PORT of the web) to the new database, run gamewe check and start the collector"
