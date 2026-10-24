-- gameWE - collector: create the PostgreSQL roles and database (run as superuser).
--
-- The only SQL to run by hand (init-db.sh does it, docs/DATABASE.md). The
-- tables, the reference data and the table rights of the web roles are
-- created by the collector afterwards:  gamewe db migrate
-- The collector role gamewe may create tables in the schema public and owns
-- them (only the owner may alter a table); it grants the web roles their
-- rights on them.
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

SELECT format('GRANT CONNECT ON DATABASE %I TO gamewe, gamewe_web, gamewe_web_admin', :'db') \gexec
\connect :"db"
GRANT USAGE, CREATE ON SCHEMA public TO gamewe;
GRANT USAGE ON SCHEMA public TO gamewe_web, gamewe_web_admin;
