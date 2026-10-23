-- gameWE - collector: create the database and its users (run as MySQL / MariaDB root).
--
-- Step 1 of 4 (docs/DATABASE.md, or init-db.sh to run all steps):
--   00_create_database.sql   database and users          (this file)
--   01_schema.sql            tables (= schema/hlstatsx.sql)
--   02_seed.sql              reference data again (idempotent; already in 01)
--   03_grants.sql            table rights of the web users
--
-- Before running: replace the database name `gamewe` everywhere (if you want
-- another one), the host '%' (better: the collector's / web server's IP) and
-- the passwords CHANGE_ME_COLLECTOR / CHANGE_ME_WEB / CHANGE_ME_WEB_ADMIN.
-- init-db.sh does the passwords and the name for you.

CREATE DATABASE IF NOT EXISTS `gamewe` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- The collector: reads and writes rows, never changes the structure.
-- DELETE is used for hlstats_Livestats, hlstats_PlayersInactive and REPLACE.
CREATE USER IF NOT EXISTS 'gamewe'@'%' IDENTIFIED BY 'CHANGE_ME_COLLECTOR';
ALTER USER 'gamewe'@'%' IDENTIFIED BY 'CHANGE_ME_COLLECTOR';   -- also when the user already existed
-- ALTER USER 'gamewe'@'%' REQUIRE SSL;   -- recommended when the database is not local
GRANT SELECT, INSERT, UPDATE, DELETE ON `gamewe`.* TO 'gamewe'@'%';

-- The public web pages: read only, but not the RCON passwords. Its rights
-- are per table and therefore in 03_grants.sql.
CREATE USER IF NOT EXISTS 'gamewe_web'@'%' IDENTIFIED BY 'CHANGE_ME_WEB';
ALTER USER 'gamewe_web'@'%' IDENTIFIED BY 'CHANGE_ME_WEB';
-- ALTER USER 'gamewe_web'@'%' REQUIRE SSL;

-- The web admin pages: read everything here (including RCON passwords, to
-- manage the servers); the rights to edit settings, reference tables and
-- players are in 03_grants.sql (they need the tables to exist).
-- No admin account is created in hlstats_Users: use the web frontend's
-- `npm run create-admin -- <name>` (passwords are scrypt$salt$hash).
CREATE USER IF NOT EXISTS 'gamewe_web_admin'@'%' IDENTIFIED BY 'CHANGE_ME_WEB_ADMIN';
ALTER USER 'gamewe_web_admin'@'%' IDENTIFIED BY 'CHANGE_ME_WEB_ADMIN';
-- ALTER USER 'gamewe_web_admin'@'%' REQUIRE SSL;
GRANT SELECT ON `gamewe`.* TO 'gamewe_web_admin'@'%';
