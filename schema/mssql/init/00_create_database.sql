-- gameWE - collector: create the SQL Server logins, database and users (run as sysadmin).
--
-- The only SQL to run by hand (init-db.sh does it, docs/DATABASE.md). The
-- tables, the reference data and the table rights of the web users are
-- created by the collector afterwards:  gamewe db migrate
-- The collector user gamewe is db_owner of the database: it creates and
-- alters the tables and grants the web users their rights on them.
--
-- sqlcmd scripting variables (init-db.sh sets them as environment variables,
-- so the passwords are not on the command line):
--   db            database name, e.g. gamewe
--   collector_pw  password of the collector login gamewe
--   web_pw        password of the read-only login gamewe_web
--   admin_pw      password of the web admin login gamewe_web_admin
--
-- The collation is case-insensitive (as MariaDB's utf8mb4_unicode_ci); the
-- text columns are nvarchar, so any Unicode text fits.

:on error exit
SET NOCOUNT ON;

IF DB_ID(N'$(db)') IS NULL CREATE DATABASE [$(db)] COLLATE Latin1_General_100_CI_AS_SC;
GO
IF SUSER_ID(N'gamewe') IS NULL CREATE LOGIN gamewe WITH PASSWORD = N'$(collector_pw)', CHECK_POLICY = OFF, DEFAULT_DATABASE = [$(db)];
ELSE ALTER LOGIN gamewe WITH PASSWORD = N'$(collector_pw)';
IF SUSER_ID(N'gamewe_web') IS NULL CREATE LOGIN gamewe_web WITH PASSWORD = N'$(web_pw)', CHECK_POLICY = OFF, DEFAULT_DATABASE = [$(db)];
ELSE ALTER LOGIN gamewe_web WITH PASSWORD = N'$(web_pw)';
IF SUSER_ID(N'gamewe_web_admin') IS NULL CREATE LOGIN gamewe_web_admin WITH PASSWORD = N'$(admin_pw)', CHECK_POLICY = OFF, DEFAULT_DATABASE = [$(db)];
ELSE ALTER LOGIN gamewe_web_admin WITH PASSWORD = N'$(admin_pw)';
GO
USE [$(db)];
IF USER_ID(N'gamewe') IS NULL CREATE USER gamewe FOR LOGIN gamewe;
IF USER_ID(N'gamewe_web') IS NULL CREATE USER gamewe_web FOR LOGIN gamewe_web;
IF USER_ID(N'gamewe_web_admin') IS NULL CREATE USER gamewe_web_admin FOR LOGIN gamewe_web_admin;
ALTER ROLE db_owner ADD MEMBER gamewe;
GO
