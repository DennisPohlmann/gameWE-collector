-- gameWE - collector: create the database and its users (run as MySQL / MariaDB root).
--
-- The only SQL to run by hand (init-db.sh does it, docs/DATABASE.md). The
-- tables, the reference data and the table rights of the web users are
-- created by the collector afterwards:  gamewe db migrate
--
-- Before running: replace the database name `gamewe` everywhere (if you want
-- another one), the host '%' (better: the collector's / web server's IP) and
-- the passwords CHANGE_ME_COLLECTOR / CHANGE_ME_WEB / CHANGE_ME_WEB_ADMIN.
-- init-db.sh does the passwords and the name for you.

CREATE DATABASE IF NOT EXISTS `gamewe` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- The collector: reads and writes rows (DELETE is used for
-- hlstats_Livestats, hlstats_PlayersInactive and REPLACE). CREATE, ALTER and
-- INDEX are for `gamewe db migrate`, which creates and upgrades the tables
-- (never DROP: it cannot drop a table); WITH GRANT OPTION lets it give the
-- web users their rights on the tables it created (the web users get only
-- what grants.sql lists, see docs/DATABASE.md).
CREATE USER IF NOT EXISTS 'gamewe'@'%' IDENTIFIED BY 'CHANGE_ME_COLLECTOR';
ALTER USER 'gamewe'@'%' IDENTIFIED BY 'CHANGE_ME_COLLECTOR';   -- also when the user already existed
-- ALTER USER 'gamewe'@'%' REQUIRE SSL;   -- recommended when the database is not local
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX ON `gamewe`.* TO 'gamewe'@'%' WITH GRANT OPTION;

-- The public web pages: read only, but not the RCON passwords. Its rights
-- are per table; gamewe db migrate grants them when it creates the tables.
CREATE USER IF NOT EXISTS 'gamewe_web'@'%' IDENTIFIED BY 'CHANGE_ME_WEB';
ALTER USER 'gamewe_web'@'%' IDENTIFIED BY 'CHANGE_ME_WEB';
-- ALTER USER 'gamewe_web'@'%' REQUIRE SSL;

-- The web admin pages: read everything here (including RCON passwords, to
-- manage the servers); the rights to edit settings, reference tables and
-- players are per table (gamewe db migrate grants them).
-- No admin account is created in hlstats_Users: use the web frontend's
-- `npm run create-admin -- <name>` (passwords are scrypt$salt$hash).
CREATE USER IF NOT EXISTS 'gamewe_web_admin'@'%' IDENTIFIED BY 'CHANGE_ME_WEB_ADMIN';
ALTER USER 'gamewe_web_admin'@'%' IDENTIFIED BY 'CHANGE_ME_WEB_ADMIN';
-- ALTER USER 'gamewe_web_admin'@'%' REQUIRE SSL;
GRANT SELECT ON `gamewe`.* TO 'gamewe_web_admin'@'%';
