-- gameWE - collector: create the PostgreSQL roles and database (run as superuser).
--
-- Step 1 of 4 (init-db.sh runs all of them):
--   00_create_database.sql   roles and database          (this file)
--   01_schema.sql            tables and default data (= schema/postgres/hlstatsx.sql)
--   02_seed.sql              default data again (idempotent; already in 01)
--   03_grants.sql            rights of the roles
--
-- psql variables (init-db.sh sets them):
--   db            database name, e.g. gamewe
--   collector_pw  password of the collector role gamewe
--   web_pw        password of the read-only role gamewe_web
--   admin_pw      password of the web admin role gamewe_web_admin
--
--   psql -v db=gamewe -v collector_pw=... -v web_pw=... -v admin_pw=... -f 00_create_database.sql
--
-- Restrict access in pg_hba.conf (e.g. hostssl gamewe gamewe 203.0.113.5/32 scram-sha-256).

\set ON_ERROR_STOP on

SELECT 'CREATE ROLE gamewe LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gamewe') \gexec
SELECT 'CREATE ROLE gamewe_web LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gamewe_web') \gexec
SELECT 'CREATE ROLE gamewe_web_admin LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gamewe_web_admin') \gexec
ALTER ROLE gamewe LOGIN PASSWORD :'collector_pw';
ALTER ROLE gamewe_web LOGIN PASSWORD :'web_pw';
ALTER ROLE gamewe_web_admin LOGIN PASSWORD :'admin_pw';

SELECT format('CREATE DATABASE %I ENCODING ''UTF8'' TEMPLATE template0', :'db')
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'db') \gexec
